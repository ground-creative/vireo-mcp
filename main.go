// Command vireo-mcp is an MCP server that gives any MCP host — Claude Desktop,
// Cursor, VS Code — access to the whole vireo network. Installed once, its
// tools let the agent discover and connect to any daemon at runtime: connect
// lists the things a business offers, conversations show dated history with
// summaries, and send_message talks.
//
// Host configuration (Claude Desktop / VS Code / Cursor):
//
//	{
//	  "mcpServers": {
//	    "vireo": { "command": "/path/to/vireo-mcp" }
//	  }
//	}
//
// Over stdio this process is one client instance: its stable client id is
// minted once and stored under the user config directory, so conversations
// resume across restarts.
//
// A tool call is dispatched in its own goroutine so the transport keeps
// reading: when the host cancels, the bridge has to see it while the turn is
// still running. Cancelling stops the wait and asks the daemon to abandon the
// call — it is not a rollback, because the message already left.
//
// Over HTTP (-http-addr) the caller must present an authorization token:
//
//	Authorization: Bearer vmcp_…
//
// The token is bound to a client id the server generates once and keeps, so
// each caller has its own conversations.
package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"html"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"text/tabwriter"
	"time"

	"github.com/ground-creative/vireo-mcp/internal/dialer"
)

const serverName = "vireo-mcp"
const defaultProtocolVersion = "2024-11-05"

// codeCancelled is the MCP error code for a request the receiver abandoned.
const codeCancelled = -32800

// Version metadata. version has a default so `go install` and a plain
// `go build` report something sensible; a release build overrides all three
// with -ldflags and no other line in the program changes:
//
//	go build -ldflags "-X main.version=0.9.0 \
//	  -X main.buildCommit=$(git rev-parse --short HEAD) \
//	  -X main.buildTime=$(date -u +%Y-%m-%dT%H:%M:%SZ)"
//
// Only the Go toolchain injects these — no generated file, no extra dependency,
// and a build with no flags still works.
var (
	version     = "0.8.0" // release builds override this
	buildCommit = "dev"
	buildTime   = "unknown"
)

