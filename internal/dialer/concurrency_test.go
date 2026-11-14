package dialer

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"
)

// A host may issue several tool calls at once. They are serialised, because the
// conversation state is plain maps: concurrent bodies would race on them.
func TestConcurrentToolCallsAreSerialised(t *testing.T) {
	d := New(Options{ClientID: "vireo_0123456789abcdef0123456789abcdef"})

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, ok := d.Handle(context.Background(), "send_message", json.RawMessage(`{"message":"hi"}`)); ok {
				t.Error("send_message succeeded without a connection")
			}
		}()
	}
	wg.Wait()
}

// Cancelling must not wait for the tool-call lock: the call being cancelled is
// the one holding it, so taking the lock there would deadlock the very case this
// exists for.
func TestCancelDoesNotWaitForTheToolCallLock(t *testing.T) {
	d := New(Options{ClientID: "vireo_0123456789abcdef0123456789abcdef"})

	d.mu.Lock() // stand in for a tool call in progress
	defer d.mu.Unlock()

	done := make(chan struct{})
	go func() {
		d.Cancel()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Cancel blocked on the tool-call lock")
	}
}
