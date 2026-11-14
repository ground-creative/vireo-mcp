// Package dialer is the stateful engine behind the vireo-mcp server: it
// wraps internal/client, holds one current daemon connection plus the current
// conversation per agent, and implements the tool flow the daemon's manifest
// describes:
//
//	connect(host) → list of things to do (agents) → choose one
//	→ conversations(agent): dates + summaries → new or resume → send_message
//
// Session ids are plumbing; tools speak in dates, summaries, and turn counts.
package dialer

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ground-creative/vireo-mcp/internal/client"
	"github.com/ground-creative/vireo-mcp/internal/protocol"
)

// Options configures the dialer.
type Options struct {
	ClientID string        // unique identity toward the daemon (never typed)
	Name     string        // human label, display only
	Insecure bool          // skip TLS verification (dev)
	Timeout  time.Duration // per-request timeout

	// OnPartial receives the progress lines an agent sends mid-turn. In an MCP
	// host these become notifications/progress, which is the only way a tool
	// call can say "hold on" without returning.
	OnPartial func(text string)
}

// Dialer is the tool engine. Tool calls are serialised: an MCP host may have
// several in flight at once, and the conversation and connection state below is
// a plain set of fields, not a concurrent data structure.
type Dialer struct {
	opts Options

	// mu serialises the tool bodies and the state they touch.
	mu sync.Mutex

	conn *client.Client

	// live mirrors conn for Cancel, which runs from another goroutine while a
	// tool call holds mu, and so cannot take it. It is the only field read
	// without the lock.
	live atomic.Pointer[client.Client]

	addr    string
	current string // chosen agent

	conversations map[string]string               // agent → current conversation id
	listed        map[string][]client.SessionInfo // agent → last listed conversations
}

func New(opts Options) *Dialer {
	return &Dialer{
		opts:          opts,
		conversations: map[string]string{},
		listed:        map[string][]client.SessionInfo{},
	}
}

// LoadOrCreateID returns this installation's identity toward the daemon.
//
// The id is never typed by a human: it is 128 bits from crypto/rand in the
// shape the daemon accepts, because a client id decides which conversations
// you can see. A guessable or shared value would let two unrelated
// installations collide on the same daemon and read each other's history.
//
// label only selects which local identity to use — one per host application —
// and is sent to the daemon as a display name. Two people in different countries
// may both run with label "claude-desktop"; they still get different ids,
// because the id is random per installation.
func LoadOrCreateID(label string) (string, error) {
	if v := strings.TrimSpace(os.Getenv("VIREO_MCP_ID")); v != "" {
		return v, nil // explicit override, for tests and odd deployments
	}

	label = sanitizeLabel(label)
	dir, err := os.UserConfigDir()
	if err != nil {
		return mintID(), nil // no config dir: still unique, just not durable
	}

	base := filepath.Join(dir, "vireo-mcp")
	path := filepath.Join(base, label+".id")
	if b, err := os.ReadFile(path); err == nil {
		if id := strings.TrimSpace(string(b)); protocol.ValidateClientID(id) == nil {
			return id, nil
		}
	}

	id := mintID()
	_ = os.MkdirAll(base, 0o755)
	if err := os.WriteFile(path, []byte(id), 0o600); err != nil {
		return id, nil // mint anyway; persistence is best-effort
	}
	return id, nil
}

const defaultLabel = "default"

// sanitizeLabel keeps a label usable as a file name.
func sanitizeLabel(label string) string {
	label = strings.ToLower(strings.TrimSpace(label))
	var b strings.Builder
	pendingDash := false
	for _, r := range label {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '_':
			if pendingDash && b.Len() > 0 {
				b.WriteByte('-')
			}
			pendingDash = false
			b.WriteRune(r)
		default:
			// Anything else (space, dot, slash, unicode) becomes one dash.
			pendingDash = true
		}
	}
	if b.Len() == 0 {
		return defaultLabel
	}
	return b.String()
}

// mintID returns a client id in the shape the daemon accepts.
func mintID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("vireo_%032x", time.Now().UnixNano())
	}
	return protocol.ClientIDPrefix + hex.EncodeToString(b)
}

// displayName is the label the daemon sees, purely for humans reading logs.
func (d *Dialer) displayName() string {
	if n := strings.TrimSpace(d.opts.Name); n != "" {
		return n
	}
	return "vireo-mcp"
}