func main() {
	label := flag.String("name", "default", "label for this installation, e.g. claude-desktop (display only; the id is generated)")
	insecure := flag.Bool("insecure", false, "skip daemon TLS verification (UNSAFE; local development only)")
	caFile := flag.String("ca", "", "PEM bundle to trust for this daemon, e.g. certs/cert.pem (verification stays on)")
	timeout := flag.Duration("timeout", 120*time.Second, "per-request timeout toward the daemon")
	httpAddr := flag.String("http-addr", "", "serve MCP over Streamable HTTP on this address instead of stdio, e.g. 127.0.0.1:8081")
	issueToken := flag.Bool("issue-token", false, "mint a new authorization token for the HTTP transport and print it")
	listTokens := flag.Bool("list-tokens", false, "print the registered authorization tokens and their client ids")
	trace := flag.String("log", "", "write a JSON-RPC trace to this file, or - for stderr")
	showTools := flag.Bool("tools", false, "print the tool catalog as JSON and exit")
	showRoutes := flag.Bool("routes", false, "print the transport/route table and exit")
	showVersion := flag.Bool("version", false, "print name, version and build, then exit")
	flag.Parse()

	// -version is the first thing a bug report needs, so it prints the build
	// identity on one line: version, commit, build time. The first two fields
	// stay machine-readable for scripts (`vireo-mcp -version | cut -d' ' -f2`).
	if *showVersion {
		fmt.Printf("%s %s (commit %s, built %s)\n", serverName, version, buildCommit, buildTime)
		return
	}
	if *showRoutes {
		printRoutes()
		return
	}

	// Verification is on by default: with it off the traffic is still encrypted,
	// but nothing binds the connection to the business being contacted (SEC-6).
	// The warning goes to stderr: stdout carries the MCP stream over stdio.
	if *insecure {
		fmt.Fprintf(os.Stderr, "%s: warning: -insecure disables certificate verification; the endpoint's identity is not established\n", serverName)
	}

	clientID, err := dialer.LoadOrCreateID(*label)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s: client id: %v\n", serverName, err)
		os.Exit(1)
	}

	registry, err := openCallers(callersPath(*label))
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s: token registry: %v\n", serverName, err)
		os.Exit(1)
	}

	if *issueToken {
		entry, err := registry.Issue(*label)
		if err != nil {
			fmt.Fprintf(os.Stderr, "%s: %v\n", serverName, err)
			os.Exit(1)
		}
		fmt.Printf("token:      %s\n", entry.Token)
		fmt.Printf("client id:  %s\n", entry.ClientID)
		fmt.Printf("header:     Authorization: Bearer %s\n", entry.Token)
		return
	}
	if *listTokens {
		for _, e := range registry.List() {
			fmt.Printf("%s  %s  %s\n", e.Token, e.ClientID, e.Label)
		}
		return
	}

	s := &server{
		baseID:    clientID,
		name:      *label,
		instances: map[string]*instance{},
		callers:   registry,
		inflight:  map[string]func(){},
	}
	s.newDialer = func(id string) *dialer.Dialer {
		return dialer.New(dialer.Options{
			ClientID: id, Name: *label, Insecure: *insecure, CAFile: *caFile, Timeout: *timeout,
			// Mid-turn progress becomes notifications/progress, so a host can
			// show "hold on" while a tool call is still running.
			OnPartial: s.notifyProgress,
		})
	}
	s.d = s.newDialer(clientID)

	if *showTools {
		// The full catalogue, exactly as tools/list serves it — including the
		// server's own `about` tool, which is not part of the dialer's set.
		b, err := json.MarshalIndent(s.tools(s.d), "", "  ")
		if err != nil {
			fmt.Fprintf(os.Stderr, "%s: %v\n", serverName, err)
			os.Exit(1)
		}
		fmt.Println(string(b))
		return
	}

	if *trace != "" {
		if *trace == "-" {
			s.trace = os.Stderr
		} else {
			f, err := os.OpenFile(*trace, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
			if err != nil {
				fmt.Fprintf(os.Stderr, "%s: trace file: %v\n", serverName, err)
				os.Exit(1)
			}
			defer f.Close()
			s.trace = f
		}
	}

	fmt.Fprintf(os.Stderr, "%s %s — MCP client instance %q\n", serverName, version, clientID)

	if *httpAddr != "" {
		entry, err := registry.Ensure(*label)
		if err != nil {
			fmt.Fprintf(os.Stderr, "%s: token registry: %v\n", serverName, err)
			os.Exit(1)
		}
		fmt.Fprintf(os.Stderr, "%s: MCP over Streamable HTTP at http://%s/mcp\n", serverName, *httpAddr)
		fmt.Fprintf(os.Stderr, "%s: authorization token (send it as a bearer token on every request):\n", serverName)
		fmt.Fprintf(os.Stderr, "  Authorization: Bearer %s\n", entry.Token)
		fmt.Fprintf(os.Stderr, "%s: client id for that token: %s\n", serverName, entry.ClientID)
		if err := s.serveHTTP(*httpAddr); err != nil {
			fmt.Fprintf(os.Stderr, "%s: %v\n", serverName, err)
			os.Exit(1)
		}
		return
	}

	fmt.Fprintf(os.Stderr, "%s: MCP over stdio — JSON-RPC lines on stdin, replies on stdout\n", serverName)
	// Only stdio can carry notifications while a tool call is still running.
	s.progressOut = os.Stdout
	s.serveStdio()
}

// server is one MCP server process. Over stdio it is exactly one client
// instance; over HTTP each MCP session gets its own instance so that parallel
// callers do not share conversations.
type server struct {
	baseID string
	name   string
	// newDialer builds the engine for one caller's client id.
	newDialer func(id string) *dialer.Dialer
	// d is the stdio instance: one process, one identity, no token needed.
	d *dialer.Dialer

	// callers is the durable token → client id registry. It is the only thing
	// this process stores; conversations live in the daemon.
	callers *callers

	instMu    sync.Mutex
	instances map[string]*instance
	lastSweep time.Time

	trace   io.Writer
	traceMu sync.Mutex
	events  []event

	// progressOut is where notifications/progress go: stdout over stdio, nil
	// over HTTP, where one POST has no stream to notify on.
	progressOut   io.Writer
	progressMu    sync.Mutex
	progressToken string
	progressSeq   int

	// outMu serialises stdout: a tool call answers from its own goroutine and
	// progress notifications can write at any time.
	outMu sync.Mutex

	// inflight maps a JSON-RPC request id to the cancel action for that call,
	// so notifications/cancelled can reach a call that is still running.
	inflightMu sync.Mutex
	inflight   map[string]func()
}

