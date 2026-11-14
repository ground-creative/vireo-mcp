package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

// A tool call that reports progress writes notifications/progress lines, so a
// host can show "hold on" while the call is still running. Progress numbers are
// per call and start at 1.
func TestProgressNotificationReachesTheHost(t *testing.T) {
	var out bytes.Buffer
	s := &server{progressOut: &out}
	s.beginProgress(json.RawMessage(`"tok-1"`))
	s.notifyProgress("fetching your site…")
	s.notifyProgress("still working…")
	s.endProgress()

	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("wrote %d notifications, want 2: %q", len(lines), out.String())
	}

	var msg struct {
		JSONRPC string `json:"jsonrpc"`
		Method  string `json:"method"`
		Params  struct {
			Token    string `json:"progressToken"`
			Progress int    `json:"progress"`
			Message  string `json:"message"`
		} `json:"params"`
	}
	if err := json.Unmarshal([]byte(lines[0]), &msg); err != nil {
		t.Fatalf("decode notification: %v", err)
	}
	if msg.JSONRPC != "2.0" || msg.Method != "notifications/progress" {
		t.Errorf("envelope = %q/%q, want 2.0/notifications/progress", msg.JSONRPC, msg.Method)
	}
	if msg.Params.Token != "tok-1" {
		t.Errorf("progressToken = %q, want %q", msg.Params.Token, "tok-1")
	}
	if msg.Params.Progress != 1 {
		t.Errorf("progress = %d, want 1", msg.Params.Progress)
	}
	if msg.Params.Message != "fetching your site…" {
		t.Errorf("message = %q, want %q", msg.Params.Message, "fetching your site…")
	}
}

// Without a token the host did not ask for progress: stay silent rather than
// writing notifications it cannot attribute.
func TestNoProgressWithoutAToken(t *testing.T) {
	var out bytes.Buffer
	s := &server{progressOut: &out}
	s.beginProgress(nil)
	s.notifyProgress("nobody asked for this")
	s.endProgress()

	if out.Len() != 0 {
		t.Errorf("wrote %q, want nothing", out.String())
	}
}

// The HTTP transport has no stream to notify on, so progress is dropped instead
// of being written to a response that is not being read.
func TestProgressIsSilentWithoutAnOutput(t *testing.T) {
	s := &server{}
	s.beginProgress(json.RawMessage(`7`))
	s.notifyProgress("ignored")
	s.endProgress()
}
