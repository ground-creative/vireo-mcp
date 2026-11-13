// Package client is the connection library for vireo daemons. It resolves a
// hostname to a daemon address (an explicit host:port, or an SRV record),
// connects over TLS or QUIC, negotiates with a stable client id, and exposes
// the manifest, sessions, and messaging.
//
// One connection is opened at Connect and reused for every later call; the
// daemon's connection loop serves any number of messages. A transport failure
// drops the connection and the next call dials again, so a daemon restart is a
// retry rather than an error the caller has to handle.
package client

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/quic-go/quic-go"

	"vireo-mcp/internal/frame"
	"vireo-mcp/internal/protocol"
)

// SRVService and SRVProto form the lookup name: _vireo._tcp.<host>.
const (
	SRVService = "vireo"
	SRVProto   = "tcp"
)

// Options configures a client for one daemon.
type Options struct {
	Addr     string // resolved daemon address host:port
	ClientID string // stable, required by the daemon
	Name     string // human-readable client label (optional)
	Timeout  time.Duration
	Insecure bool // skip TLS verification (dev only)
	UseQUIC  bool
}

// Client talks to one vireo daemon. The manifest is captured at Connect and
// the connection is held open: every call reuses it, and a broken connection
// is replaced on the next call.
type Client struct {
	opts     Options
	manifest protocol.Manifest

	// mu serialises round trips. The client uses stream id 1 for its own
	// calls, so two concurrent calls on one connection would interleave.
	mu    sync.Mutex
	rw    io.ReadWriteCloser
	rd    *frame.Reader
	dials int
}

// Resolver abstracts SRV lookups so discovery can be tested without DNS.
type Resolver interface {
	LookupSRV(service, proto, name string) (cname string, addrs []*net.SRV, err error)
}

// Discover resolves a hostname to a daemon address:
//
//  1. host already carries a port → used as-is, no lookup
//  2. SRV lookup _vireo._tcp.<host> → host and port from the record
//
// There is no default port and no fallback: an endpoint that is not published
// in DNS has to be addressed explicitly as host:port. Guessing a port would
// make a typo in a hostname look like a daemon that is down.
func Discover(host string, res Resolver) (string, error) {
	addr, _, err := DiscoverDetail(host, res)
	return addr, err
}

// Via values reported by DiscoverDetail.
const (
	ViaExplicit = "explicit address" // host:port given as-is
	ViaSRV      = "SRV record"       // _vireo._tcp.<host> answered
)

// SystemResolver looks records up with the host resolver. Pass nil to Discover
// to get this.
var SystemResolver Resolver = systemResolver{}

type systemResolver struct{}

func (systemResolver) LookupSRV(service, proto, name string) (string, []*net.SRV, error) {
	return net.DefaultResolver.LookupSRV(context.Background(), service, proto, name)
}

// DiscoverDetail is Discover with provenance: how the address was found.
func DiscoverDetail(host string, res Resolver) (addr, via string, err error) {
	host = strings.TrimSpace(host)
	if host == "" {
		return "", "", fmt.Errorf("client: empty host")
	}
	if _, _, err := net.SplitHostPort(host); err == nil {
		return host, ViaExplicit, nil // explicit port: no lookup needed
	}
	if res == nil {
		res = SystemResolver
	}
	// A bracketed-but-portless IPv6 literal ("[::1]") loses its brackets so
	// net.JoinHostPort re-adds them exactly once.
	if strings.HasPrefix(host, "[") && strings.HasSuffix(host, "]") {
		host = host[1 : len(host)-1]
	}
	if _, addrs, lerr := res.LookupSRV(SRVService, SRVProto, host); lerr == nil && len(addrs) > 0 {
		// Pick the highest-priority record (lowest value); ties broken by
		// the resolver's ordering.
		best := addrs[0]
		for _, s := range addrs[1:] {
			if s.Priority < best.Priority {
				best = s
			}
		}
		target := strings.TrimSuffix(best.Target, ".")
		return net.JoinHostPort(target, strconv.Itoa(int(best.Port))), ViaSRV, nil
	}
	return "", "", fmt.Errorf("no SRV record _%s._%s.%s — connect with an explicit host:port instead",
		SRVService, SRVProto, host)
}