// instance is the live state for one caller: a connection and its current
// conversation per agent. Disposable — the token registry is what persists.
type instance struct {
	d        *dialer.Dialer
	lastUsed time.Time
}

const (
	// maxClientID mirrors the daemon's limit on the client id it accepts.
	maxClientID = 128
	// instanceIdleTTL drops in-memory instances nobody has used for a while.
	instanceIdleTTL = 30 * time.Minute
	// instanceSweepEvery bounds how often the map is scanned for idle entries.
	instanceSweepEvery = 5 * time.Minute
)

// instanceFor returns the live state for a token, creating it on first use.
// The token must already be in the registry.
func (s *server) instanceFor(token string) *dialer.Dialer {
	s.instMu.Lock()
	defer s.instMu.Unlock()

	now := time.Now()
	if inst, ok := s.instances[token]; ok {
		inst.lastUsed = now
		return inst.d
	}

	if now.Sub(s.lastSweep) > instanceSweepEvery {
		for t, inst := range s.instances {
			if now.Sub(inst.lastUsed) > instanceIdleTTL {
				delete(s.instances, t)
			}
		}
		s.lastSweep = now
	}

	entry, ok := s.callers.Lookup(token)
	if !ok {
		return nil
	}
	d := s.newDialer(entry.ClientID)
	s.instances[token] = &instance{d: d, lastUsed: now}
	return d
}

// callerCount is the number of live instances, for the health endpoint.
func (s *server) callerCount() (registered, live int) {
	s.instMu.Lock()
	live = len(s.instances)
	s.instMu.Unlock()
	return s.callers.Len(), live
}

const maxEvents = 200

// event is one line of observed traffic, kept for the human dashboard.
type event struct {
	At   time.Time
	Dir  string
	Text string
}

func (s *server) logf(format string, args ...any) {
	if s.trace == nil {
		return
	}
	s.traceMu.Lock()
	defer s.traceMu.Unlock()
	fmt.Fprintf(s.trace, format+"\n", args...)
}

// record mirrors traffic to the trace writer and the in-memory ring.
func (s *server) record(dir, text string) {
	s.logf("%s %s", dir, text)

	s.traceMu.Lock()
	defer s.traceMu.Unlock()
	s.events = append(s.events, event{At: time.Now(), Dir: dir, Text: text})
	if len(s.events) > maxEvents {
		s.events = s.events[len(s.events)-maxEvents:]
	}
}

// serveStdio runs the transport every MCP host uses by default: one JSON-RPC
// message per line in, one out. Tool calls are dispatched in their own
// goroutine, because a turn can take minutes and the loop has to keep reading —
// otherwise the host's cancel notification would sit in the pipe unread.
// Everything else is answered inline, in order.
func (s *server) serveStdio() {
	in := bufio.NewScanner(os.Stdin)
	in.Buffer(make([]byte, 1<<20), 1<<20)

	for in.Scan() {
		line := bytes.TrimSpace(in.Bytes())
		if len(line) == 0 {
			continue
		}
		msg := append([]byte(nil), line...)
		if isToolCall(msg) {
			go func(b []byte) {
				if resp := s.handleLine(s.d, b); resp != "" {
					s.writeLine(resp)
				}
			}(msg)
			continue
		}
		if resp := s.handleLine(s.d, line); resp != "" {
			s.writeLine(resp)
		}
	}
	if err := in.Err(); err != nil {
		fmt.Fprintf(os.Stderr, "%s: stdin: %v\n", serverName, err)
	}
}

// isToolCall reports whether a line is a tools/call request, the only message
// that can block for the length of a turn.
func isToolCall(line []byte) bool {
	var probe struct {
		Method string `json:"method"`
	}
	return json.Unmarshal(line, &probe) == nil && probe.Method == "tools/call"
}

// writeLine emits one JSON-RPC message, serialised against progress lines.
func (s *server) writeLine(text string) {
	s.outMu.Lock()
	defer s.outMu.Unlock()
	fmt.Fprintln(os.Stdout, text)
}

