package dialer

import (
	"strings"
	"testing"
)

// A conversation with a greeting opens with the agent's own words.
func TestConversationOpenedShowsTheGreeting(t *testing.T) {
	text := conversationOpened("concierge", "Hello there")
	if !strings.Contains(text, "concierge") {
		t.Errorf("text %q does not name the agent", text)
	}
	if !strings.Contains(text, "Hello there") {
		t.Errorf("text %q does not carry the greeting", text)
	}
}

// Without a greeting the caller is told what to do next.
func TestConversationOpenedWithoutGreeting(t *testing.T) {
	text := conversationOpened("concierge", "")
	if strings.Contains(text, "Hello there") {
		t.Errorf("text %q carries a greeting it does not have", text)
	}
	if !strings.Contains(text, "Send a message") {
		t.Errorf("text %q does not say what to do next", text)
	}
}