// Close releases the daemon connection. Called when an idle HTTP caller is
// evicted, and when a new connect replaces the current daemon.
func (d *Dialer) Close() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.closeLocked()
}

// closeLocked is Close for a caller that already holds mu.
func (d *Dialer) closeLocked() error {
	d.live.Store(nil)
	if d.conn == nil {
		return nil
	}
	err := d.conn.Close()
	d.conn = nil
	return err
}

// ClientID reports the identity this dialer presents to the daemon.
func (d *Dialer) ClientID() string { return d.opts.ClientID }

// State reports the current connection, for diagnostics.
func (d *Dialer) State() (addr, agent string, connected bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.addr, d.current, d.conn != nil
}

// Tools returns the tool definitions served on tools/list, in flow order.
func (d *Dialer) Tools() []map[string]any {
	obj := func(props map[string]any, required ...string) map[string]any {
		s := map[string]any{"type": "object", "properties": props}
		if len(required) > 0 {
			s["required"] = required
		}
		return s
	}
	str := func(desc string) map[string]any { return map[string]any{"type": "string", "description": desc} }

	return []map[string]any{
		{
			"name":        "discover",
			"description": "Probe a domain for a vireo daemon without switching the current connection. Resolves the DNS SRV record _vireo._tcp.<host> and attempts a connection. Returns what the endpoint offers, or a clean \"no agent endpoint\" answer — which is information for deciding your next move, not an error.",
			"inputSchema": obj(map[string]any{
				"host": str("Domain to probe, e.g. example.com — or host:port for an explicit address."),
			}, "host"),
		},
		{
			"name":        "connect",
			"description": "Discover and connect to a vireo daemon (a business). Returns the list of agents — the things you can talk to there. Choose one with conversations or new_conversation.",
			"inputSchema": obj(map[string]any{
				"host": str("Hostname or host:port. A bare host is resolved through its _vireo._tcp SRV record."),
			}, "host"),
		},
		{
			"name":        "conversations",
			"description": "List your previous conversations with one agent (dates, summaries, turn counts) and make it the current agent.",
			"inputSchema": obj(map[string]any{
				"agent": str("Agent name from the connect list."),
			}, "agent"),
		},
		{
			"name":        "new_conversation",
			"description": "Start a new conversation with an agent and make it current.",
			"inputSchema": obj(map[string]any{
				"agent": str("Agent name from the connect list."),
				"title": str("Optional one-line description of this conversation."),
			}, "agent"),
		},
		{
			"name":        "resume_conversation",
			"description": "Resume a previous conversation: by its number from the conversations list, or the latest one.",
			"inputSchema": obj(map[string]any{
				"agent":  str("Agent name from the connect list."),
				"number": map[string]any{"type": "integer", "description": "Number from the conversations list."},
				"latest": map[string]any{"type": "boolean", "description": "Resume the most recent conversation."},
			}, "agent"),
		},
		{
			"name":        "send_message",
			"description": "Send a message to the current agent and get its reply. Conversational agents need a conversation: start one with new_conversation or resume_conversation first. Stateless agents answer single messages.",
			"inputSchema": obj(map[string]any{
				"message": str("What you want to say, in plain language."),
				"agent":   str("Optional agent override; defaults to the current agent."),
			}, "message"),
		},
		{
			"name":        "close_conversation",
			"description": "Close the current conversation with an agent. It can no longer be resumed.",
			"inputSchema": obj(map[string]any{
				"agent": str("Agent name."),
			}, "agent"),
		},
	}
}

// Handle runs one tool call and returns its text result. A tool error is
// returned as a friendly message with ok=false; protocol plumbing stays out.
//
// Calls are serialised: the host may issue several at once, and the state below
// is kept in plain maps.
func (d *Dialer) Handle(ctx context.Context, name string, args json.RawMessage) (text string, ok bool) {
	d.mu.Lock()
	defer d.mu.Unlock()

	switch name {
	case "discover":
		return d.toolDiscover(ctx, args)
	case "connect":
		return d.toolConnect(ctx, args)
	case "conversations":
		return d.toolConversations(ctx, args)
	case "new_conversation":
		return d.toolNew(ctx, args)
	case "resume_conversation":
		return d.toolResume(ctx, args)
	case "send_message":
		return d.toolSend(ctx, args)
	case "close_conversation":
		return d.toolClose(ctx, args)
	default:
		return fmt.Sprintf("unknown tool %q", name), false
	}
}