// Connect dials opts.Addr, negotiates with the client id, and captures the
// manifest. The connection stays open until Close.
func Connect(ctx context.Context, opts Options) (*Client, error) {
	if opts.ClientID == "" {
		return nil, fmt.Errorf("client: a client id is required")
	}
	if opts.Timeout <= 0 {
		opts.Timeout = 15 * time.Second
	}
	c := &Client{opts: opts}
	if err := c.open(ctx); err != nil {
		return nil, err
	}
	return c, nil
}

// open dials and negotiates, replacing any current connection. Callers must
// hold c.mu or be the constructor.
func (c *Client) open(ctx context.Context) error {
	rw, err := dial(ctx, c.opts)
	if err != nil {
		return fmt.Errorf("dial %s: %w", c.opts.Addr, err)
	}
	m, err := negotiate(rw, c.opts)
	if err != nil {
		_ = rw.Close()
		return err
	}
	c.rw = rw
	c.rd = frame.NewReader(rw)
	c.manifest = m
	c.dials++
	return nil
}

// Close releases the connection. Later calls re-dial, so a closed client is
// still usable — it just costs a handshake again.
func (c *Client) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.closeLocked()
}

func (c *Client) closeLocked() error {
	if c.rw == nil {
		return nil
	}
	err := c.rw.Close()
	c.rw, c.rd = nil, nil
	return err
}

// Dials reports how many connections this client has opened. Diagnostic only.
func (c *Client) Dials() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.dials
}

// Manifest returns the daemon's manifest as seen at Connect.
func (c *Client) Manifest() protocol.Manifest { return c.manifest }

// Agents returns the agents the daemon fronts.
func (c *Client) Agents() []protocol.AgentEntry { return c.manifest.Agents }

// Agent finds one agent entry by name.
func (c *Client) Agent(name string) (protocol.AgentEntry, bool) {
	for _, a := range c.manifest.Agents {
		if a.Name == name {
			return a, true
		}
	}
	return protocol.AgentEntry{}, false
}

// SessionInfo describes a session the caller holds, including the id the
// daemon minted. Ids are plumbing; summaries and dates are what humans see.
type SessionInfo struct {
	ID      string
	Agent   string
	Summary string
	Turns   int
	Updated time.Time
	Status  string
}

// Reply is one agent answer plus the session envelope it updated.
type Reply struct {
	Text    string
	Session SessionInfo
}

// SendMessage delivers one turn to an agent. sessionID may be empty for a
// stateless agent.
func (c *Client) SendMessage(ctx context.Context, agent, sessionID, message string) (Reply, error) {
	req := protocol.MessageRequest{Agent: agent, SessionID: sessionID, Message: message}
	var out Reply
	err := c.call(ctx, protocol.TypeMessage, req, func(data []byte, end *protocol.EndPayload) error {
		var resp protocol.MessageResponse
		if err := json.Unmarshal(data, &resp); err != nil {
			return err
		}
		out.Text = resp.Reply
		if end != nil && end.Session != nil {
			out.Session = fromProtocol(*end.Session)
		}
		return nil
	})
	if err != nil {
		return Reply{}, err
	}
	return out, nil
}

// NewSession asks the daemon to mint a session with one agent.
func (c *Client) NewSession(ctx context.Context, agent, summary string) (SessionInfo, error) {
	var info protocol.SessionInfo
	err := c.session(ctx, protocol.SessionOpNew, protocol.SessionRequest{Agent: agent, Summary: summary},
		func(data []byte, end *protocol.EndPayload) error {
			if end != nil && end.Session != nil {
				info = *end.Session
				return nil
			}
			return json.Unmarshal(data, &info)
		})
	if err != nil {
		return SessionInfo{}, err
	}
	return fromProtocol(info), nil
}

