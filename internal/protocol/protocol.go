// Package protocol defines the vireo wire vocabulary: frame types, error
// codes, and the JSON envelopes carried inside frame payloads.
//
// The protocol is message-based. A peer connects, negotiates, receives a
// manifest of the agents behind the endpoint, and then sends messages to those
// agents. Sessions are the daemon's own channel, carried on a separate frame so
// that the message channel stays free of anything but conversation.
package protocol

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"time"
)

// Version is the ALPN identifier and the protocol string carried in negotiate
// and manifest frames.
const Version = "vireo/1"

// Frame types.
const (
	TypeMessage uint8 = 0x01
	TypeData    uint8 = 0x02
	TypeEnd     uint8 = 0x03
	// TypePartial carries progress inside a turn: text the agent wants shown
	// while it is still working. It never ends the turn and never counts as
	// one; TypeEnd still follows.
	TypePartial   uint8 = 0x04
	TypeError     uint8 = 0x05
	TypeNegotiate uint8 = 0x10
	TypeManifest  uint8 = 0x11
	TypeSession   uint8 = 0x12
)

// TypeName returns a human-readable name for a frame type, for logs.
func TypeName(t uint8) string {
	switch t {
	case TypeMessage:
		return "message"
	case TypeData:
		return "data"
	case TypeEnd:
		return "end"
	case TypePartial:
		return "partial"
	case TypeError:
		return "error"
	case TypeNegotiate:
		return "negotiate"
	case TypeManifest:
		return "manifest"
	case TypeSession:
		return "session"
	}
	return "unknown"
}

// Error codes.
const (
	CodeBadClientID     = "bad_client_id"
	CodeBadProtocol     = "bad_protocol"
	CodeAgentNotFound   = "agent_not_found"
	CodeSessionRequired = "session_required"
	CodeUnknownSession  = "unknown_session"
	CodeUpstreamError   = "upstream_error"
	CodeInvalidFrame    = "invalid_frame"
	CodeInternal        = "internal"
)

// Session statuses.
const (
	StatusOpen    = "open"
	StatusClosed  = "closed"
	StatusExpired = "expired"
)

// Session operations, carried on a session frame. They are advertised in the
// manifest when session.list is enabled.
const (
	SessionOpNew   = "session.new"
	SessionOpList  = "session.list"
	SessionOpGet   = "session.get"
	SessionOpClose = "session.close"
	SessionOpPrune = "session.prune"
)

// ClientIDPrefix starts every client id.
const ClientIDPrefix = "vireo_"

// ClientIDHexLength is the number of hex characters after the prefix: 16
// random bytes, so two peers colliding is not a practical concern.
const ClientIDHexLength = 32

// ClientIDPattern is the exact shape a client id must have: the prefix, then
// 32 lowercase hex characters. Nothing else is accepted — the id is the
// identity that sessions are scoped by, so it is not free-form text.
var ClientIDPattern = regexp.MustCompile(`^vireo_[0-9a-f]{32}$`)

// NewClientID mints a client id in the accepted shape.
func NewClientID() (string, error) {
	b := make([]byte, ClientIDHexLength/2)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("mint client id: %w", err)
	}
	return ClientIDPrefix + hex.EncodeToString(b), nil
}

// ValidateClientID reports why an id is not acceptable, or nil when it is.
func ValidateClientID(id string) error {
	if id == "" {
		return errors.New("a client id is required")
	}
	if !ClientIDPattern.MatchString(id) {
		return fmt.Errorf("must match %s (prefix %q then %d lowercase hex characters)",
			ClientIDPattern.String(), ClientIDPrefix, ClientIDHexLength)
	}
	return nil
}

// ClientInfo identifies the caller. ID is required; name and version are
// informational.
type ClientInfo struct {
	ID      string `json:"id"`
	Name    string `json:"name,omitempty"`
	Version string `json:"version,omitempty"`
}

// NegotiateRequest is the payload of the first frame on a connection. The
// client id is mandatory; a connection without one is rejected.
type NegotiateRequest struct {
	Protocol string     `json:"protocol"`
	Client   ClientInfo `json:"client"`
}