// Cancel asks the daemon to abandon the turn in flight, if there is one. It is
// best effort and safe to call while Handle is still running in another
// goroutine — which is the point: mu is deliberately not taken here, because the
// running call is the one holding it.
func (d *Dialer) Cancel() {
	if c := d.live.Load(); c != nil {
		_ = c.CancelCurrent()
	}
}

// ---- tools ----

// toolDiscover probes a domain for a daemon. Both outcomes are answers:
// "found, here's what it offers" and "no agent endpoint" return ok=true.
func (d *Dialer) toolDiscover(ctx context.Context, args json.RawMessage) (string, bool) {
	var in struct {
		Host string `json:"host"`
	}
	if err := json.Unmarshal(args, &in); err != nil || strings.TrimSpace(in.Host) == "" {
		return "discover needs {\"host\": \"example.com\"}", false
	}
	addr, via, err := client.DiscoverDetail(in.Host, nil)
	if err != nil {
		return "discovery failed: " + err.Error(), false
	}
	c, err := client.Connect(ctx, client.Options{
		Addr:     addr,
		ClientID: d.opts.ClientID,
		Name:     d.displayName() + " (probe)",
		Timeout:  d.opts.Timeout,
		Insecure: d.opts.Insecure,
	})
	if err != nil {
		return fmt.Sprintf("No agent endpoint at %s (tried %s, via %s): %v\nThe domain does not run a vireo daemon — or it is down.",
			in.Host, addr, via, err), true
	}
	// The probe holds a live connection now, so release it: a probe must leave
	// nothing open.
	defer c.Close()
	m := c.Manifest()
	var b strings.Builder
	fmt.Fprintf(&b, "Agent endpoint found at %s (via %s): %q (%s).\nThings it offers:", in.Host, via, m.Endpoint.Name, m.Endpoint.ID)
	for _, a := range m.Agents {
		mode := "conversational"
		if !a.Sessions {
			mode = "stateless"
		}
		fmt.Fprintf(&b, "\n  %s — %s [%s]", a.Name, a.Description, mode)
	}
	b.WriteString("\nConnect with the connect tool to start working with it.")
	return b.String(), true
}

func (d *Dialer) toolConnect(ctx context.Context, args json.RawMessage) (string, bool) {
	var in struct {
		Host string `json:"host"`
	}
	if err := json.Unmarshal(args, &in); err != nil || strings.TrimSpace(in.Host) == "" {
		return "connect needs {\"host\": \"example.com\"}", false
	}
	addr, via, err := client.DiscoverDetail(in.Host, nil)
	if err != nil {
		return "discovery failed: " + err.Error(), false
	}
	c, err := client.Connect(ctx, client.Options{
		Addr:     addr,
		ClientID: d.opts.ClientID,
		Name:     d.displayName(),
		Timeout:  d.opts.Timeout,
		Insecure: d.opts.Insecure,
		OnPartial: func(p protocol.PartialPayload) {
			if d.opts.OnPartial != nil && strings.TrimSpace(p.Text) != "" {
				d.opts.OnPartial(strings.TrimSpace(p.Text))
			}
		},
	})
	if err != nil {
		return fmt.Sprintf("connect to %s failed: %v", addr, err), false
	}
	d.closeLocked() // drop the previous daemon's connection, if any
	d.conn = c
	d.live.Store(c)
	d.addr = addr
	d.current = ""
	d.conversations = map[string]string{}
	d.listed = map[string][]client.SessionInfo{}

	m := c.Manifest()
	var b strings.Builder
	fmt.Fprintf(&b, "Connected to %q (%s) at %s via %s.\nThings you can do here:", m.Endpoint.Name, m.Endpoint.ID, addr, via)
	for _, a := range m.Agents {
		mode := "conversational"
		if !a.Sessions {
			mode = "stateless — send a message directly"
		}
		fmt.Fprintf(&b, "\n  %s — %s [%s]", a.Name, a.Description, mode)
	}
	b.WriteString("\nStart with conversations {agent} or new_conversation {agent}, then send_message.")
	return b.String(), true
}

