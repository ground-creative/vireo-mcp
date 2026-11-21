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

| Tool                  | What it does                                                                    |
| --------------------- | ------------------------------------------------------------------------------- |
| `discover`            | probe a domain for a vireo daemon without switching the current connection      |
| `connect`             | connect to a business and list the agents behind it                             |
| `conversations`       | list your previous conversations with one agent (dates, summaries, turn counts) |
| `new_conversation`    | start a new conversation with an agent                                          |
| `resume_conversation` | resume a previous one, by number or the latest                                  |
| `send_message`        | send a message to the current agent and get its reply                           |
| `close_conversation`  | close the current conversation                                                  |
| `about`               | report the version, build, identity and current connection                      |

The flow an assistant follows:

```
connect {host} → choose an agent → conversations/new_conversation → send_message
```

When an agent has a `start_message` in its configuration, `new_conversation`
returns that greeting as the text of the tool result: the conversation opens
with the agent's own words instead of a bare confirmation.

## Live demo

The live endpoint is [ziwa.ai](https://ziwa.ai), found through
`_vireo._tcp.ziwa.ai`. The flow, against an example business:

```
connect acme-plumbing.example
  connected to "acme-plumbing" at agent.acme-plumbing.example:7777 via SRV record

  bookings — Book a visit, change or cancel an appointment
  support  — Questions about service, coverage and pricing

new_conversation support
  What can I help you with today?

send_message "do you cover the north side?"
  Yes — everything north of the river, with same-day slots before 11am.
   [open · turn 1] "coverage north side"
```

That is the whole setup: one host name, no vendor-specific tool. The next
`send_message` continues the same conversation, and `conversations` lists it by
its summary next time.

## Install

```bash
go install github.com/ground-creative/vireo-mcp@latest
```

The binary lands in `$(go env GOPATH)/bin` — `~/go/bin` on most machines. Put
that on your `PATH` so hosts can start it by name:

```bash
export PATH="$PATH:$(go env GOPATH)/bin"
```

Check that it runs before wiring it into a host:

```bash
command -v vireo-mcp      # where the shell finds it
vireo-mcp -version        # e.g. vireo-mcp 0.5.0
```

That is the whole install: one static binary, no data files of its own. From
source, if you prefer:

```bash
git clone https://github.com/ground-creative/vireo-mcp.git
cd vireo-mcp
go build -o vireo-mcp .
```

Tagged releases with prebuilt binaries for Linux and macOS (amd64, arm64) are
intended so you can skip the Go toolchain — nothing is published yet, so install
with `go install` or build from source for now.

Only the bridge is installed per AI host. The daemon side (`vireo`) is separate
and is always built from source, because it needs `config/`, `certs/` and
`sessions/` next to the binary.

## Load it into your client

### Claude Desktop

`~/Library/Application Support/Claude/claude_desktop_config.json` (macOS) or
`%APPDATA%\Claude\claude_desktop_config.json` (Windows):

```json
{
  "mcpServers": {
    "vireo": {
      "command": "vireo-mcp"
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
      "command": "vireo-mcp"
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
      "command": "vireo-mcp"
    }
  }
}
```

Any other MCP host: point it at the binary with the standard stdio transport —
one JSON-RPC message per line on stdin, one per line on stdout.

### If the host cannot start it

`"command": "vireo-mcp"` only works when the host inherits a `PATH` that
contains `$(go env GOPATH)/bin`. Desktop hosts usually do not: Claude Desktop,
and anything launched from a dock or an app bundle, starts with a minimal `PATH`
such as `/usr/bin:/bin:/usr/sbin:/sbin`. The binary is invisible to them even
though your terminal finds it fine, and the host reports the server as failed —
`spawn vireo-mcp ENOENT`, `MCP server "vireo" failed to start`, or
`command not found` in its log.

Give it the absolute path instead:

```json
{
  "servers": {
    "vireo": {
      "type": "stdio",
      "command": "/home/you/go/bin/vireo-mcp"
    }
  }
}
```

Get the exact path to paste:

```bash
go env GOBIN     # if set, the binary is $GOBIN/vireo-mcp
go env GOPATH    # otherwise it is $GOPATH/bin/vireo-mcp
```

On Windows that is `%USERPROFILE%\go\bin\vireo-mcp.exe`.

Two things mislead people here: `which vireo-mcp` succeeding in your terminal
says nothing about a host launched from a GUI, because the two do not share an
environment; and adding the directory to `~/.bashrc` or `~/.zshrc` does not help
either, since a GUI host never reads it. The absolute path always works.

### Over HTTP

For a remote host, a container, or one server shared by several assistants.
Start it with `-http-addr`: it prints the bearer token and the client id that
token is bound to, and [HTTP transport](#http-transport) has the full output plus
the token flags.

Then point the host at the URL and send that token in the `Authorization`
header.

**Claude Desktop** — `claude_desktop_config.json`:

```json
{
  "mcpServers": {
    "vireo": {
      "type": "http",
      "url": "http://127.0.0.1:8091/mcp",
      "headers": {
        "Authorization": "Bearer vmcp_…"
      }
    }
  }
}
```

**Cursor** — `.cursor/mcp.json`:

```json
{
  "mcpServers": {
    "vireo": {
      "url": "http://127.0.0.1:8091/mcp",
      "headers": {
        "Authorization": "Bearer vmcp_…"
      }
    }
  }
}
```

**VS Code** — `.vscode/mcp.json`:

```json
{
  "servers": {
    "vireo": {
      "type": "http",
      "url": "http://127.0.0.1:8091/mcp",
      "headers": {
        "Authorization": "Bearer vmcp_…"
      }
    }
  }
}
```

Every request to `/mcp` needs that header; without it the server answers `401`.
A different token means a different identity, so each assistant — and each
user — should have its own.

## Identity

The daemon scopes conversations by **client id**, so the bridge needs one, and
it must be the same every time or your conversations vanish.

**Over stdio** the identity is generated once and stored locally, under
`$XDG_CONFIG_HOME/vireo-mcp/<label>.id` (`~/.config/vireo-mcp/default.id` on
Linux). Delete the file to start as somebody else. The `-name` flag chooses
which local identity to use — one per host application, so Claude Desktop and
Cursor do not share conversations.

**Over HTTP** there is no local file to rely on, so identity comes from an
authorization token instead. The token is bound to a client id that the server
generates once and keeps; the startup output prints both — see
[HTTP transport](#http-transport).

Mint more tokens for other callers:

```bash
vireo-mcp -issue-token          # prints a token, its client id, the header
vireo-mcp -list-tokens          # what is registered
```

Tokens live in `$XDG_CONFIG_HOME/vireo-mcp/<label>.callers.json`, so a token
keeps working after the process restarts.

## Progress while a turn runs

An agent that knows a turn will be slow can report progress instead of leaving
the host staring at a spinner. When the daemon relays such a line, `send_message`
emits an MCP notification over stdio:

```json
{
  "jsonrpc": "2.0",
  "method": "notifications/progress",
  "params": {
    "progressToken": "tok",
    "progress": 1,
    "message": "fetching your site…"
  }
}
```

The token comes from `params._meta.progressToken` in the `tools/call` request; a
host that does not send one gets no notifications, and the call simply returns
when the answer is ready. The final tool result is unchanged, and the turn count
still increments once — progress is not a turn. If the backend goes quiet for
more than ten seconds, a generic "still working…" line keeps the call alive.

Over HTTP there is no notification channel: one POST yields one response, so
progress is silent there.

## Cancelling a call

A host that stops a tool call sends the standard MCP cancellation:

```json
{
  "jsonrpc": "2.0",
  "method": "notifications/cancelled",
  "params": { "requestId": 4, "reason": "user pressed stop" }
}
```

`vireo-mcp` matches `requestId` to the tool call still running and does two
things:

1. stops waiting for the answer;
2. sends a `cancel` frame to the daemon naming that turn's stream, so the daemon
   cancels the backend request too.

The abandoned call is answered with the MCP code for a cancelled request:

```json
{
  "jsonrpc": "2.0",
  "id": 4,
  "error": {
    "code": -32800,
    "message": "cancelled by the host — the turn may still have completed on the daemon"
  }
}
```

**Cancellation is not a rollback.** The message already reached the daemon
before the host could cancel it, so the daemon can stop waiting and can abort the
upstream request, but it cannot undo work the backend already committed. A build
that had already provisioned an assistant stays provisioned. A cancelled turn is
not counted in the session: `turns` and the summary are left untouched.

To make this work the read loop has to stay free while a call runs, so
`tools/call` is dispatched in its own goroutine. A blocking loop could never read
the cancellation — it would sit in the pipe until the turn finished, which is
exactly the bug this replaced.

There is one more consequence worth knowing: a request that has already been
written to the daemon is **never retried**. Retrying a turn whose read failed
would deliver the same message twice. Only a request that never reached the wire
is retried, so a daemon restart still looks like a slightly slower call.

Over HTTP there is no server-to-client notification stream per call; the cancel
is honoured on a best-effort basis for the caller's own instance.

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
{
  "jsonrpc": "2.0",
  "id": 1,
  "result": {
    "content": [
      {
        "type": "text",
        "text": "Connected to \"restaurant\" (restaurant-dev) at localhost:7777 via explicit address.\nThings you can do here:\n  orders — Take food orders: pizzas, sides, drinks, pickup or delivery [conversational]"
      }
    ],
    "isError": false
  }
}
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

| Flag           | Default   | Meaning                                                         |
| -------------- | --------- | --------------------------------------------------------------- |
| `-name`        | `default` | label for this installation (display only; the id is generated) |
| `-http-addr`   | —         | serve MCP over Streamable HTTP on this address instead of stdio |
| `-insecure`    | `false`   | skip daemon TLS verification (unsafe; local development only)   |
| `-timeout`     | `120s`    | per-request timeout toward the daemon                           |
| `-issue-token` | —         | mint an authorization token for the HTTP transport and print it |
| `-list-tokens` | —         | print the registered tokens and their client ids                |
| `-log`         | —         | write a JSON-RPC trace to this file, or `-` for stderr          |
| `-tools`       | —         | print the tool catalog as JSON and exit                         |
| `-routes`      | —         | print the transport/route table and exit                        |
| `-version`     | —         | print name and version and exit                                 |

Environment: `VIREO_MCP_ID` overrides the client id.

Certificate verification is on by default: the certificate must cover the domain
you asked for. A daemon on `localhost` with a certificate from
`scripts/gen-certs.sh` is not trusted by the system, so local runs need
`-insecure`, which prints a warning. See `SEC-6` and `SEC-7` in the
[specification](https://github.com/ground-creative/agent-front-door-spec).

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
