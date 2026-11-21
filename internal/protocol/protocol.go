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
	"sort"
	"time"
)

// Version is the ALPN identifier and the protocol string carried in negotiate
// and manifest frames.
const Version = "vireo/1"

// ManifestVersion is the manifest compatibility level this adapter understands.
// It is independent of Version (the wire protocol) and of the spec release tag.
const ManifestVersion = "1.2"

// ManifestVersionFallback is what a manifest without manifest_version means.
const ManifestVersionFallback = "1.0"

// Frame types.
const (
	TypeMessage uint8 = 0x01
	TypeData    uint8 = 0x02
	TypeEnd     uint8 = 0x03
	// TypePartial carries progress inside a turn: text the agent wants shown
	// while it is still working. It never ends the turn and never counts as
	// one; TypeEnd still follows.
	TypePartial uint8 = 0x04
	TypeError   uint8 = 0x05
	// TypeCancel asks the daemon to abandon the turn in flight on the stream
	// named by the frame's stream id. It is cooperative and best effort: the
	// daemon stops waiting and cancels the upstream request's context, but
	// work the backend already committed is not rolled back.
	TypeCancel    uint8 = 0x13
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
	case TypeCancel:
		return "cancel"
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

// Link is a published reference: a URI plus what it is. It replaces the
// deprecated refs map, which could carry only a key and a URL.
type Link struct {
	Rel         string `json:"rel"`
	Href        string `json:"href"`
	Label       string `json:"label,omitempty"`
	Type        string `json:"type,omitempty"`
	Description string `json:"description,omitempty"`
}

// SessionOps says which session operations the endpoint accepts.
type SessionOps struct {
	New   bool `json:"new"`
	Get   bool `json:"get"`
	List  bool `json:"list"`
	Close bool `json:"close"`
	Prune bool `json:"prune"`
}

// Capabilities is what the endpoint implements beyond the protocol minimum.
type Capabilities struct {
	Session        *SessionOps `json:"session,omitempty"`
	Progress       bool        `json:"progress,omitempty"`
	Cancel         bool        `json:"cancel,omitempty"`
	MaxTurnSeconds int         `json:"max_turn_seconds,omitempty"`
}

// Limits are advertised operational limits. They are advisory: a client must
// still handle rate_limited.
type Limits struct {
	MaxMessageBytes   int `json:"max_message_bytes,omitempty"`
	RequestsPerMinute int `json:"requests_per_minute,omitempty"`
	Burst             int `json:"burst,omitempty"`
	MaxTurns          int `json:"max_turns,omitempty"`
}

// AgentEntry is one agent behind the endpoint. Sessions says whether the agent
// holds multi-turn conversations.
type AgentEntry struct {
	ID           string            `json:"id,omitempty"`
	Name         string            `json:"name"`
	Description  string            `json:"description,omitempty"`
	Sessions     bool              `json:"sessions"`
	StartMessage string            `json:"start_message,omitempty"`
	Links        []Link            `json:"links,omitempty"`
	Limits       *Limits           `json:"limits,omitempty"`
	Session      *SessionPolicy    `json:"session,omitempty"`
	Refs         map[string]string `json:"refs,omitempty"`
}

// SessionPolicy is the session contract advertised to callers. The id format
// is deliberately absent: the server mints ids; callers receive and echo them.
// List is the 1.0 shape; Capabilities.Session is authoritative in 1.1.
type SessionPolicy struct {
	TTLSeconds int  `json:"ttl_seconds,omitempty"`
	List       bool `json:"list_enabled,omitempty"`
}

// Signature is reserved: in manifest_version 1.1 it is null.
type Signature struct{}

// Manifest is the daemon's answer to a negotiate frame.
type Manifest struct {
	ManifestVersion string        `json:"manifest_version"`
	Protocol        string        `json:"protocol"`
	Endpoint        Endpoint      `json:"endpoint"`
	Links           []Link        `json:"links,omitempty"`
	Capabilities    *Capabilities `json:"capabilities,omitempty"`
	Limits          *Limits       `json:"limits,omitempty"`
	Session         SessionPolicy `json:"session"`
	Agents          []AgentEntry  `json:"agents"`

	// Deprecated members, still read for compatibility (MAN-44, MAN-45).
	Commands []string          `json:"commands,omitempty"`
	Refs     map[string]string `json:"refs,omitempty"`

	// Freshness and integrity (MAN-39 .. MAN-43).
	GeneratedAt string     `json:"generated_at,omitempty"`
	ExpiresAt   string     `json:"expires_at,omitempty"`
	Digest      string     `json:"digest,omitempty"`
	Signature   *Signature `json:"signature"`
}

// Links yields the manifest's links, falling back to the deprecated refs map
// so a caller sees one vocabulary whichever the daemon sent (MAN-44).
func (m Manifest) LinksOrRefs() []Link {
	if len(m.Links) > 0 {
		return m.Links
	}
	if len(m.Refs) == 0 {
		return nil
	}
	keys := make([]string, 0, len(m.Refs))
	for k := range m.Refs {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]Link, 0, len(keys))
	for _, k := range keys {
		out = append(out, Link{Rel: k, Href: m.Refs[k]})
	}
	return out
}

