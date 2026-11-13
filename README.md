# vireo-mcp

The **MCP bridge** for [vireo](https://github.com/ground-creative/vireo). It is
one static binary that speaks **MCP** to an AI host (Claude Desktop, Cursor, VS
Code, anything MCP) and the **vireo wire protocol** to a daemon.

```
Claude Desktop ──MCP (stdio or HTTP)──▶ vireo-mcp ──vireo/1 (TLS/QUIC)──▶ vireo ──HTTP──▶ your backend
```

Install it once. Its tools let the assistant discover and talk to any vireo
endpoint at runtime — no per-service MCP server, no per-vendor configuration.

## Tools

| Tool | What it does |
|---|---|
| `discover` | probe a domain for a vireo daemon without switching the current connection |
| `connect` | connect to a business and list the agents behind it |
| `conversations` | list your previous conversations with one agent (dates, summaries, turn counts) |
| `new_conversation` | start a new conversation with an agent |
| `resume_conversation` | resume a previous one, by number or the latest |
| `send_message` | send a message to the current agent and get its reply |
| `close_conversation` | close the current conversation |
| `about` | report the version, build, identity and current connection |

The flow an assistant follows:

```
connect {host} → choose an agent → conversations/new_conversation → send_message
```

## Install

```bash
git clone https://github.com/ground-creative/vireo-mcp.git
cd vireo-mcp
go build -o vireo-mcp ./dialer
```

## Load it into your client

### Claude Desktop

`~/Library/Application Support/Claude/claude_desktop_config.json` (macOS) or
`%APPDATA%\Claude\claude_desktop_config.json` (Windows):

```json
{
  "mcpServers": {
    "vireo": {
      "command": "/absolute/path/to/vireo-mcp"
    }
  }
}
```

Restart Claude Desktop. The `vireo` server appears in the tools menu.

### Cursor

`.cursor/mcp.json` in your project, or `~/.cursor/mcp.json` for all projects:

```json
{
  "mcpServers": {
    "vireo": {
      "command": "/absolute/path/to/vireo-mcp"
    }
  }
}
```

### VS Code

`.vscode/mcp.json` in your workspace:

```json
{
  "servers": {
    "vireo": {
      "type": "stdio",
      "command": "/absolute/path/to/vireo-mcp"
    }
  }
}
```

Any other MCP host: point it at the binary with the standard stdio transport —
one JSON-RPC message per line on stdin, one per line on stdout.

## Identity

The daemon scopes conversations by **client id**, so the bridge needs one, and
it must be the same every time or your conversations vanish.

**Over stdio** the identity is generated once and stored locally, under
`$XDG_CONFIG_HOME/vireo-mcp/<label>.id` (`~/.config/vireo-mcp/default.id` on
Linux). Delete the file to start as somebody else. The `-name` flag chooses
which local identity to use — one per host application, so Claude Desktop and
Cursor do not share conversations.

**Over HTTP** there is no local file to rely on, so identity comes from an
authorization token instead. The token is bound to a client id the server
generates once and keeps:

```bash
vireo-mcp -http-addr 127.0.0.1:8091
```

```
vireo-mcp: authorization token (send it as a bearer token on every request):
  Authorization: Bearer vmcp_ec418d0a9c2e44f6bccc8f32b5dbf69d
vireo-mcp: client id for that token: vireo_1a30b91e8c38d8907a8a9e843488ab17
```

Mint more tokens for other callers:

```bash
vireo-mcp -issue-token          # prints a token, its client id, the header
vireo-mcp -list-tokens          # what is registered
```

Tokens live in `$XDG_CONFIG_HOME/vireo-mcp/<label>.callers.json`, so a token
keeps working after the process restarts.

## HTTP transport

`-http-addr` serves MCP over Streamable HTTP instead of stdio. Useful for
remote hosts, containers, or driving the server with curl.

```
POST /mcp
GET  /mcp      (text/event-stream, liveness)
GET  /health
GET  /routes
GET  /          (a live HTML view of the traffic)
```

Every request to `/mcp` must carry the authorization token:

```
POST /mcp HTTP/1.1
Host: 127.0.0.1:8091
Content-Type: application/json
Authorization: Bearer vmcp_ec418d0a9c2e44f6bccc8f32b5dbf69d

{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"connect","arguments":{"host":"localhost:7777"}}}
```

```json
{"jsonrpc":"2.0","id":1,"result":{"content":[{"type":"text","text":"Connected to \"restaurant\" (restaurant-dev) at localhost:7777 via explicit address.\nThings you can do here:\n  orders — Take food orders: pizzas, sides, drinks, pickup or delivery [conversational]"}],"isError":false}}
```

Without a token, or with one the server does not know:

```
HTTP/1.1 401 Unauthorized
WWW-Authenticate: Bearer realm="vireo-mcp"

{"jsonrpc":"2.0","id":null,"error":{"code":-32001,"message":"missing authorization — send: Authorization: Bearer <token>"}}
```

A bare token without the `Bearer ` prefix is accepted too, which makes curl
easier.

## Talk to a daemon

Domains resolve through their SRV record — `_vireo._tcp.<domain>` — and
nothing else. A domain with no record must be addressed as `host:port`:

```
_vireo._tcp.example.com. 300 IN SRV 10 5 7777 agent.example.com.
```

## Flags

| Flag | Default | Meaning |
|---|---|---|
| `-name` | `default` | label for this installation (display only; the id is generated) |
| `-http-addr` | — | serve MCP over Streamable HTTP on this address instead of stdio |
| `-insecure` | `true` | skip daemon TLS verification (development only) |
| `-timeout` | `15s` | per-request timeout toward the daemon |
| `-issue-token` | — | mint an authorization token for the HTTP transport and print it |
| `-list-tokens` | — | print the registered tokens and their client ids |
| `-log` | — | write a JSON-RPC trace to this file, or `-` for stderr |
| `-tools` | — | print the tool catalog as JSON and exit |
| `-routes` | — | print the transport/route table and exit |
| `-version` | — | print name and version and exit |

Environment: `VIREO_MCP_ID` overrides the client id.

## Drive it by hand

One JSON-RPC message per line on stdin — exactly what an MCP host sends:

```bash
printf '%s\n' \
  '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"t","version":"1"}}}' \
  '{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"connect","arguments":{"host":"localhost:7777"}}}' \
  '{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"new_conversation","arguments":{"agent":"orders"}}}' \
  '{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"send_message","arguments":{"message":"hi, I want a margherita"}}}' \
  | ./vireo-mcp
```

```
{"jsonrpc":"2.0","id":4,"result":{"content":[{"type":"text","text":"This is not an AI. No model runs behind this endpoint — the daemon, the session and the transport are real, the answers are not…"}],"isError":false}}
```

## Build and test

```bash
go build ./...
go test ./...
go vet ./...
```

The daemon side of the protocol — frames, manifest, messages, sessions — is
specified in the vireo repository: [`docs/PROTOCOL.md`](https://github.com/ground-creative/vireo/blob/main/docs/PROTOCOL.md).
