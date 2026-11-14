package main

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// The MCP server keeps no conversations — those live in the daemon. It keeps
// exactly one thing: a registry of the clients that have identified themselves,
//
//	token  →  client id  +  label  +  timestamps
//
// The token is what the caller holds and sends in the Authorization header. The
// client id is what we present to the daemon, and it is what the daemon scopes
// conversations by. Because the registry is a file, a caller that comes back
// after this process restarts is recognised again instead of silently landing
// in a new namespace.

// callerTokenPrefix makes a stray value from another system fail loudly rather
// than quietly creating a namespace nobody else can reach.
const callerTokenPrefix = "vmcp_"

// callerTokenBytes is 16 bytes = 128 bits of entropy in 32 hex chars.
const callerTokenBytes = 16

// callerIdleTTL is how long an unused token is remembered.
const callerIdleTTL = 30 * 24 * time.Hour

// caller is one external client of this MCP server. It is not a conversation
// session: it is "who is calling", not "what we talked about".
type caller struct {
	Token    string    `json:"token"`
	ClientID string    `json:"client_id"`
	Label    string    `json:"label,omitempty"`
	Created  time.Time `json:"created"`
	LastSeen time.Time `json:"last_seen"`
}

// callers is the token registry, persisted as JSON.
type callers struct {
	path    string
	mu      sync.Mutex
	byToken map[string]*caller
}

// openCallers loads the registry, creating an empty one when the file is new.
func openCallers(path string) (*callers, error) {
	c := &callers{path: path, byToken: map[string]*caller{}}

	raw, err := os.ReadFile(path)
	switch {
	case err == nil:
		var entries []*caller
		if err := json.Unmarshal(raw, &entries); err != nil {
			return nil, fmt.Errorf("%s is not valid JSON: %w", path, err)
		}
		for _, e := range entries {
			if e != nil && e.Token != "" && e.ClientID != "" {
				c.byToken[e.Token] = e
			}
		}
	case os.IsNotExist(err):
	default:
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	return c, nil
}

// save writes the registry atomically. Callers must hold mu.
func (c *callers) saveLocked() {
	entries := make([]*caller, 0, len(c.byToken))
	for _, e := range c.byToken {
		entries = append(entries, e)
	}
	blob, err := json.MarshalIndent(entries, "", "  ")
	if err != nil {
		return
	}
	if dir := filepath.Dir(c.path); dir != "" {
		_ = os.MkdirAll(dir, 0o755)
	}
	tmp := c.path + ".tmp"
	if err := os.WriteFile(tmp, blob, 0o600); err != nil {
		return
	}
	_ = os.Rename(tmp, c.path)
}

// Issue mints a token for a new caller and records it, together with the
// daemon-facing client id this caller will be known by.
func (c *callers) Issue(label string) (*caller, error) {
	token, err := mintToken()
	if err != nil {
		return nil, err
	}
	now := time.Now()
	entry := &caller{
		Token:    token,
		ClientID: deriveClientID(token),
		Label:    label,
		Created:  now,
		LastSeen: now,
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	c.byToken[token] = entry
	c.pruneLocked(now)
	c.saveLocked()
	return entry, nil
}

// Ensure returns the token registered for a label, minting one the first time.
// This is what the HTTP transport uses: the operator gets one token, and every
// request that presents it is the same caller.
func (c *callers) Ensure(label string) (*caller, error) {
	c.mu.Lock()
	for _, e := range c.byToken {
		if e.Label == label {
			c.mu.Unlock()
			return e, nil
		}
	}
	c.mu.Unlock()
	return c.Issue(label)
}

// Lookup resolves a token and stamps last-seen.
func (c *callers) Lookup(token string) (*caller, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	entry, ok := c.byToken[token]
	if !ok {
		return nil, false
	}
	entry.LastSeen = time.Now()
	return entry, true
}

// Revoke forgets a token; the client id it carried is not reused.
func (c *callers) Revoke(token string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, ok := c.byToken[token]; !ok {
		return false
	}
	delete(c.byToken, token)
	c.saveLocked()
	return true
}

// Len reports how many callers are registered.
func (c *callers) Len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.byToken)
}

// List returns the registered callers, oldest first.
func (c *callers) List() []*caller {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]*caller, 0, len(c.byToken))
	for _, e := range c.byToken {
		out = append(out, e)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Created.Before(out[j].Created) })
	return out
}

// pruneLocked drops tokens nobody has used for callerIdleTTL.
func (c *callers) pruneLocked(now time.Time) {
	for token, e := range c.byToken {
		if now.Sub(e.LastSeen) > callerIdleTTL {
			delete(c.byToken, token)
		}
	}
}

// deriveClientID builds the identity presented to the daemon from the token.
// The token is 128 random bits, so hashing it gives a stable, unguessable and
// distinct id per token — in the shape the daemon accepts.
func deriveClientID(token string) string {
	sum := sha256.Sum256([]byte("vireo-mcp:" + token))
	return "vireo_" + hex.EncodeToString(sum[:16])
}

// callersPath is where the token registry lives: beside the identity file, one
// registry per label so two host applications never share callers.
func callersPath(label string) string {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "vireo-mcp-callers.json"
	}
	name := strings.ToLower(strings.TrimSpace(label))
	if name == "" {
		name = "default"
	}
	var b strings.Builder
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-', r == '_':
			b.WriteRune(r)
		case r == ' ' || r == '.':
			b.WriteByte('-')
		}
	}
	if b.Len() == 0 {
		b.WriteString("default")
	}
	return filepath.Join(dir, "vireo-mcp", b.String()+".callers.json")
}

// mintToken returns a fresh opaque token.
func mintToken() (string, error) {
	b := make([]byte, callerTokenBytes)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("mint token: %w", err)
	}
	return callerTokenPrefix + hex.EncodeToString(b), nil
}