// route is one entry in the human-facing route table. The MCP spec has no
// concept of routes; this exists so a person can see what the process answers.
type route struct {
	Transport string `json:"transport"`
	Method    string `json:"method"`
	Path      string `json:"path"`
	Handler   string `json:"handler"`
	Notes     string `json:"notes"`
}

func routes() []route {
	return []route{
		{"http", "GET", "/", "handleDashboard", "live HTML view: routes + last 200 messages"},
		{"http", "GET", "/routes", "handleRoutesJSON", "this table as JSON"},
		{"http", "GET", "/health", "handleHealth", `{"ok":true,"server":"vireo-mcp",...}`},
		{"http", "POST", "/mcp", "handleHTTP", "one JSON-RPC message per request; Authorization: Bearer <token> required"},
		{"http", "GET", "/mcp", "handleHTTP", "text/event-stream liveness channel; same token required"},
		{"http", "*", "/mcp", "tokenFrom", "identity from the Authorization header; 401 without a known token"},

		{"stdio", "line", "stdin → stdout", "handleLine", "one JSON-RPC message per line, no framing, no HTTP"},
		{"mcp", "initialize", "-", "dispatch", "protocolVersion, capabilities.tools, serverInfo"},
		{"mcp", "notifications/initialized", "-", "dispatch", "notification — no reply"},
		{"mcp", "ping", "-", "dispatch", "empty result"},
		{"mcp", "tools/list", "-", "dispatch", "the 9 tools with JSON Schemas (8 from the dialer + about)"},
		{"mcp", "tools/call", "-", "dispatch → handleToolsCall", "runs one tool, returns text content + isError"},
		{"mcp", "prompts/list", "-", "dispatch", "empty list (capability not advertised)"},
		{"mcp", "resources/list", "-", "dispatch", "empty list (capability not advertised)"},
		{"mcp", "*", "-", "dispatch", "-32601 method not found"},
	}
}

func printRoutes() {
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "TRANSPORT\tMETHOD\tROUTE\tHANDLER\tNOTES")
	for _, r := range routes() {
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", r.Transport, r.Method, r.Path, r.Handler, r.Notes)
	}
	w.Flush()
}

// handler builds the HTTP mux. Kept separate from serveHTTP so tests can mount
// it in httptest without opening a port.
func (s *server) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/health", s.handleHealth)
	mux.HandleFunc("/routes", s.handleRoutesJSON)
	mux.HandleFunc("/mcp", s.handleHTTP)
	mux.HandleFunc("/", s.handleDashboard)
	return mux
}

// serveHTTP exposes the same messages over Streamable HTTP so the server can
// be driven with curl and observed without an AI host.
func (s *server) serveHTTP(addr string) error {
	srv := &http.Server{Addr: addr, Handler: s.handler(), ReadHeaderTimeout: 10 * time.Second}
	return srv.ListenAndServe()
}

func (s *server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	registered, live := s.callerCount()
	fmt.Fprintf(w, `{"ok":true,"server":%q,"version":%q,"client_id":%q,"callers_registered":%d,"callers_live":%d}`+"\n",
		serverName, version, s.baseID, registered, live)
}

func (s *server) handleRoutesJSON(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	_ = enc.Encode(map[string]any{"server": serverName, "version": version, "routes": routes()})
}

