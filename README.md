# vireo-mcp

**The MCP bridge for [Portico](https://github.com/ground-creative/agent-front-door-spec).**
One static binary that speaks **MCP** to an AI host and **`portico/1`** to a
remote endpoint, so any MCP-speaking assistant can discover and talk to agents a
business publishes at its own domain.

```text
AI host ──MCP (stdio or HTTP)──▶ vireo-mcp ──portico/1 (TLS 1.3 / QUIC)──▶ vireo ──HTTP──▶ the business's backend
```

Install it once, register it with your host once, and then any domain that runs a
Portico endpoint is reachable at runtime. No per-service server, no per-vendor
configuration, and no Go toolchain if you use a release binary.

> **Status.** `vireo-mcp` is one half of a reference implementation. The protocol
> it speaks is not a ratified standard, and the live endpoint below is an example
> deployment — not a public service directory.

**Start here:**

| If you want to…                                            | Go to                                                                 |
| ---------------------------------------------------------- | --------------------------------------------------------------------- |
| talk to a real endpoint in five minutes                     | [Try the live endpoint](#1-try-the-live-endpoint)                     |
| point the bridge at your own domain                         | [Connect to your own endpoint](#2-connect-to-your-own-portico-endpoint) |
| understand how the pieces fit                               | [Architecture](#3-architecture)                                       |
| see an existing agent runtime exposed without rewriting it  | [Backend integration](#4-backend-integration-an-existing-agent-runtime) |
| fix something that is not working                           | [Troubleshooting](#troubleshooting)                                   |
| send a patch                                                | [Contributing](#contributing)                                         |

## Contents

- [1. Try the live endpoint](#1-try-the-live-endpoint)
- [2. Connect to your own Portico endpoint](#2-connect-to-your-own-portico-endpoint)
- [3. Architecture](#3-architecture)
- [4. Backend integration: an existing agent runtime](#4-backend-integration-an-existing-agent-runtime)
- [Install](#install)
- [MCP host configuration](#mcp-host-configuration)
- [Tools](#tools)
- [Identity](#identity)
- [Progress, cancellation and transport differences](#progress-cancellation-and-transport-differences)
- [Troubleshooting](#troubleshooting)
- [Contributing](#contributing)
- [Reference](#reference)

---

## 1. Try the live endpoint

A Portico endpoint is running at **`ziwa.ai`**. You do not need to run a daemon,
own a domain, or write any code: install the bridge, register it with your host,
and ask your assistant to connect.

Verified against `ziwa.ai` while writing this README:

```
_portico._tcp.ziwa.ai.  300 IN SRV 10 5 7778 vireo.ziwa.ai.
```

The endpoint publishes two agents, both conversational:

| Agent                  | What it is for                                                                                        | Greeting                                           |
| ---------------------- | ----------------------------------------------------------------------------------------------------- | -------------------------------------------------- |
| `sales-representative` | Chat with Martin, our Solutions Advisor, about our IT services, tech stack, or booking a consultation. | "Hi, I'm Martin! …"                                |
| `vireo-representative` | Discuss Vireo, its architecture, design decisions and AI agent interoperability.                      | "Hi! I'm Vireo's AI representative. …"             |

Both hold multi-turn conversations. The endpoint advertises 60 requests/minute
with a burst of 10, and 30/minute with a burst of 5 for `sales-representative`
specifically.

**What is public and what is not.** Discovery, the manifest and conversations on
`ziwa.ai` are open to anyone who can reach the daemon — there is no API key and no
account. That is a property of *this deployment*, not of Portico: an independently
operated endpoint may require a client credential, and this bridge does not
currently carry one (see [known limitations](#known-limitations)). The bridge's own
bearer tokens authenticate an **MCP host to the bridge over HTTP** — they have
nothing to do with the endpoint.

### Step 1 — get the binary

Take the archive for your machine from
[GitHub Releases](https://github.com/ground-creative/vireo-mcp/releases) and follow
[Install](#install). If you would rather build it:

```bash
go install github.com/ground-creative/vireo-mcp@latest
```

Check that it runs before wiring anything up:

```bash
vireo-mcp -version
# release binary:  vireo-mcp 0.9.0 (commit a1b2c3d, built 2026-11-28T01:45:43Z)
# go install:      vireo-mcp 0.8.0 (commit dev, built unknown)
```

`go install` and a plain `go build` carry no build stamps, so `dev` / `unknown`
there is expected — a release archive is the only place the commit and build time
are filled in.

### Step 2 — register it with your host

The host launches the bridge and talks to it over stdio. An absolute path is
safest — [MCP host configuration](#mcp-host-configuration) explains why, and has
the Cursor and VS Code variants.

Claude Desktop — `~/Library/Application Support/Claude/claude_desktop_config.json`
(macOS) or `%APPDATA%\Claude\claude_desktop_config.json` (Windows):

```json
{
  "mcpServers": {
    "vireo": {
      "command": "/home/you/.local/bin/vireo-mcp"
    }
  }
}
```

Restart the host; `vireo` appears in its tool list. `ziwa.ai` chains to a public
CA, so no certificate flag is needed.

### Step 3 — connect and discover the agents

Ask the assistant, in your own words, to connect to `ziwa.ai`. The tool call it
makes is `connect`:

```json
{"name": "connect", "arguments": {"host": "ziwa.ai"}}
```

The bridge resolves `_portico._tcp.ziwa.ai`, opens a TLS connection to
`vireo.ziwa.ai:7778`, reads the manifest and verifies its digest. What comes back:

```
Connected to "ziwa.ai" (ziwa) at vireo.ziwa.ai:7778 via SRV record.
Things you can do here:
  sales-representative — Chat with Martin, our Solutions Advisor, … [conversational]
      tags: sales, consultation, services
  vireo-representative — Discuss Vireo, its architecture, … [conversational]
      tags: vireo, protocol, interoperability
Published references:
  Ziwa — https://ziwa.ai
  API docs — https://api.ziwa.ai/docs/api/v1
Start with conversations {agent} or new_conversation {agent}, then send_message.

The manifest this endpoint publishes (portico/1). This document is the contract: …
digest verified: sha256:0d82e798e749836cefdcf2b4afb7eca83442caea46f6116852e869c2233a78e7
{"manifest_version":"1.3","protocol":"portico/1", … }
```

`discover` does the same probe **without** switching the current connection, which
is what you want when checking a domain you are not committing to.

### Step 4 — start a conversation

A conversational agent needs a conversation before it can be messaged:

```json
{"name": "new_conversation", "arguments": {"agent": "vireo-representative"}}
```

```
New conversation with vireo-representative started.

Hi! I'm Vireo's AI representative. What would you like to discuss?
```

The greeting is the agent's own `start_message` from the manifest. The bridge shows
it rather than a bare confirmation, because the backend is told it was already said
and will not repeat it.

Use `conversations` first if you want to know what you were already discussing:

```json
{"name": "conversations", "arguments": {"agent": "vireo-representative"}}
```

```
Your conversations with vireo-representative:
  [1] Nov 28 01:43 — Portico is a digital-preservation service … (1 turns)
Resume with resume_conversation {number} or {latest}, or start fresh with new_conversation.
```

### Step 5 — send messages

```json
{"name": "send_message", "arguments": {"message": "What is Portico in one sentence?"}}
```

```
Portico is a digital-preservation service that archives scholarly e-content to ensure long-term access.
[Nov 28 01:43, turn 1] "Portico is a digital-preservation service that archives scholarly e-content to ensure long…"
```

Each further `send_message` continues **the same conversation**: the daemon keeps
the session and its summary, so the next question carries the thread instead of
starting over. The bracketed line is the session envelope — when the turn happened,
the turn count, and the summary the agent supplied.

### Step 6 — resume a conversation later

Conversations outlive both your host and the bridge, because they live on the
daemon, scoped to your [client identity](#identity) rather than to a process. After
connecting to `ziwa.ai` again:

```json
{"name": "conversations", "arguments": {"agent": "vireo-representative"}}
{"name": "resume_conversation", "arguments": {"agent": "vireo-representative", "latest": true}}
{"name": "send_message", "arguments": {"message": "And how does that compare to A2A?"}}
```

`{"number": 1}` resumes a specific entry from the `conversations` list. A closed
conversation is history only; the bridge says so and points at
`new_conversation` instead of letting the daemon reject the message.

**The same flow without a host**, for scripting and debugging — one JSON-RPC
message per line on stdin, exactly what a host sends:

```bash
(
  printf '%s\n' \
    '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"shell","version":"1"}}}' \
    '{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"connect","arguments":{"host":"ziwa.ai"}}}'
  sleep 10
  printf '%s\n' \
    '{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"new_conversation","arguments":{"agent":"vireo-representative"}}}'
  sleep 8
  printf '%s\n' \
    '{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"send_message","arguments":{"message":"What is Portico?"}}}'
  sleep 40
) | vireo-mcp -timeout 60s
```

stdin must stay open for as long as the calls run: `vireo-mcp` exits when stdin
closes, so a bare `printf | vireo-mcp` is only reliable for non-blocking commands
like `-tools` and `-routes`.

---

## 2. Connect to your own Portico endpoint

`ziwa.ai` above is just one instance of this. The bridge keeps no endpoint list:
you give it a host, and the host decides everything else.

### What the endpoint must publish

A daemon is reachable in exactly two ways.

**By DNS.** The domain publishes an SRV record naming host and port:

```
_portico._tcp.example.com. 300 IN SRV 10 5 7777 agent.example.com.
```

Then `connect {"host": "example.com"}` needs nothing else. The bridge resolves the
record, connects to `agent.example.com:7777`, and verifies the certificate against
**`example.com`** — the name you asked for, not the SRV target, so a DNS answer
cannot silently move which certificate is acceptable.

There is no default port and no well-known URL. A domain without a record must be
addressed explicitly.

**By explicit address.** For a local daemon, a container or a port forward:

```json
{"name": "connect", "arguments": {"host": "agent.example.com:7777"}}
{"name": "connect", "arguments": {"host": "127.0.0.1:7777"}}
```

The retired `_vireo._tcp` record is still honoured as a fallback, and the reply
says which record answered — so an unmigrated deployment is visible rather than
mysterious.

### Certificates

Verification is **on by default**. The usual local problem is a development
certificate from the daemon's `scripts/gen-certs.sh` that no public CA chains to.
Two answers, depending on how long the setting should live.

**`-ca`, for anything that might outlive the test.** It trusts one PEM bundle and
keeps verification on: the peer must still be signed by that bundle *and* cover the
name you asked for.

```json
{
  "mcpServers": {
    "vireo": {
      "command": "/home/you/.local/bin/vireo-mcp",
      "args": ["-ca", "/abs/path/to/vireo/certs/cert.pem"]
    }
  }
}
```

System roots are kept, so `-ca` is additive, never a replacement. Restart the
bridge after `gen-certs.sh` regenerates the file — the bundle is read at startup.
`SSL_CERT_FILE` also works, because Go reads it as the root bundle, but it
*replaces* the system roots for the whole process; prefer `-ca`.

**`-insecure`, for a disposable local run only.** It turns verification off: the
traffic is still encrypted, but nothing establishes who answered the call.

```json
{"args": ["-insecure"]}
```

A deployment with a public certificate needs no flag at all.

### Authentication

Two independent layers, and it matters which one you are configuring.

**MCP host → bridge, over HTTP.** If several assistants or several people share one
bridge, run it with `-http-addr` and give each caller its own bearer token:

```bash
vireo-mcp -http-addr 127.0.0.1:8091
# prints: Authorization: Bearer vmcp_…
```

Every `/mcp` request must then send `Authorization: Bearer <token>`. Each token is
bound to a client id, so each caller gets its own conversation history. See
[HTTP transport](#http-transport).

**Bridge → endpoint.** `portico/1` as implemented has no client credential in the
handshake: identity toward the daemon is the client id the bridge generates, and
the daemon decides what that id may do. If an endpoint needs more than that, put
it behind your own network control — a private address, a tunnel, a firewall rule.
The bridge has no flag to present an API key to a daemon.

### Selecting agents

There is no agent registry to query; the manifest **is** the list.

```json
{"name": "discover", "arguments": {"host": "example.com"}}   // probe, no commitment
{"name": "connect",  "arguments": {"host": "example.com"}}   // connect + list agents
```

Each agent in the reply says whether it is `[conversational]` or `[stateless]`:

- **conversational** — `conversations` / `new_conversation` /
  `resume_conversation` first, then `send_message`; the daemon requires a session.
- **stateless** — `send_message` directly; there is no session to hold.

A name that is not in the manifest is rejected before anything is sent. An agent
whose backend is down surfaces the daemon's error. `manifest` returns the document
verbatim with its digest check, and is the authoritative answer on what a business
exposes.

---

## 3. Architecture

Four things, and keeping them apart is the point:

| Layer                     | What it is                                                                                   | Where |
| ------------------------- | -------------------------------------------------------------------------------------------- | ----- |
| **Agent Front Door**      | The open specification: the problem, the manifest, the conformance ladder.                    | [agent-front-door-spec](https://github.com/ground-creative/agent-front-door-spec) |
| **Portico (`portico/1`)** | The wire protocol: framing, handshake, manifest, sessions, messages, progress, cancellation.  | [`normative/PROTOCOL.md`](https://github.com/ground-creative/agent-front-door-spec/blob/main/normative/PROTOCOL.md) |
| **Vireo**                 | The Go reference daemon that serves a domain's endpoint.                                      | [ground-creative/vireo](https://github.com/ground-creative/vireo) |
| **vireo-mcp** (this repo) | The adapter between an MCP host and a Portico endpoint.                                       | here |

```text
        AI host  (Claude Desktop · Cursor · VS Code · any MCP host)
           │
           │ MCP — stdio (or Streamable HTTP)
           ▼
      ┌───────────┐
      │ vireo-mcp │   AI-host side · this repository
      └─────┬─────┘
            │ portico/1 over TLS 1.3 (TCP); endpoint found by DNS SRV
            ▼
      ┌───────────┐
      │   vireo   │   business side · separate repository
      │  daemon   │   handshake → manifest → session → message
      └─────┬─────┘
            │ HTTP POST (JSON; optional NDJSON progress)
            ▼
      ┌───────────┐
      │  backend  │   the business's model, logic and data
      └───────────┘
```

**MCP is the integration point for the AI host. Portico is the protocol between the
bridge and the remote agents.** The host never speaks `portico/1`; the daemon never
speaks MCP. The bridge is the only place both exist, and it is replaceable —
`vireo-chat` in the vireo repository reaches the same daemons with no MCP anywhere
in the path.

What follows from the split:

- **Discovery is DNS.** `_portico._tcp.<domain>` names host and port. There is no
  application-level directory, no marketplace, no central registry.
- **The manifest is the contract.** It arrives inside the TLS connection; the
  bridge verifies its digest and surfaces it verbatim instead of paraphrasing it.
- **Sessions live on the daemon.** The bridge holds a connection and a current
  conversation per agent, and nothing durable. Kill the bridge and the
  conversations are still there under the same client id.
- **The daemon holds no model and no business logic.** It routes each turn to the
  business's webhook and returns the answer.

---

## 4. Backend integration: an existing agent runtime

This is worth stating precisely, because it is the easiest thing to get backwards.

`vireo-mcp` is **not** an OpenClaw adapter. It never sees OpenClaw, and OpenClaw
never sees MCP. The OpenClaw integration sits on the *other* side of the daemon, as
a **backend adapter** — an HTTP server that answers vireo's webhook by calling an
OpenClaw gateway:

```text
AI host ──MCP──▶ vireo-mcp ──portico/1──▶ vireo ──HTTP webhook──▶ openclaw-bridge ──▶ OpenClaw gateway ──▶ your agent
```

The point of the example is that **an agent you already run becomes reachable over
`portico/1` without modifying the agent runtime**. The bridge translates vireo's
webhook into the gateway's Chat Completions endpoint and back, so your agent runs
exactly as it does today.

- [`vireo/bridges/openclaw-bridge`](https://github.com/ground-creative/vireo/tree/main/bridges/openclaw-bridge)
  — setup, configuration and isolation notes
- [`vireo/docs/BACKENDS.md`](https://github.com/ground-creative/vireo/blob/main/docs/BACKENDS.md)
  — the webhook contract, and how to write one for any runtime

Once that bridge runs behind a daemon, nothing here changes: the host connects to
the daemon's domain, and the OpenClaw agent is just an agent in the manifest.
Swapping the backend is invisible to `vireo-mcp`.

---

## Install

### From a release (no Go required)

Every tagged release publishes five archives and a checksum file:

| Platform             | Archive                         |
| -------------------- | ------------------------------- |
| macOS, Apple Silicon | `vireo-mcp-darwin-arm64.tar.gz` |
| macOS, Intel         | `vireo-mcp-darwin-amd64.tar.gz` |
| Windows, x64         | `vireo-mcp-windows-amd64.zip`   |
| Linux, x64           | `vireo-mcp-linux-amd64.tar.gz`  |
| Linux, ARM64         | `vireo-mcp-linux-arm64.tar.gz`  |
| —                    | `checksums.txt` (SHA-256)       |

Download from <https://github.com/ground-creative/vireo-mcp/releases>. Verify before
trusting the file:

```bash
sha256sum -c checksums.txt --ignore-missing   # macOS: shasum -a 256 -c checksums.txt
```

**macOS and Linux** — extract, keep the executable bit, put it somewhere on your
`PATH`. The archive already carries mode `0755`, so `chmod` is only a fallback:

```bash
tar -xzf vireo-mcp-darwin-arm64.tar.gz      # the archive for your machine
chmod +x vireo-mcp                          # only if the bit was lost in transit
mkdir -p ~/.local/bin && mv vireo-mcp ~/.local/bin/
export PATH="$PATH:$HOME/.local/bin"        # add to ~/.zshrc or ~/.bashrc to keep it
vireo-mcp -version
```

Any directory you own works. What matters is that the **absolute path** goes into
the host's config: a GUI host does not read your shell profile.

**Windows** — extract the `.zip` and note the full path to the executable:

```powershell
Expand-Archive vireo-mcp-windows-amd64.zip -DestinationPath $HOME\bin
$HOME\bin\vireo-mcp.exe -version
```

Point the host at it, doubling the backslashes because JSON escapes them:

```json
{
  "mcpServers": {
    "vireo": {
      "command": "C:\\Users\\you\\bin\\vireo-mcp.exe"
    }
  }
}
```

**Unsigned binaries.** These releases are not code-signed or notarized — there is
no Developer ID certificate and no Authenticode signature behind them, so both
platforms warn:

- **macOS Gatekeeper** reports *"vireo-mcp" cannot be opened because the developer
  cannot be verified*. Clear it for this one binary with
  `xattr -d com.apple.quarantine ./vireo-mcp`, or allow it under **System Settings
  → Privacy & Security**. Do that only for a binary whose checksum you verified.
- **Windows SmartScreen** shows *"Windows protected your PC"*. **More info → Run
  anyway**, again only after checking the checksum.

If you would rather not accept an unnotarized binary, build from source — the next
section takes about ten seconds and needs nothing but Go.

### With Go

```bash
go install github.com/ground-creative/vireo-mcp@latest
```

The binary lands in `$(go env GOPATH)/bin` (`~/go/bin` on most machines). Add that
to `PATH`, or use the absolute path in the host config:

```bash
export PATH="$PATH:$(go env GOPATH)/bin"
vireo-mcp -version
```

### From source

```bash
git clone https://github.com/ground-creative/vireo-mcp.git
cd vireo-mcp
go build -o vireo-mcp .          # or: make build
./vireo-mcp -version
```

To produce the same archives a release does — all five targets, with checksums —
run the packaging script. It needs no arguments and no tag:

```bash
./scripts/release.sh dev dist    # → dist/ + dist/checksums.txt
```

Only the bridge is installed per AI host. The daemon side (`vireo`) is a separate
deployment: it needs `config/`, `certs/` and `sessions/` beside it, so it is built
in its own checkout.

---

## MCP host configuration

Three hosts are covered; the bridge speaks standard MCP over stdio (one JSON-RPC
message per line), so any conforming host works the same way.

### Claude Desktop

`~/Library/Application Support/Claude/claude_desktop_config.json` (macOS),
`%APPDATA%\Claude\claude_desktop_config.json` (Windows):

```json
{
  "mcpServers": {
    "vireo": {
      "command": "/home/you/.local/bin/vireo-mcp",
      "args": []
    }
  }
}
```

### Cursor

`.cursor/mcp.json` in a project, or `~/.cursor/mcp.json` for all projects:

```json
{
  "mcpServers": {
    "vireo": {
      "command": "/home/you/.local/bin/vireo-mcp",
      "args": []
    }
  }
}
```

### VS Code

`.vscode/mcp.json` in the workspace — `servers` rather than `mcpServers`, and an
explicit `"type": "stdio"`:

```json
{
  "servers": {
    "vireo": {
      "type": "stdio",
      "command": "/home/you/.local/bin/vireo-mcp",
      "args": []
    }
  }
}
```

### Why the absolute path

`"command": "vireo-mcp"` only works when the host inherits a `PATH` containing the
binary. Desktop hosts usually do not: Claude Desktop and anything launched from a
dock or an app bundle starts with a minimal `PATH` such as
`/usr/bin:/bin:/usr/sbin:/sbin`. Your terminal finds the binary; the host does not,
and reports `spawn vireo-mcp ENOENT` or `MCP server "vireo" failed to start`.

Adding the directory to `~/.zshrc` does not help either — a GUI host never reads it.
The absolute path always works:

```bash
command -v vireo-mcp        # exactly this path goes in the config
```

On Windows that is `C:\\Users\\you\\bin\\vireo-mcp.exe`.

### Common flags in `args`

| Need                                 | `args`                              |
| ------------------------------------ | ----------------------------------- |
| local daemon with a dev certificate  | `["-ca", "/abs/path/to/certs/cert.pem"]` |
| a separate identity per host app     | `["-name", "claude-desktop"]`       |
| longer timeout for a slow backend    | `["-timeout", "300s"]`              |
| local daemon, disposable test only   | `["-insecure"]`                     |
| trace every message for debugging    | `["-log", "/tmp/vireo-mcp.jsonl"]`  |

`-version`, `-tools` and `-routes` are one-shot diagnostics and do not belong in a
host config.

### HTTP transport

For a remote host, a container, or one bridge shared by several assistants:

```bash
vireo-mcp -http-addr 127.0.0.1:8091
```

It prints the bearer token and the client id that token is bound to. Endpoints:

```
POST /mcp     one JSON-RPC message per request; Authorization: Bearer <token>
GET  /mcp     text/event-stream liveness channel; same token
GET  /health  {"ok":true,"server":"vireo-mcp",…}
GET  /routes  the route table as JSON
GET  /        a live HTML view of routes and recent traffic
```

Then point the host at the URL:

```json
{
  "servers": {
    "vireo": {
      "type": "http",
      "url": "http://127.0.0.1:8091/mcp",
      "headers": { "Authorization": "Bearer vmcp_…" }
    }
  }
}
```

Without a token, or with an unknown one:

```
HTTP/1.1 401 Unauthorized
WWW-Authenticate: Bearer realm="vireo-mcp"

{"jsonrpc":"2.0","id":null,"error":{"code":-32001,"message":"missing authorization — send: Authorization: Bearer <token>"}}
```

A bare token with no `Bearer ` prefix is accepted, which makes curl easier. A
different token is a different identity, so each assistant and each person should
have their own.

---

## Tools

Nine tools; `vireo-mcp -tools` prints the catalogue verbatim.

| Tool                  | What it does                                                                    |
| --------------------- | ------------------------------------------------------------------------------- |
| `discover`            | probe a domain without switching the current connection                         |
| `connect`             | connect to an endpoint and list the agents behind it                            |
| `conversations`       | list your previous conversations with one agent (dates, summaries, turn counts) |
| `new_conversation`    | start a new conversation with an agent                                          |
| `resume_conversation` | resume a previous one, by number from that list or the latest                   |
| `send_message`        | send a message to the current agent and get its reply                           |
| `close_conversation`  | close the current conversation; it becomes history                              |
| `manifest`            | the endpoint's manifest document verbatim, with its digest checked              |
| `about`               | version, build, caller identity and current connection                          |

The flow:

```
discover? → connect {host} → choose an agent → conversations / new_conversation
         → send_message … → resume_conversation later → close_conversation
```

A tool failure comes back as `isError: true` with a message meant to be acted on
(`not connected — run connect first`, `unknown agent "x" — see the connect list`),
so a host can recover without the user reading a stack trace.

---

## Identity

The daemon scopes conversations by **client id**, which must be the same every
time or your conversations disappear.

**Over stdio** the id is generated once (128 bits from `crypto/rand`) and stored at
`$XDG_CONFIG_HOME/vireo-mcp/<label>.id` — `~/.config/vireo-mcp/default.id` on
Linux. Delete the file to start as somebody else. `-name` selects which local
identity to use, so Claude Desktop and Cursor can be different callers:

```json
{"args": ["-name", "claude-desktop"]}
```

**Over HTTP** there is no local file to rely on: the token *is* the identity. It is
bound to a server-generated client id printed at startup. Mint more with:

```bash
vireo-mcp -issue-token      # token, client id, and the header to send
vireo-mcp -list-tokens      # what is registered, with client ids
```

Tokens live in `$XDG_CONFIG_HOME/vireo-mcp/<label>.callers.json` and survive a
restart. `VIREO_MCP_ID` overrides the client id entirely, for tests and odd
deployments.

---

## Progress, cancellation and transport differences

**Progress.** A backend that streams while it works produces MCP
`notifications/progress` over stdio, provided the host sent a
`params._meta.progressToken`. A silent backend and a host that sent no token
behave identically: the call blocks, then returns. A quiet turn also emits
`still working…` every ten seconds, so hosts that time out idle calls do not
abandon it.

**Cancellation.** A host `notifications/cancelled` stops the bridge waiting and
sends a `cancel` frame to the daemon, which cancels its upstream request. The tool
call is answered with code `-32800`. It is cooperative and best effort, **not a
rollback**: the message already reached the daemon, so a backend that committed
work has committed it. `tools/call` runs in its own goroutine precisely so the
cancel can be read while the turn is still in flight.

**Over HTTP** one POST yields one response — no stream to notify on, so progress is
silent and cancellation is best effort per caller.

**A written request is never retried.** Only a request that never reached the wire
is retried, so a daemon restart looks like a slightly slower call rather than a
duplicated message.

---

## Troubleshooting

Each entry starts from what you actually see.

### The host cannot start the binary

```
spawn vireo-mcp ENOENT
MCP server "vireo" failed to start
command not found
```

The host cannot see the executable. Put the absolute path from
`command -v vireo-mcp` in the config ([why](#why-the-absolute-path)), and confirm
the binary is sound first:

```bash
/abs/path/to/vireo-mcp -version
```

If that fails, the download is wrong for the platform — check
`file /abs/path/to/vireo-mcp` (`ELF 64-bit … x86-64`, `ELF 64-bit … ARM aarch64`,
`Mach-O 64-bit arm64`, or a Windows `PE32+`) — or macOS has quarantined it.

### Invalid MCP configuration

The host reports the server as failed with no useful message, or it starts and no
tools appear:

- Malformed JSON — a trailing comma, or a Windows path with single backslashes
  (`C:\Users\…` is invalid; `C:\\Users\\…` is correct).
- VS Code needs `"servers"`, not `"mcpServers"`, plus `"type": "stdio"`.
- The same bridge registered under two `-name` labels is two identities and two
  conversation histories. That is by design, but it looks like lost history.
- Ask the host to call `about`. It reports version, build and identity, which
  settles "is the config pointing at the binary I think it is?".

### MCP initialization failures

The handshake is plain JSON-RPC on stdio, so it can be checked without the host:

```bash
printf '%s\n' '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"t","version":"1"}}}' \
  | /abs/path/to/vireo-mcp
```

Expect `"serverInfo":{"name":"vireo-mcp","version":"…"}`. If nothing appears,
something is writing to stdout that is not JSON-RPC — stdout carries the MCP
stream, and every diagnostic belongs on stderr. `-log -` moves the trace to stderr
for exactly this reason.

If the handshake works in the shell but not from the host, the host is starting a
different binary (above), or `-name` / `VIREO_MCP_ID` has changed the identity so
that the tools you expect are not there.

### DNS discovery errors

```
discovery failed: no SRV record _portico._tcp.example.com (or retired _vireo._tcp) — connect with an explicit host:port instead
```

The domain publishes no record the bridge knows. Check from the same machine:

```bash
dig +short SRV _portico._tcp.example.com
dig +short SRV _vireo._tcp.example.com
```

An empty answer means either "this domain does not run an endpoint" or "the record
is not published yet". If the endpoint is up but not in DNS, connect with
`host:port` — the bridge does not guess a port:

```json
{"name": "connect", "arguments": {"host": "agent.example.com:7777"}}
```

### Endpoint connection failures

```
connect to agent.example.com:7777 failed: dial tcp …: connect: connection refused
```

The address resolved but nothing is listening. Check the port in the daemon's
config, that the daemon is running, and that TCP **and** UDP are open if QUIC is in
use. For a local daemon, `./scripts/dev.sh` in the vireo checkout starts backend,
daemon and client together.

```
connect to … failed: … certificate verification failed for "…"; a development
certificate is not trusted by the system. Pass -ca <certs/cert.pem> to trust it
(verification stays on), or -insecure to skip verification, which is unsafe and
only for local development
```

A local daemon with a `gen-certs.sh` certificate. Use `-ca`, not `-insecure`, if
the setting will outlive the test. When `-ca` is already set the message changes:
the certificate must be **signed by a CA in that file and cover that name** — so
either the bundle is stale (regenerate it, restart the bridge) or the certificate
does not include the hostname you connected with.

A timeout rather than a refusal is usually a firewall, a private endpoint, or a
wrong SRV target.

### Authentication failures

```
missing authorization — send: Authorization: Bearer <token>
unknown token — mint one with: vireo-mcp -issue-token
```

These come from the bridge's own HTTP transport and mean exactly what they say.
Two traps: a token minted for one `-name` label lives in a different registry from
another label's, and an HTTP-served bridge requires the header on `GET /mcp` too.
Over stdio there is no token and this error cannot occur — stdio identity is the
local id file.

An **endpoint** rejecting the caller is a different case. `portico/1` as
implemented carries no API key, so `bad_client_id` means the *client id* was
refused (empty, or longer than the daemon accepts). The bridge always mints a valid
one, so seeing this means `VIREO_MCP_ID` was set to something the daemon will not
take.

### Unknown agent identifiers

```
unknown agent "orders" — see the connect list
```

The name is not in the manifest. Agent names are case-sensitive and come from the
daemon's config, not from a URL or a guess. Run `connect` (or `manifest`) and use a
name exactly as listed.

```
orders holds conversations — start one with new_conversation or resume_conversation first
```

The agent is conversational (`"sessions": true`) and this message had no session.
Passing an `agent` override to `send_message` does not open a conversation for you.

### Conversation creation failures

```
not connected — run connect first
```

Tool state is per connection. Reconnecting deliberately resets the current agent
and the per-agent conversation pointers; the conversations themselves live on the
daemon and are not lost.

An `upstream_error` while starting a conversation is the daemon reporting that the
backend refused, and the message is the backend's own. `rate_limited` means the
advertised limit was exceeded — the manifest states the numbers.

### Conversation resumption failures

```
that conversation is no longer known (the daemon may have restarted) — run conversations, then resume or start new
```

The daemon's session store no longer holds the id — typically a restart with an
in-memory store, or TTL expiry. The manifest advertises `ttl_seconds`.

```
That conversation is closed — history only: "…" (N turns). Start a new one with new_conversation(orders).
```

Closed sessions stay visible and cannot be resumed.

A conversation that disappears from `conversations` after a config change almost
always means the client id changed: a different `-name` label, a deleted id file,
`VIREO_MCP_ID` set, or a different bearer token over HTTP.

---

## Contributing

Three kinds of contribution, in ascending order of effort. None of them require
implementing the protocol.

### 1. Test the bridge against an independent Portico endpoint

The cheapest and most valuable. If you run a daemon — this one or your own — point
`vireo-mcp` at it and report what broke. A daemon in another language, or on an
unusual network, is the interesting case.

- daemon: [ground-creative/vireo](https://github.com/ground-creative/vireo)
- the ladder to aim for: [`normative/CONFORMANCE.md`](https://github.com/ground-creative/agent-front-door-spec/blob/main/normative/CONFORMANCE.md)

### 2. Build an integration or an independent client

The bridge is one adapter. A client that talks `portico/1` without reusing this code
is stronger evidence that the protocol is implementable from the text — any
language, any runtime. Start from
[`normative/PROTOCOL.md`](https://github.com/ground-creative/agent-front-door-spec/blob/main/normative/PROTOCOL.md)
and [`normative/MANIFEST.md`](https://github.com/ground-creative/agent-front-door-spec/blob/main/normative/MANIFEST.md),
and check your work against the live endpoint with the probe that shares no code
with vireo (clone
[agent-front-door-spec](https://github.com/ground-creative/agent-front-door-spec)
first — `machine/` lives there):

```bash
python3 machine/conformance.py --endpoint vireo.ziwa.ai:7778 --server-name ziwa.ai
```

Claim a conformance level in that repository's
[`IMPLEMENTATIONS.md`](https://github.com/ground-creative/agent-front-door-spec/blob/main/IMPLEMENTATIONS.md)
as a pull request with a row: project, language, role, level, spec release, and how
a third party reproduces the check.

### 3. Improve documentation, tests and interoperability

For this repository specifically:

```bash
gofmt -l .                              # must print nothing
go vet ./...
go test -count=1 -race ./...
go build ./...
./scripts/release.sh dev dist           # all five targets + checksums
```

Tests live beside the code: `transport_test.go`, `callers_test.go`,
`progress_test.go`, `cancel_test.go`, and `internal/*/*_test.go`. A test that
encodes a real host behaviour — a malformed config, a cancelled call, a resumed
session — is worth more than one that restates an implementation detail.

Issues about unclear documentation are defects, not noise. The specification
repository explains what makes a useful report
([`CONTRIBUTING.md`](https://github.com/ground-creative/agent-front-door-spec/blob/main/CONTRIBUTING.md))
and how decisions are made
([`GOVERNANCE.md`](https://github.com/ground-creative/agent-front-door-spec/blob/main/GOVERNANCE.md)).

---

## Reference

### Flags

| Flag           | Default   | Meaning                                                                 |
| -------------- | --------- | ----------------------------------------------------------------------- |
| `-name`        | `default` | label for this installation — selects the local identity (display only) |
| `-http-addr`   | —         | serve MCP over Streamable HTTP on this address instead of stdio          |
| `-ca`          | —         | PEM bundle to trust in addition to the system roots (verification stays on) |
| `-insecure`    | `false`   | skip daemon TLS verification — unsafe, local development only            |
| `-timeout`     | `120s`    | per-request timeout toward the daemon                                    |
| `-issue-token` | —         | mint an authorization token for the HTTP transport and print it          |
| `-list-tokens` | —         | print the registered tokens and their client ids                         |
| `-log`         | —         | write a JSON-RPC trace to this file, or `-` for stderr                   |
| `-tools`       | —         | print the tool catalogue as JSON and exit                                |
| `-routes`      | —         | print the transport/route table and exit                                 |
| `-version`     | —         | print name, version, commit and build time, then exit                    |

Environment: `VIREO_MCP_ID` overrides the client id; `SSL_CERT_FILE` adds a root
bundle (it replaces the system roots — prefer `-ca`).

### Daemon error codes the bridge translates

From `portico/1` as implemented in the vireo daemon:

| Code               | Means                                                        |
| ------------------ | ------------------------------------------------------------ |
| `session_required` | a conversational agent was messaged without a session        |
| `unknown_session`  | the session id is unknown — restarted daemon, or wrong client |
| `agent_not_found`  | the agent name is not in the manifest                        |
| `bad_client_id`    | the daemon refused the client id                             |
| `upstream_error`   | the backend behind the daemon failed                         |
| `invalid_frame`    | a protocol violation on the wire                             |
| `rate_limited`     | a per-client or per-agent limit was exceeded                 |
| `bad_protocol`     | no acceptable protocol version                               |

### Driving it by hand

```bash
vireo-mcp -tools             # the catalogue, exactly as tools/list serves it
vireo-mcp -routes            # every transport and message this process answers
vireo-mcp -log -             # trace JSON-RPC to stderr
curl -s localhost:8091/health
```

### Known limitations

Stated plainly, because they are limits of this implementation today, not of the
idea:

- **No client credential toward a daemon.** If an endpoint requires one, the bridge
  cannot present it; identity is the generated client id.
- **HTTP transport has no per-call progress or cancellation stream.** One POST, one
  response.
- **Cancellation is not a rollback.** It stops the wait and cancels the upstream
  request; committed work stays committed.
- **Signed releases are not implemented.** Archives are neither notarized nor
  Authenticode-signed, so both platforms warn (see [Install](#install)).
- **Windows ARM64 is not built.** It is a one-line addition to
  `scripts/release.sh` when there is a reason to add it.
- **Only linux/amd64 is executed in CI.** The other targets are compile-verified
  and file-type checked; a green workflow does not claim they ran on their target
  OS.

### Repository layout

```
main.go                        MCP server: stdio + Streamable HTTP, tools, progress, cancellation
callers.go                     token → client id registry for the HTTP transport
internal/dialer/               tool engine: connect, conversations, send, resume; tool schemas
internal/client/               portico/1 client: SRV discovery, TLS, handshake, messages, manifest
internal/frame/                9-byte frame header codec
internal/protocol/             frame types, error codes, JSON envelopes
scripts/release.sh             cross-platform build, packaging and checksums
.github/workflows/release.yml  tag-triggered build, test, verification and publication
```

The protocol itself is specified in the vireo repository and in the specification,
not here:
[`docs/PROTOCOL.md`](https://github.com/ground-creative/vireo/blob/main/docs/PROTOCOL.md)
and [`normative/PROTOCOL.md`](https://github.com/ground-creative/agent-front-door-spec/blob/main/normative/PROTOCOL.md).