func (d *Dialer) toolConversations(ctx context.Context, args json.RawMessage) (string, bool) {
	var in struct {
		Agent string `json:"agent"`
	}
	if err := json.Unmarshal(args, &in); err != nil || in.Agent == "" {
		return "conversations needs {\"agent\": \"orders\"}", false
	}
	if d.conn == nil {
		return "not connected — run connect first", false
	}
	entry, ok := d.conn.Agent(in.Agent)
	if !ok {
		return fmt.Sprintf("unknown agent %q — see the connect list", in.Agent), false
	}
	d.current = in.Agent
	if !entry.Sessions {
		return fmt.Sprintf("%s is stateless — send it a message directly, no conversation needed.", in.Agent), true
	}
	list, err := d.conn.ListSessions(ctx, in.Agent, 20)
	if err != nil {
		return friendly(err), false
	}
	d.listed[in.Agent] = list
	var b strings.Builder
	if len(list) == 0 {
		fmt.Fprintf(&b, "No conversations yet with %s. Start one: new_conversation.", in.Agent)
		return b.String(), true
	}
	fmt.Fprintf(&b, "Your conversations with %s:", in.Agent)
	for i, s := range list {
		summary := s.Summary
		if summary == "" {
			summary = "(no summary yet)"
		}
		closed := ""
		if s.Status == protocol.StatusClosed {
			closed = " — closed (history)"
		}
		fmt.Fprintf(&b, "\n  [%d] %s — %s (%d turns)%s", i+1, s.Updated.Format("Jan 2 15:04"), summary, s.Turns, closed)
	}
	b.WriteString("\nResume with resume_conversation {number} or {latest}, or start fresh with new_conversation.")
	return b.String(), true
}

func (d *Dialer) toolNew(ctx context.Context, args json.RawMessage) (string, bool) {
	var in struct {
		Agent string `json:"agent"`
		Title string `json:"title"`
	}
	if err := json.Unmarshal(args, &in); err != nil || in.Agent == "" {
		return "new_conversation needs {\"agent\": \"orders\"}", false
	}
	if d.conn == nil {
		return "not connected — run connect first", false
	}
	entry, ok := d.conn.Agent(in.Agent)
	if !ok {
		return fmt.Sprintf("unknown agent %q — see the connect list", in.Agent), false
	}
	if !entry.Sessions {
		return fmt.Sprintf("%s is stateless — just send it a message.", in.Agent), true
	}
	started, err := d.conn.NewSession(ctx, in.Agent, in.Title)
	if err != nil {
		return friendly(err), false
	}
	d.current = in.Agent
	d.conversations[in.Agent] = started.Session.ID
	delete(d.listed, in.Agent) // the cache predates this conversation
	return conversationOpened(in.Agent, started.StartMessage), true
}

// conversationOpened is what the caller reads when a conversation starts. The
// agent's greeting is part of it when the agent has one: the greeting was
// already shown, so it is not something the caller has to ask for again.
func conversationOpened(agent, greeting string) string {
	if greeting != "" {
		return fmt.Sprintf("New conversation with %s started.\n\n%s", agent, greeting)
	}
	return fmt.Sprintf("New conversation with %s started. Send a message to talk.", agent)
}

func (d *Dialer) toolResume(ctx context.Context, args json.RawMessage) (string, bool) {
	var in struct {
		Agent  string `json:"agent"`
		Number *int   `json:"number"`
		Latest bool   `json:"latest"`
	}
	if err := json.Unmarshal(args, &in); err != nil || in.Agent == "" {
		return "resume_conversation needs {\"agent\": \"orders\", \"number\": 1} or {\"latest\": true}", false
	}
	if d.conn == nil {
		return "not connected — run connect first", false
	}
	d.current = in.Agent
	list := d.listed[in.Agent]
	if len(list) == 0 {
		fetched, err := d.conn.ListSessions(ctx, in.Agent, 20)
		if err != nil {
			return friendly(err), false
		}
		list = fetched
		d.listed[in.Agent] = list
	}
	if len(list) == 0 {
		return fmt.Sprintf("No conversations with %s yet — start one with new_conversation.", in.Agent), true
	}
	var pick client.SessionInfo
	switch {
	case in.Latest:
		// newest one that is still open; if everything is closed, fall through
		// to the closed-conversation message below.
		pick = list[0]
		for _, s := range list {
			if s.Status != "closed" {
				pick = s
				break
			}
		}
	case in.Number != nil:
		if *in.Number < 1 || *in.Number > len(list) {
			return fmt.Sprintf("pick a number between 1 and %d (run conversations first)", len(list)), false
		}
		pick = list[*in.Number-1]
	default:
		return "give {\"number\": N} or {\"latest\": true}", false
	}
	// Closed conversations stay visible as history but cannot be resumed: the
	// daemon would reject every message with unknown_session.
	if pick.Status == "closed" {
		label := pick.Summary
		if label == "" {
			label = "(no summary)"
		}
		return fmt.Sprintf("That conversation is closed — history only: %q (%d turns). Start a new one with new_conversation(%s).",
			label, pick.Turns, in.Agent), false
	}
	d.conversations[in.Agent] = pick.ID
	summary := pick.Summary
	if summary == "" {
		summary = "(no summary yet)"
	}
	return fmt.Sprintf("Resumed conversation from %s — %q (%d turns). Send a message to continue.",
		pick.Updated.Format("Jan 2 15:04"), summary, pick.Turns), true
}