// handleDashboard renders the observable view: what this process answers, and
// what it has recently been asked. No JavaScript, refreshes itself.
func (s *server) handleDashboard(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	s.traceMu.Lock()
	ev := append([]event(nil), s.events...)
	s.traceMu.Unlock()

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprintf(w, `<!doctype html><meta charset="utf-8"><title>%s</title>
<meta http-equiv="refresh" content="2">
<style>
 body{font:13px/1.55 ui-monospace,SFMono-Regular,Menlo,monospace;background:#0f1115;color:#d7dae0;margin:24px}
 h1{font-size:16px;margin:0} h2{font-size:12px;margin:28px 0 8px;color:#8b93a1;letter-spacing:.1em;text-transform:uppercase}
 table{border-collapse:collapse;width:100%%}
 td,th{text-align:left;padding:5px 12px 5px 0;border-bottom:1px solid #1e232b;vertical-align:top}
 th{color:#8b93a1;font-weight:400}
 .dim{color:#8b93a1} .in{color:#79c0ff} .out{color:#7ee787}
 pre{white-space:pre-wrap;word-break:break-all;margin:0}
</style>
<h1>%s %s</h1>
<div class="dim">MCP client instance %s</div>
<h2>What this process answers</h2>
<table><tr><th>transport</th><th>method</th><th>route</th><th>handler</th><th>notes</th></tr>`,
		serverName, serverName, version, html.EscapeString(s.baseID))

	for _, rt := range routes() {
		fmt.Fprintf(w, `<tr><td>%s</td><td>%s</td><td>%s</td><td>%s</td><td class="dim">%s</td></tr>`,
			rt.Transport, rt.Method, html.EscapeString(rt.Path), rt.Handler, html.EscapeString(rt.Notes))
	}

	fmt.Fprint(w, `</table><h2>Recent traffic (newest first)</h2>
<table><tr><th>time</th><th>dir</th><th>message</th></tr>`)
	if len(ev) == 0 {
		fmt.Fprint(w, `<tr><td colspan="3" class="dim">nothing yet — call a tool</td></tr>`)
	}
	for i := len(ev) - 1; i >= 0; i-- {
		cls := "in"
		if ev[i].Dir == "->" {
			cls = "out"
		}
		fmt.Fprintf(w, `<tr><td class="dim">%s</td><td class="%s">%s</td><td><pre>%s</pre></td></tr>`,
			ev[i].At.Format("15:04:05.000"), cls, ev[i].Dir, html.EscapeString(clip(ev[i].Text, 600)))
	}
	fmt.Fprint(w, `</table>`)
}

func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

