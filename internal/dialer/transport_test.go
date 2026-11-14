package dialer

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func testDialer() *Dialer {
	return New(Options{
		ClientID: "vireo_0123456789abcdef0123456789abcdef",
		Name:     "test",
		Timeout:  time.Second,
	})
}

// The catalogue must stay in step with the dispatcher: every advertised tool is
// callable, and every callable tool is advertised. A tool in only one of the two
// lists is either invisible to a host or dead on arrival.
func TestToolsAndHandleAgree(t *testing.T) {
	d := testDialer()
	advertised := map[string]bool{}
	for _, tool := range d.Tools() {
		name, _ := tool["name"].(string)
		if name == "" {
			t.Fatalf("a tool has no name: %v", tool)
		}
		advertised[name] = true
		if _, ok := tool["inputSchema"]; !ok {
			t.Errorf("tool %q has no inputSchema", name)
		}
	}

	for _, want := range []string{
		"discover", "connect", "conversations", "new_conversation",
		"resume_conversation", "send_message", "close_conversation",
	} {
		if !advertised[want] {
			t.Errorf("tool %q is missing from the catalogue", want)
		}
	}

	text, ok := d.Handle(context.Background(), "no-such-tool", json.RawMessage(`{}`))
	if ok || !strings.Contains(text, "no-such-tool") {
		t.Errorf("Handle(unknown) = (%q, %t), want a complaint about the name", text, ok)
	}
}

// Tools that need a daemon say so instead of hanging or panicking before the
// host has connected.
func TestToolsThatNeedAConnectionSaySo(t *testing.T) {
	d := testDialer()
	cases := map[string]string{
		"conversations":       `{"agent":"orders"}`,
		"new_conversation":    `{"agent":"orders"}`,
		"resume_conversation": `{"agent":"orders","latest":true}`,
		"send_message":        `{"message":"hi"}`,
		"close_conversation":  `{"agent":"orders"}`,
	}
	for tool, args := range cases {
		text, ok := d.Handle(context.Background(), tool, json.RawMessage(args))
		if ok {
			t.Errorf("%s: ok = true, want a failure", tool)
		}
		if !strings.Contains(text, "connect") {
			t.Errorf("%s: %q, want it to mention connect", tool, text)
		}
	}
}

// Arguments are validated before any network work: a malformed call gets an
// actionable message, not a dial attempt.
func TestToolsValidateTheirArguments(t *testing.T) {
	d := testDialer()
	for _, tool := range []string{"discover", "connect", "send_message"} {
		text, ok := d.Handle(context.Background(), tool, json.RawMessage(`{}`))
		if ok {
			t.Errorf("%s: ok = true, want a failure for missing arguments", tool)
		}
		if text == "" {
			t.Errorf("%s: empty message", tool)
		}
	}
}