func (d *Dialer) toolSend(ctx context.Context, args json.RawMessage) (string, bool) {
	var in struct {
		Agent   string `json:"agent"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal(args, &in); err != nil || strings.TrimSpace(in.Message) == "" {
		return "send_message needs {\"message\": \"I'd like a margherita\"}", false
	}
	if d.conn == nil {
		return "not connected — run connect first", false
	}
	agent := strings.TrimSpace(in.Agent)
	if agent == "" {
		agent = d.current
	}
	if agent == "" {
		return "no agent chosen — run conversations or new_conversation first (or pass \"agent\")", false
	}
	entry, ok := d.conn.Agent(agent)
	if !ok {
		return fmt.Sprintf("unknown agent %q — see the connect list", agent), false
	}
	sid := d.conversations[agent]
	if entry.Sessions && sid == "" {
		return fmt.Sprintf("%s holds conversations — start one with new_conversation or resume_conversation first", agent), false
	}
	reply, err := d.conn.SendMessage(ctx, agent, sid, strings.TrimSpace(in.Message))
	if err != nil {
		return friendly(err), false
	}
	updated := reply.Session
	var b strings.Builder
	b.WriteString(reply.Text)
	if updated.ID != "" {
		if updated.Summary != "" {
			fmt.Fprintf(&b, "\n[%s, turn %d] %q", updated.Updated.Format("Jan 2 15:04"), updated.Turns, updated.Summary)
		}
		if updated.Status == protocol.StatusClosed {
			delete(d.conversations, agent)
			b.WriteString("\nthe conversation was closed by the agent — start a new one with new_conversation")
			return b.String(), true
		}
		d.conversations[agent] = updated.ID
	}
	return b.String(), true
}

func (d *Dialer) toolClose(ctx context.Context, args json.RawMessage) (string, bool) {
	var in struct {
		Agent string `json:"agent"`
	}
	if err := json.Unmarshal(args, &in); err != nil || in.Agent == "" {
		return "close_conversation needs {\"agent\": \"orders\"}", false
	}
	if d.conn == nil {
		return "not connected — run connect first", false
	}
	sid := d.conversations[in.Agent]
	if sid == "" {
		return fmt.Sprintf("no current conversation with %s", in.Agent), false
	}
	if _, err := d.conn.CloseSession(ctx, in.Agent, sid, "closed by the MCP client"); err != nil {
		return friendly(err), false
	}
	delete(d.conversations, in.Agent)
	delete(d.listed, in.Agent) // status changed; refetch on next listing
	return fmt.Sprintf("Conversation with %s closed.", in.Agent), true
}

// friendly maps daemon error codes to actionable text for the model.
func friendly(err error) string {
	if ce, ok := err.(*client.Error); ok {
		switch ce.Code {
		case protocol.CodeSessionRequired:
			return "the daemon wants a conversation first — new_conversation or resume_conversation"
		case protocol.CodeUnknownSession:
			return "that conversation is no longer known (the daemon may have restarted) — run conversations, then resume or start new"
		case protocol.CodeAgentNotFound:
			return "no such agent: " + ce.Message
		case protocol.CodeBadClientID:
			return "the daemon rejected the client id: " + ce.Message
		}
		return ce.Code + ": " + ce.Message
	}
	return err.Error()
}