func (s *server) handleHTTP(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodPost:
		body, err := io.ReadAll(io.LimitReader(r.Body, 4<<20))
		if err != nil {
			http.Error(w, "read body", http.StatusBadRequest)
			return
		}

		// Identity comes from the Authorization header and nowhere else. The
		// token decides which caller this is; the client id behind it decides
		// which conversations they can see.
		token := tokenFrom(r)
		if token == "" {
			writeAuthError(w, http.StatusUnauthorized, "missing authorization — send: Authorization: Bearer <token>")
			return
		}
		if _, ok := s.callers.Lookup(token); !ok {
			writeAuthError(w, http.StatusUnauthorized, "unknown token — mint one with: vireo-mcp -issue-token")
			return
		}

		resp := s.handleLine(s.instanceFor(token), bytes.TrimSpace(body))
		if resp == "" {
			w.WriteHeader(http.StatusAccepted)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, resp+"\n")

	case http.MethodGet:
		// Server-sent events stream: MCP hosts use it for server-initiated
		// messages, we keep it as a liveness channel.
		flusher, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "streaming unsupported", http.StatusInternalServerError)
			return
		}
		token := tokenFrom(r)
		if token == "" {
			writeAuthError(w, http.StatusUnauthorized, "missing authorization — send: Authorization: Bearer <token>")
			return
		}
		if _, ok := s.callers.Lookup(token); !ok {
			writeAuthError(w, http.StatusUnauthorized, "unknown token")
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.WriteHeader(http.StatusOK)
		fmt.Fprintf(w, ": connected to %s %s\n\n", serverName, version)
		flusher.Flush()
		<-r.Context().Done()

	default:
		w.Header().Set("Allow", "GET, POST")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// tokenFrom reads the bearer token from the Authorization header. This is the
// only place identity enters: one header, one string. A bare token without the
// "Bearer " prefix is accepted too, because that is what curl makes easy.
func tokenFrom(r *http.Request) string {
	v := strings.TrimSpace(r.Header.Get("Authorization"))
	if v == "" {
		return ""
	}
	if len(v) > 7 && strings.EqualFold(v[:7], "bearer ") {
		v = strings.TrimSpace(v[7:])
	}
	return v
}

// writeAuthError answers in JSON-RPC shape so an MCP client can surface it.
func writeAuthError(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("WWW-Authenticate", `Bearer realm="vireo-mcp"`)
	w.WriteHeader(status)
	fmt.Fprintf(w, `{"jsonrpc":"2.0","id":null,"error":{"code":-32001,"message":%q}}`+"\n", message)
}

// rpcMessage is the loose JSON-RPC 2.0 envelope; id may be absent
// (notification) or a number or string.
type rpcMessage struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

// handleLine routes one JSON-RPC message; notifications produce no response.
func (s *server) handleLine(d *dialer.Dialer, line []byte) string {
	s.record("<-", string(line))

	var msg rpcMessage
	if err := json.Unmarshal(line, &msg); err != nil {
		resp := marshalResponse(rpcResponse{
			JSONRPC: "2.0",
			ID:      nil,
			Error:   &rpcError{Code: -32700, Message: "parse error"},
		})
		s.record("->", resp)
		return resp
	}

	// Notifications (no id) get no response. A cancel is the one that has to do
	// something before it is dropped.
	if strings.HasPrefix(msg.Method, "notifications/") {
		if msg.Method == "notifications/cancelled" {
			s.handleCancelled(msg.Params)
		}
		return ""
	}

	resp := s.dispatch(d, msg)
	if resp != "" {
		s.record("->", resp)
	}
	return resp
}

func (s *server) dispatch(d *dialer.Dialer, msg rpcMessage) string {
	switch msg.Method {
	case "initialize":
		return respond(msg.ID, map[string]any{
			"protocolVersion": negotiateProtocolVersion(msg.Params),
			"capabilities":    map[string]any{"tools": map[string]any{}},
			"serverInfo":      map[string]any{"name": serverName, "version": version},
		})

	case "ping":
		return respond(msg.ID, map[string]any{})

	case "tools/list":
		return respond(msg.ID, map[string]any{"tools": s.tools(d)})

	case "tools/call":
		return s.handleToolsCall(d, msg)

	case "prompts/list":
		return respond(msg.ID, map[string]any{"prompts": []any{}})

	case "resources/list":
		return respond(msg.ID, map[string]any{"resources": []any{}})

	default:
		return respondError(msg.ID, -32601, "method not found: "+msg.Method)
	}
}

// aboutTool reports which build is running, so "am I on the new binary?" has an
// answer from inside the MCP host instead of from ps.
func aboutTool() map[string]any {
	return map[string]any{
		"name":        "about",
		"description": "Report the version and build of this MCP server, the caller identity it presents, and whether it is connected to a daemon. Use it to confirm which build a host is running.",
		"inputSchema": map[string]any{"type": "object", "properties": map[string]any{}},
	}
}

// tools is the catalogue plus the server's own metadata tool.
func (s *server) tools(d *dialer.Dialer) []map[string]any {
	return append(d.Tools(), aboutTool())
}

func (s *server) about(d *dialer.Dialer) string {
	addr, agent, connected := d.State()
	registered, live := s.callerCount()
	link := "not connected"
	if connected {
		link = fmt.Sprintf("connected to %s (agent %q)", addr, agent)
	}
	if agent == "" {
		agent = "-"
	}
	return fmt.Sprintf("%s %s\nbuild: %s (%s)\nMCP protocol: %s\ncaller identity: %s\nlabel: %q\ndaemon: %s\ncallers: %d registered, %d live",
		serverName, version, buildCommit, buildTime, defaultProtocolVersion,
		d.ClientID(), s.name, link, registered, live)
}

// handleToolsCall runs one tool and wraps its text result as MCP content.
func (s *server) handleToolsCall(d *dialer.Dialer, msg rpcMessage) string {
	var params struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
		// Hosts that want progress send a token here; without one there is
		// nowhere to report to, so the call simply blocks.
		Meta struct {
			ProgressToken json.RawMessage `json:"progressToken"`
		} `json:"_meta"`
	}
	if err := json.Unmarshal(msg.Params, &params); err != nil || params.Name == "" {
		return respondError(msg.ID, -32602, "tools/call needs {name, arguments}")
	}
	if params.Name == "about" {
		return respond(msg.ID, map[string]any{
			"content": []map[string]any{{"type": "text", "text": s.about(d)}},
			"isError": false,
		})
	}
	if params.Arguments == nil {
		params.Arguments = json.RawMessage("{}")
	}

	// The turn is already on its way to the daemon, so cancelling stops the
	// wait and asks the daemon to abandon the call. It is not a rollback.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	key := requestKey(msg.ID)
	s.inflightMu.Lock()
	if s.inflight == nil {
		s.inflight = map[string]func(){}
	}
	s.inflight[key] = func() {
		d.Cancel()
		cancel()
	}
	s.inflightMu.Unlock()
	defer func() {
		s.inflightMu.Lock()
		delete(s.inflight, key)
		s.inflightMu.Unlock()
	}()

	s.beginProgress(params.Meta.ProgressToken)
	done := make(chan struct{})
	go s.keepAlive(done)
	text, ok := d.Handle(ctx, params.Name, params.Arguments)
	close(done)
	s.endProgress()

	if ctx.Err() != nil {
		// The host gave up. Say so plainly: the turn may already have run.
		return respondError(msg.ID, codeCancelled,
			"cancelled by the host — the turn may still have completed on the daemon")
	}

	result := map[string]any{
		"content": []map[string]any{{"type": "text", "text": text}},
		"isError": !ok,
	}
	return respond(msg.ID, result)
}