// Endpoint is the identity of the daemon presented to peers: the place you
// connected to, not an agent itself.
type Endpoint struct {
	ID          string `json:"id,omitempty"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}

// AgentEntry is one conversational agent behind the endpoint. Sessions says
// whether the agent holds multi-turn conversations; a stateless agent answers
// single messages and records nothing.
type AgentEntry struct {
	Name         string            `json:"name"`
	Description  string            `json:"description,omitempty"`
	Sessions     bool              `json:"sessions"`
	StartMessage string            `json:"start_message,omitempty"`
	Refs         map[string]string `json:"refs,omitempty"`
}

// SessionPolicy is the session contract advertised to callers. The id format
// is deliberately absent: the server mints ids; callers receive and echo them.
type SessionPolicy struct {
	TTLSeconds int  `json:"ttl_seconds,omitempty"`
	List       bool `json:"list_enabled"`
}

// Manifest is the daemon's answer to a negotiate frame.
type Manifest struct {
	Protocol string            `json:"protocol"`
	Endpoint Endpoint          `json:"endpoint"`
	Commands []string          `json:"commands,omitempty"`
	Agents   []AgentEntry      `json:"agents"`
	Refs     map[string]string `json:"refs,omitempty"`
	Session  SessionPolicy     `json:"session"`
}

// MessageRequest is one turn of conversation. Agent names the agent to talk
// to, SessionID continues an existing conversation, and Message is the text.
type MessageRequest struct {
	Agent     string `json:"agent"`
	SessionID string `json:"session_id,omitempty"`
	Message   string `json:"message"`
}

// SessionUpdate is the optional session hint an agent may attach to a reply
// so the daemon can keep the envelope current.
type SessionUpdate struct {
	Summary string `json:"summary,omitempty"`
	Status  string `json:"status,omitempty"`
}

// PartialPayload is the payload of a partial frame: something to show while the
// turn is still running.
type PartialPayload struct {
	Kind string `json:"kind"`
	Seq  int    `json:"seq,omitempty"`
	Text string `json:"text"`
}

// MessageResponse is the webhook reply contract. Reply is the agent's answer;
// Session optionally updates the envelope.
type MessageResponse struct {
	Reply   string         `json:"reply,omitempty"`
	Session *SessionUpdate `json:"session,omitempty"`
}

// EndPayload terminates one exchange; it carries the updated session envelope
// so callers can show summaries without an extra round trip.
type EndPayload struct {
	OK      bool         `json:"ok"`
	Session *SessionInfo `json:"session,omitempty"`
}

// ErrorPayload is the payload of error frames.
type ErrorPayload struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// SessionRequest is the payload of a session frame. Op selects the operation;
// the remaining fields are read per operation.
type SessionRequest struct {
	Op        string `json:"op"`
	Agent     string `json:"agent,omitempty"`
	ID        string `json:"id,omitempty"`
	Summary   string `json:"summary,omitempty"`
	Reason    string `json:"reason,omitempty"`
	Limit     int    `json:"limit,omitempty"`
	Cursor    string `json:"cursor,omitempty"`
	Retention string `json:"retention,omitempty"`
}

// NewSessionResponse is the answer to session.new: the fresh envelope, plus
// the agent's greeting when it has one. The greeting comes from the
// configuration, so no backend call is made for it.
type NewSessionResponse struct {
	Session      SessionInfo `json:"session"`
	StartMessage string      `json:"start_message,omitempty"`
}

// SessionInfo is the session envelope: identity and metadata, never
// conversation content. Dates are what callers display; ids are plumbing.
type SessionInfo struct {
	ID        string    `json:"id"`
	Owner     string    `json:"owner,omitempty"`
	Agent     string    `json:"agent"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
	ExpiresAt time.Time `json:"expires_at"`
	Summary   string    `json:"summary,omitempty"`
	Turns     int       `json:"turns"`
	Status    string    `json:"status"`
}

// ListSessionsResponse is the answer to session.list, ordered by updated_at
// descending.
type ListSessionsResponse struct {
	Sessions []SessionInfo `json:"sessions"`
	Next     string        `json:"next_cursor,omitempty"`
}

// PruneResult reports what a prune did.
type PruneResult struct {
	Retention string        `json:"retention"`
	Removed   int           `json:"removed"`
	Before    SessionCounts `json:"before"`
	After     SessionCounts `json:"after"`
}

// SessionCounts is the daemon's census of stored sessions.
type SessionCounts struct {
	Open    int `json:"open"`
	Closed  int `json:"closed"`
	Expired int `json:"expired"`
	Total   int `json:"total"`
}

// AgentNames extracts the agent names in order.
func AgentNames(agents []AgentEntry) []string {
	out := make([]string, 0, len(agents))
	for _, a := range agents {
		out = append(out, a.Name)
	}
	return out
}

// MarshalIndent is a small helper for logs and diagnostics.
func MarshalIndent(v any) []byte {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return []byte("null")
	}
	return b
}
