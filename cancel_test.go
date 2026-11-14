package main

import (
	"encoding/json"
	"strings"
	"sync/atomic"
	"testing"
)

// A notifications/cancelled message has to reach the tool call it names: the
// host gave up, so the call stops waiting and the daemon is asked to abandon the
// turn. Nothing is written back — notifications get no response.
func TestCancelNotificationReachesTheInFlightCall(t *testing.T) {
	h := newHarness(t)
	var cancelled atomic.Bool
	h.inflight = map[string]func(){
		"7": func() { cancelled.Store(true) },
	}

	got := h.handleLine(h.d, []byte(`{"jsonrpc":"2.0","method":"notifications/cancelled","params":{"requestId":7,"reason":"user pressed stop"}}`))

	if got != "" {
		t.Errorf("cancel produced a response %q, want none", got)
	}
	if !cancelled.Load() {
		t.Error("the in-flight call was not cancelled")
	}
}

// A cancel that names a call which already finished is a no-op, not an error.
func TestCancelNotificationForAnUnknownRequestIsIgnored(t *testing.T) {
	h := newHarness(t)

	got := h.handleLine(h.d, []byte(`{"jsonrpc":"2.0","method":"notifications/cancelled","params":{"requestId":99}}`))

	if got != "" {
		t.Errorf("cancel produced a response %q, want none", got)
	}
}

// A cancel with no request id is malformed; it must not panic or answer.
func TestCancelNotificationWithoutAnIDIsIgnored(t *testing.T) {
	h := newHarness(t)
	called := false
	h.inflight = map[string]func(){"7": func() { called = true }}

	if got := h.handleLine(h.d, []byte(`{"jsonrpc":"2.0","method":"notifications/cancelled","params":{}}`)); got != "" {
		t.Errorf("cancel produced a response %q, want none", got)
	}
	if called {
		t.Error("a cancel without a request id cancelled a call")
	}
}

// Request ids arrive as rendered JSON, so the key used to register a call and
// the key used to find it have to agree on whatever spacing the host used.
func TestRequestKeyNormalisesRenderedIDs(t *testing.T) {
	cases := []struct{ a, b string }{
		{`7`, `7`},
		{`"abc"`, `"abc"`},
		{` 7 `, `7`},
	}
	for _, c := range cases {
		ka := requestKey(json.RawMessage(c.a))
		kb := requestKey(json.RawMessage(c.b))
		if ka != kb {
			t.Errorf("requestKey(%q) = %q, requestKey(%q) = %q; want them equal", c.a, ka, c.b, kb)
		}
	}
}

// Only tools/call can run for the length of a turn, so only that is dispatched
// concurrently. Everything else stays inline and ordered.
func TestOnlyToolCallsAreDispatchedConcurrently(t *testing.T) {
	if !isToolCall([]byte(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{}}`)) {
		t.Error("tools/call is not recognised as a long-running call")
	}
	for _, line := range []string{
		`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`,
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`,
		`{"jsonrpc":"2.0","method":"notifications/cancelled","params":{}}`,
		`not json`,
	} {
		if isToolCall([]byte(line)) {
			t.Errorf("%s was treated as a tool call", strings.TrimSpace(line))
		}
	}
}

// The cancellation code is the one MCP defines for an abandoned request, so a
// host can tell "the user stopped this" apart from a real failure.
func TestCancelledCodeIsTheStandardOne(t *testing.T) {
	if codeCancelled != -32800 {
		t.Errorf("codeCancelled = %d, want -32800", codeCancelled)
	}
}