// handleCancelled abandons the tool call the host names. The turn was already
// sent to the daemon, so this stops the wait and asks the daemon to abandon the
// backend call too — it is not a rollback.
func (s *server) handleCancelled(params json.RawMessage) {
	var p struct {
		RequestID json.RawMessage `json:"requestId"`
		Reason    string          `json:"reason"`
	}
	if err := json.Unmarshal(params, &p); err != nil || len(p.RequestID) == 0 {
		return
	}
	key := requestKey(p.RequestID)
	s.inflightMu.Lock()
	cancel, ok := s.inflight[key]
	s.inflightMu.Unlock()
	if !ok {
		return // the call already finished; nothing to abandon
	}
	s.logf("cancelled request %s (%s)", key, p.Reason)
	cancel()
}

// requestKey normalises a JSON-RPC id into a map key. Ids arrive as rendered
// JSON, so the same id renders the same way on both sides of the exchange.
func requestKey(id json.RawMessage) string {
	return string(bytes.TrimSpace(id))
}

// beginProgress starts a turn: the token decides whether anything is
// reported, and the counter restarts per tool call.
func (s *server) beginProgress(token json.RawMessage) {
	s.progressMu.Lock()
	defer s.progressMu.Unlock()
	s.progressToken = string(token)
	s.progressSeq = 0
}

func (s *server) endProgress() {
	s.progressMu.Lock()
	defer s.progressMu.Unlock()
	s.progressToken = ""
}

// notifyProgress reports one progress line to the host. No token means the host
// did not ask for progress; the turn still completes, it is just silent.
func (s *server) notifyProgress(text string) {
	text = strings.TrimSpace(text)
	if s.progressOut == nil || text == "" {
		return
	}
	s.progressMu.Lock()
	token := s.progressToken
	s.progressSeq++
	seq := s.progressSeq
	s.progressMu.Unlock()
	if token == "" {
		return
	}

	msg := map[string]any{
		"jsonrpc": "2.0",
		"method":  "notifications/progress",
		"params": map[string]any{
			"progressToken": json.RawMessage(token),
			"progress":      seq,
			"message":       text,
		},
	}
	b, err := json.Marshal(msg)
	if err != nil {
		return
	}
	s.record("->", string(b))
	s.outMu.Lock()
	defer s.outMu.Unlock()
	fmt.Fprintln(s.progressOut, string(b))
}

// keepAlive reports a generic line while a quiet backend works, so hosts that
// time out idle calls do not abandon a slow turn.
func (s *server) keepAlive(done <-chan struct{}) {
	t := time.NewTicker(10 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-done:
			return
		case <-t.C:
			s.notifyProgress("still working…")
		}
	}
}

func negotiateProtocolVersion(params json.RawMessage) string {
	var p struct {
		ProtocolVersion string `json:"protocolVersion"`
	}
	if err := json.Unmarshal(params, &p); err == nil && p.ProtocolVersion != "" {
		return p.ProtocolVersion
	}
	return defaultProtocolVersion
}

func respond(id json.RawMessage, result any) string {
	b, err := json.Marshal(result)
	if err != nil {
		return respondError(id, -32603, "internal error: "+err.Error())
	}
	return marshalResponse(rpcResponse{JSONRPC: "2.0", ID: id, Result: b})
}

func respondError(id json.RawMessage, code int, message string) string {
	return marshalResponse(rpcResponse{
		JSONRPC: "2.0",
		ID:      id,
		Error:   &rpcError{Code: code, Message: message},
	})
}

func marshalResponse(r rpcResponse) string {
	b, err := json.Marshal(r)
	if err != nil {
		return `{"jsonrpc":"2.0","id":null,"error":{"code":-32603,"message":"marshal error"}}`
	}
	return string(b)
}
