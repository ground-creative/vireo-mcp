package dialer

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"vireo-mcp/internal/protocol"
)

// The client id is what the daemon scopes conversations by, so it must have the
// exact shape the daemon accepts and it must never be a human-typed string.
func TestIdentityHasTheShapeTheDaemonAccepts(t *testing.T) {
	t.Setenv("VIREO_MCP_ID", "")
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("XDG_CONFIG_HOME", dir)

	id, err := LoadOrCreateID("claude-desktop")
	if err != nil {
		t.Fatalf("LoadOrCreateID: %v", err)
	}
	if err := protocol.ValidateClientID(id); err != nil {
		t.Fatalf("id %q is not in the shape the daemon accepts: %v", id, err)
	}
	if !strings.HasPrefix(id, protocol.ClientIDPrefix) {
		t.Fatalf("id %q lacks the %q prefix", id, protocol.ClientIDPrefix)
	}
}

// One identity per label, so two host applications never share history.
func TestIdentityIsGeneratedPerLabel(t *testing.T) {
	t.Setenv("VIREO_MCP_ID", "")
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("XDG_CONFIG_HOME", dir)

	first, err := LoadOrCreateID("claude-desktop")
	if err != nil {
		t.Fatalf("LoadOrCreateID: %v", err)
	}
	second, err := LoadOrCreateID("cursor")
	if err != nil {
		t.Fatalf("LoadOrCreateID: %v", err)
	}
	if first == second {
		t.Fatal("two labels must not share an identity")
	}

	// The same label must get the same identity back.
	again, err := LoadOrCreateID("claude-desktop")
	if err != nil {
		t.Fatalf("LoadOrCreateID: %v", err)
	}
	if again != first {
		t.Fatalf("identity changed: %q -> %q", first, again)
	}

	matches, _ := filepath.Glob(filepath.Join(dir, "vireo-mcp", "*.id"))
	if len(matches) != 2 {
		t.Fatalf("wrote %d identity files, want 2: %v", len(matches), matches)
	}
}

// The identity is stored locally, so it survives a restart.
func TestIdentityDefaultLabelIsStable(t *testing.T) {
	t.Setenv("VIREO_MCP_ID", "")
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("XDG_CONFIG_HOME", dir)

	first, err := LoadOrCreateID("default")
	if err != nil {
		t.Fatalf("LoadOrCreateID: %v", err)
	}
	second, err := LoadOrCreateID("default")
	if err != nil {
		t.Fatalf("LoadOrCreateID: %v", err)
	}
	if first != second {
		t.Fatalf("identity changed between runs: %q -> %q", first, second)
	}
}

// A stored identity that the daemon would reject is replaced, not reused.
func TestIdentityReplacesInvalidStoredValue(t *testing.T) {
	t.Setenv("VIREO_MCP_ID", "")
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("XDG_CONFIG_HOME", dir)

	base := filepath.Join(dir, "vireo-mcp")
	if err := os.MkdirAll(base, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(base, "default.id"), []byte("me\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	id, err := LoadOrCreateID("default")
	if err != nil {
		t.Fatalf("LoadOrCreateID: %v", err)
	}
	if err := protocol.ValidateClientID(id); err != nil {
		t.Fatalf("id %q is not in the shape the daemon accepts: %v", id, err)
	}
}

func TestIdentitySanitizesLabel(t *testing.T) {
	t.Setenv("VIREO_MCP_ID", "")
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("XDG_CONFIG_HOME", dir)

	if _, err := LoadOrCreateID("Claude Desktop / 2"); err != nil {
		t.Fatalf("LoadOrCreateID: %v", err)
	}
	matches, _ := filepath.Glob(filepath.Join(dir, "vireo-mcp", "*.id"))
	if len(matches) != 1 {
		t.Fatalf("wrote %d identity files, want 1: %v", len(matches), matches)
	}
	base := filepath.Base(matches[0])
	if strings.ContainsAny(base, " /") {
		t.Fatalf("label was not sanitized: %q", base)
	}
}

// An explicit override wins, for tests and odd deployments.
func TestIdentityExplicitOverride(t *testing.T) {
	fixed := "vireo_" + strings.Repeat("a", 32)
	t.Setenv("VIREO_MCP_ID", fixed)

	id, err := LoadOrCreateID("default")
	if err != nil {
		t.Fatalf("LoadOrCreateID: %v", err)
	}
	if id != fixed {
		t.Fatalf("id = %q, want the override %q", id, fixed)
	}
}