// ListSessions returns the caller's sessions with one agent, newest first.
func (c *Client) ListSessions(ctx context.Context, agent string, limit int) ([]SessionInfo, error) {
	if limit <= 0 {
		limit = 20
	}
	var lr protocol.ListSessionsResponse
	err := c.session(ctx, protocol.SessionOpList, protocol.SessionRequest{Agent: agent, Limit: limit},
		func(data []byte, _ *protocol.EndPayload) error {
			return json.Unmarshal(data, &lr)
		})
	if err != nil {
		return nil, err
	}
	out := make([]SessionInfo, 0, len(lr.Sessions))
	for _, s := range lr.Sessions {
		out = append(out, fromProtocol(s))
	}
	return out, nil
}

// GetSession returns one session envelope.
func (c *Client) GetSession(ctx context.Context, agent, sessionID string) (SessionInfo, error) {
	var info protocol.SessionInfo
	err := c.session(ctx, protocol.SessionOpGet, protocol.SessionRequest{Agent: agent, ID: sessionID},
		func(data []byte, _ *protocol.EndPayload) error {
			return json.Unmarshal(data, &info)
		})
	if err != nil {
		return SessionInfo{}, err
	}
	return fromProtocol(info), nil
}

// CloseSession closes a session with one agent.
func (c *Client) CloseSession(ctx context.Context, agent, sessionID, reason string) (SessionInfo, error) {
	var info protocol.SessionInfo
	err := c.session(ctx, protocol.SessionOpClose, protocol.SessionRequest{Agent: agent, ID: sessionID, Reason: reason},
		func(data []byte, _ *protocol.EndPayload) error {
			return json.Unmarshal(data, &info)
		})
	if err != nil {
		return SessionInfo{}, err
	}
	return fromProtocol(info), nil
}

// PruneSessions deletes finished sessions older than retention and reports
// the census before and after. An empty retention keeps the configured value.
func (c *Client) PruneSessions(ctx context.Context, agent, retention string) (protocol.PruneResult, error) {
	var out protocol.PruneResult
	err := c.session(ctx, protocol.SessionOpPrune, protocol.SessionRequest{Agent: agent, Retention: retention},
		func(data []byte, _ *protocol.EndPayload) error {
			return json.Unmarshal(data, &out)
		})
	if err != nil {
		return protocol.PruneResult{}, err
	}
	return out, nil
}

// session runs one session operation on the held connection.
func (c *Client) session(ctx context.Context, op string, req protocol.SessionRequest, onDone func(data []byte, end *protocol.EndPayload) error) error {
	req.Op = op
	return c.call(ctx, protocol.TypeSession, req, func(data []byte, end *protocol.EndPayload) error {
		return onDone(data, end)
	})
}

// Error is a protocol-level error from the daemon (a 0x05 frame).
type Error struct {
	Code    string
	Message string
}

func (e *Error) Error() string { return e.Code + ": " + e.Message }

// call runs one request on the held connection. A transport failure drops the
// connection and retries once on a fresh one, so a daemon restart shows up as
// a slightly slower call instead of an error.
func (c *Client) call(ctx context.Context, typ uint8, req any, onDone func(data []byte, end *protocol.EndPayload) error) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	for attempt := 0; ; attempt++ {
		if c.rw == nil {
			if err := c.open(ctx); err != nil {
				return err
			}
		}

		err := c.roundTrip(typ, req, onDone)
		if err == nil {
			return nil
		}
		if _, isProtocol := err.(*Error); isProtocol {
			return err // the daemon answered; the connection is fine
		}
		_ = c.closeLocked()
		if attempt >= 1 {
			return err
		}
	}
}

// roundTrip writes one request and reads until the daemon ends or errors.
// Callers must hold c.mu.
func (c *Client) roundTrip(typ uint8, req any, onDone func(data []byte, end *protocol.EndPayload) error) error {
	payload, err := json.Marshal(req)
	if err != nil {
		return err
	}
	if err := frame.Write(c.rw, frame.Frame{StreamID: 1, Type: typ, Payload: payload}); err != nil {
		return fmt.Errorf("send request: %w", err)
	}

	var data []byte
	for {
		arm(c.rw, c.opts.Timeout)
		f, err := c.rd.Read()
		if err != nil {
			return fmt.Errorf("read response: %w", err)
		}
		switch f.Type {
		case protocol.TypeData:
			data = append(data, f.Payload...)
		case protocol.TypeEnd:
			var ep protocol.EndPayload
			_ = json.Unmarshal(f.Payload, &ep)
			return onDone(data, &ep)
		case protocol.TypeError:
			var e protocol.ErrorPayload
			_ = json.Unmarshal(f.Payload, &e)
			return &Error{Code: e.Code, Message: e.Message}
		}
	}
}