// Validation patterns from MANIFEST.md. Kept identical to the daemon's copy: the
// two packages must not disagree about what a manifest is.
var (
	ManifestVersionPattern = regexp.MustCompile(`^[0-9]+\.[0-9]+$`)
	AgentNamePattern       = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)
	LinkRelPattern         = regexp.MustCompile(`^[a-z][a-z0-9._-]{0,63}$`)
	AbsoluteURIPattern     = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9+.\-]*:`)
	DigestPattern          = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
)

// Normalize fills the defaults a client is allowed to assume: a document with no
// manifest_version is a 1.0 document, not an invalid one (MAN-53).
func (m Manifest) Normalize() Manifest {
	if m.ManifestVersion == "" {
		m.ManifestVersion = ManifestVersionFallback
	}
	return m
}

// ValidateAsClient checks the minimum a client MUST enforce before it acts on a
// manifest, tolerating a 1.0 document (MAN-47 .. MAN-49, MAN-53).
func (m Manifest) ValidateAsClient() error {
	return m.Normalize().validate()
}

func (m Manifest) validate() error {
	if !ManifestVersionPattern.MatchString(m.ManifestVersion) {
		return fmt.Errorf("manifest_version %q is not MAJOR.MINOR (MAN-1)", m.ManifestVersion)
	}
	if m.Protocol == "" {
		return errors.New("protocol is required")
	}
	if m.Endpoint.Name == "" {
		return errors.New("endpoint.name is required")
	}
	if m.Agents == nil {
		return errors.New("agents is required and may be empty (MAN-7)")
	}
	seen := make(map[string]bool, len(m.Agents))
	anySessions := false
	for i, a := range m.Agents {
		if a.Name == "" {
			return fmt.Errorf("agents[%d]: name is required", i)
		}
		if !AgentNamePattern.MatchString(a.Name) {
			return fmt.Errorf("agents[%d] %q: name does not match %s (MAN-12)", i, a.Name, AgentNamePattern)
		}
		if seen[a.Name] {
			return fmt.Errorf("agents[%d] %q: duplicate name (MAN-12)", i, a.Name)
		}
		seen[a.Name] = true
		if a.Sessions {
			anySessions = true
		}
		if err := validateLinks(fmt.Sprintf("agents[%d] %q", i, a.Name), a.Links); err != nil {
			return err
		}
		if a.Session != nil && a.Session.TTLSeconds < 0 {
			return fmt.Errorf("agents[%d] %q: session.ttl_seconds must be positive (MAN-37)", i, a.Name)
		}
	}
	if err := validateLinks("links", m.Links); err != nil {
		return err
	}
	if m.Session.TTLSeconds < 0 {
		return errors.New("session.ttl_seconds must be positive (MAN-37)")
	}
	if m.Digest != "" && !DigestPattern.MatchString(m.Digest) {
		return fmt.Errorf("digest %q is not sha256:<64 hex> (MAN-40)", m.Digest)
	}
	if anySessions && m.Capabilities != nil && m.Capabilities.Session != nil && !m.Capabilities.Session.New {
		return errors.New("capabilities.session.new is false but an agent declares sessions (MAN-29)")
	}
	return nil
}

func validateLinks(where string, links []Link) error {
	for i, l := range links {
		if !LinkRelPattern.MatchString(l.Rel) {
			return fmt.Errorf("%s: links[%d] %q: rel does not match %s (MAN-20)", where, i, l.Rel, LinkRelPattern)
		}
		if l.Href == "" {
			return fmt.Errorf("%s: links[%d] %q: href is required (MAN-19)", where, i, l.Rel)
		}
		if !AbsoluteURIPattern.MatchString(l.Href) {
			return fmt.Errorf("%s: links[%d] %q: href %q is not an absolute URI (MAN-19)", where, i, l.Rel, l.Href)
		}
	}
	return nil
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
// so callers can show summaries without an extra round trip. Cancelled marks a
// turn the caller abandoned: OK is false, no envelope is attached, and the turn
// was not counted.
type EndPayload struct {
	OK        bool         `json:"ok"`
	Cancelled bool         `json:"cancelled,omitempty"`
	Session   *SessionInfo `json:"session,omitempty"`
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
