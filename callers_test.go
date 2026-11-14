package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ground-creative/vireo-mcp/internal/protocol"
)

func newCallers(t *testing.T) (*callers, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "callers.json")
	c, err := openCallers(path)
	if err != nil {
		t.Fatalf("openCallers: %v", err)
	}
	return c, path
}

// The token is the only thing a caller holds. It must be unguessable, map to a
// distinct client id in the shape the daemon accepts, and survive a restart of
// the server.
func TestCallersIssueAndReload(t *testing.T) {
	c, path := newCallers(t)

	first, err := c.Issue("claude-desktop")
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	if !strings.HasPrefix(first.Token, callerTokenPrefix) {
		t.Fatalf("token %q lacks prefix", first.Token)
	}
	if got := len(strings.TrimPrefix(first.Token, callerTokenPrefix)); got != callerTokenBytes*2 {
		t.Fatalf("token entropy = %d hex chars, want %d", got, callerTokenBytes*2)
	}
	if err := protocol.ValidateClientID(first.ClientID); err != nil {
		t.Fatalf("client id %q is not in the shape the daemon accepts: %v", first.ClientID, err)
	}

	second, err := c.Issue("claude-desktop")
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	if second.Token == first.Token {
		t.Fatal("two callers must not share a token")
	}
	if second.ClientID == first.ClientID {
		t.Fatal("two callers must not share a client id")
	}

	// Reload as a restarted process would.
	again, err := openCallers(path)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	entry, ok := again.Lookup(first.Token)
	if !ok {
		t.Fatal("token must survive a restart")
	}
	if entry.ClientID != first.ClientID {
		t.Fatalf("client id changed across restart: %q -> %q", first.ClientID, entry.ClientID)
	}
	if again.Len() != 2 {
		t.Fatalf("registry holds %d callers, want 2", again.Len())
	}
}

// Ensure hands out one token per label and keeps it: the same label must not
// get a second identity on the next start.
func TestCallersEnsureIsStablePerLabel(t *testing.T) {
	c, _ := newCallers(t)

	first, err := c.Ensure("cursor")
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	second, err := c.Ensure("cursor")
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	if first.Token != second.Token {
		t.Fatal("the same label must get the same token")
	}
	if first.ClientID != second.ClientID {
		t.Fatal("the same label must keep the same client id")
	}

	other, err := c.Ensure("claude-desktop")
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	if other.Token == first.Token {
		t.Fatal("a different label must get a different token")
	}
}

func TestCallersUnknownToken(t *testing.T) {
	c, _ := newCallers(t)
	if _, ok := c.Lookup("vmcp_deadbeef"); ok {
		t.Fatal("an unissued token must not resolve")
	}
}

func TestCallersRevoke(t *testing.T) {
	c, path := newCallers(t)
	entry, _ := c.Issue("x")

	if !c.Revoke(entry.Token) {
		t.Fatal("Revoke should report success")
	}
	if _, ok := c.Lookup(entry.Token); ok {
		t.Fatal("a revoked token must not resolve")
	}
	if c.Revoke(entry.Token) {
		t.Fatal("revoking twice should report false")
	}

	again, err := openCallers(path)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if again.Len() != 0 {
		t.Fatalf("revocation was not persisted: %d callers", again.Len())
	}
}

func TestCallersPruneIdle(t *testing.T) {
	c, _ := newCallers(t)
	entry, _ := c.Issue("x")

	c.mu.Lock()
	c.byToken[entry.Token].LastSeen = time.Now().Add(-callerIdleTTL - time.Hour)
	c.pruneLocked(time.Now())
	c.saveLocked()
	c.mu.Unlock()

	if c.Len() != 0 {
		t.Fatalf("idle token should have been pruned, %d left", c.Len())
	}
}

func TestCallersRejectsCorruptRegistry(t *testing.T) {
	path := filepath.Join(t.TempDir(), "callers.json")
	if err := os.WriteFile(path, []byte("nope"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := openCallers(path); err == nil {
		t.Fatal("a corrupt registry must fail loudly")
	}
}

// Client ids are what the daemon scopes conversations by, so they must be
// stable per token and distinct between tokens.
func TestDeriveClientIDIsStableAndDistinct(t *testing.T) {
	a := deriveClientID("vmcp_aaaabbbbccccdddd")
	b := deriveClientID("vmcp_aaaabbbbccccdddd")
	if a != b {
		t.Fatalf("not stable: %q vs %q", a, b)
	}
	c := deriveClientID("vmcp_1111222233334444")
	if a == c {
		t.Fatalf("not distinct: %q", a)
	}
	if err := protocol.ValidateClientID(a); err != nil {
		t.Fatalf("client id %q is not in the shape the daemon accepts: %v", a, err)
	}
}