func negotiate(rw io.ReadWriteCloser, opts Options) (protocol.Manifest, error) {
	arm(rw, opts.Timeout)
	neg, _ := json.Marshal(protocol.NegotiateRequest{
		Protocol: protocol.Version,
		Client:   protocol.ClientInfo{ID: opts.ClientID, Name: opts.Name, Version: "0.4"},
	})
	if err := frame.Write(rw, frame.Frame{StreamID: 0, Type: protocol.TypeNegotiate, Payload: neg}); err != nil {
		return protocol.Manifest{}, fmt.Errorf("send negotiate: %w", err)
	}
	rd := frame.NewReader(rw)
	arm(rw, opts.Timeout)
	f, err := rd.Read()
	if err != nil {
		return protocol.Manifest{}, fmt.Errorf("read manifest: %w", err)
	}
	switch f.Type {
	case protocol.TypeManifest:
		var m protocol.Manifest
		if err := json.Unmarshal(f.Payload, &m); err != nil {
			return protocol.Manifest{}, err
		}
		return m, nil
	case protocol.TypeError:
		var e protocol.ErrorPayload
		_ = json.Unmarshal(f.Payload, &e)
		return protocol.Manifest{}, &Error{Code: e.Code, Message: e.Message}
	default:
		return protocol.Manifest{}, fmt.Errorf("unexpected frame %s during handshake", protocol.TypeName(f.Type))
	}
}

// quicSession bundles a QUIC connection and its stream.
type quicSession struct {
	conn   quic.Connection
	stream quic.Stream
}

func (q *quicSession) Read(p []byte) (int, error)  { return q.stream.Read(p) }
func (q *quicSession) Write(p []byte) (int, error) { return q.stream.Write(p) }
func (q *quicSession) SetReadDeadline(t time.Time) error {
	return q.stream.SetReadDeadline(t)
}
func (q *quicSession) Close() error {
	_ = q.stream.Close()
	return q.conn.CloseWithError(0, "done")
}

func dial(ctx context.Context, opts Options) (io.ReadWriteCloser, error) {
	dctx, cancel := context.WithTimeout(ctx, opts.Timeout)
	defer cancel()

	host := opts.Addr
	if h, _, err := net.SplitHostPort(opts.Addr); err == nil {
		host = h
	}
	tlsCfg := &tls.Config{
		InsecureSkipVerify: opts.Insecure,
		NextProtos:         []string{protocol.Version},
		ServerName:         host,
		MinVersion:         tls.VersionTLS13,
	}
	if opts.UseQUIC {
		conn, err := quic.DialAddr(dctx, opts.Addr, tlsCfg, &quic.Config{MaxIdleTimeout: opts.Timeout})
		if err != nil {
			return nil, err
		}
		stream, err := conn.OpenStreamSync(dctx)
		if err != nil {
			conn.CloseWithError(0, "open stream")
			return nil, err
		}
		return &quicSession{conn: conn, stream: stream}, nil
	}
	d := &net.Dialer{Timeout: opts.Timeout}
	return tls.DialWithDialer(d, "tcp", opts.Addr, tlsCfg)
}

func arm(rw io.ReadWriteCloser, d time.Duration) {
	if ds, ok := rw.(interface{ SetReadDeadline(time.Time) error }); ok {
		_ = ds.SetReadDeadline(time.Now().Add(d))
	}
}

func fromProtocol(s protocol.SessionInfo) SessionInfo {
	return SessionInfo{
		ID:      s.ID,
		Agent:   s.Agent,
		Summary: s.Summary,
		Turns:   s.Turns,
		Updated: s.UpdatedAt,
		Status:  s.Status,
	}
}
