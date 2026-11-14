package client

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"net"
	"testing"
	"time"

	"github.com/ground-creative/vireo-mcp/internal/frame"
	"github.com/ground-creative/vireo-mcp/internal/protocol"
)

// silentDaemon negotiates, then swallows every message without answering — the
// slow-model case. It reports the message it received and any cancel frame, and
// can hang up the moment it has a message, to simulate a broken read after a
// successful write.
type silentDaemon struct {
	addr    string
	arrived chan struct{}
	cancel  chan uint32
}

func newSilentDaemon(t *testing.T, hangUpAfterMessage bool) *silentDaemon {
	t.Helper()
	d := &silentDaemon{
		arrived: make(chan struct{}, 1),
		cancel:  make(chan uint32, 1),
	}
	cert := selfSigned(t)
	ln, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{
		Certificates: []tls.Certificate{cert},
		NextProtos:   []string{protocol.Version},
		MinVersion:   tls.VersionTLS13,
	})
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { ln.Close() })
	d.addr = ln.Addr().String()

	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go d.handle(conn, hangUpAfterMessage)
		}
	}()
	return d
}

// handle serves one connection: negotiate, then swallow messages and report
// whatever the client sends.
func (d *silentDaemon) handle(conn net.Conn, hangUpAfterMessage bool) {
	defer conn.Close()
	rd := frame.NewReader(conn)

	if _, err := rd.Read(); err != nil {
		return
	}
	manifest, _ := json.Marshal(protocol.Manifest{
		Protocol: protocol.Version,
		Agents:   []protocol.AgentEntry{{Name: "concierge", Sessions: true}},
	})
	if err := frame.Write(conn, frame.Frame{StreamID: 0, Type: protocol.TypeManifest, Payload: manifest}); err != nil {
		return
	}

	for {
		f, err := rd.Read()
		if err != nil {
			return
		}
		switch f.Type {
		case protocol.TypeMessage:
			select {
			case d.arrived <- struct{}{}:
			default:
			}
			if hangUpAfterMessage {
				return
			}
		case protocol.TypeCancel:
			select {
			case d.cancel <- f.StreamID:
			default:
			}
		}
	}
}

func connect(t *testing.T, addr string, timeout time.Duration) *Client {
	t.Helper()
	c, err := Connect(context.Background(), Options{
		Addr:     addr,
		ClientID: "vireo_0123456789abcdef0123456789abcdef",
		Name:     "vireo-mcp",
		Insecure: true,
		Timeout:  timeout,
	})
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

// Cancelling a turn stops the wait at once and tells the daemon which turn to
// abandon. Without the interrupt the call would sit in Read until the request
// timeout, which is exactly the "cancel does nothing" behaviour this fixes.
func TestCancelCurrentInterruptsTheWaitAndTellsTheDaemon(t *testing.T) {
	d := newSilentDaemon(t, false)
	// A long timeout so only the cancellation can end the call early.
	c := connect(t, d.addr, 30*time.Second)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	errc := make(chan error, 1)
	go func() {
		_, err := c.SendMessage(ctx, "concierge", "s-1", "build me an assistant")
		errc <- err
	}()

	select {
	case <-d.arrived:
	case <-time.After(3 * time.Second):
		t.Fatal("the daemon never received the message")
	}

	if err := c.CancelCurrent(); err != nil {
		t.Fatalf("CancelCurrent: %v", err)
	}
	select {
	case stream := <-d.cancel:
		if stream != turnStream {
			t.Errorf("cancel frame names stream %d, want %d", stream, turnStream)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("the daemon never received a cancel frame")
	}

	// The host cancelled: the same thing handleToolsCall's cancel func does.
	cancel()

	select {
	case err := <-errc:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("SendMessage error = %v, want context.Canceled", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("SendMessage did not return after the turn was cancelled")
	}
}

// A request the daemon has already seen is never re-sent. Retrying a turn whose
// read failed would deliver the same message twice, which is how a conversation
// gained a turn nobody sent.
func TestWrittenRequestIsNotRetried(t *testing.T) {
	d := newSilentDaemon(t, true) // hangs up as soon as the message arrives
	c := connect(t, d.addr, 5*time.Second)
	before := c.Dials()

	if _, err := c.SendMessage(context.Background(), "concierge", "s-1", "hello"); err == nil {
		t.Fatal("SendMessage succeeded against a daemon that hung up")
	}
	if got := c.Dials(); got != before {
		t.Errorf("the client dialled %d times, want %d: a written request was retried", got, before)
	}
}

// A cancelled turn leaves the connection unusable: a daemon that does not know
// the cancel frame answers it with an error on the same stream, and the next call
// would read that instead of its own answer. The connection is dropped, so the
// following call starts clean.
func TestCancelledCallDiscardsTheConnection(t *testing.T) {
	d := newSilentDaemon(t, false)
	c := connect(t, d.addr, 30*time.Second)
	before := c.Dials()

	ctx, cancel := context.WithCancel(context.Background())
	errc := make(chan error, 1)
	go func() {
		_, err := c.SendMessage(ctx, "concierge", "s-1", "hi")
		errc <- err
	}()
	select {
	case <-d.arrived:
	case <-time.After(3 * time.Second):
		t.Fatal("the daemon never received the message")
	}
	_ = c.CancelCurrent()
	cancel()
	select {
	case <-errc:
	case <-time.After(3 * time.Second):
		t.Fatal("SendMessage did not return after the cancellation")
	}

	// The next call has to dial again: the stream may hold a stale frame.
	ctx2, cancel2 := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel2()
	go func() {
		_, err := c.SendMessage(ctx2, "concierge", "s-1", "again")
		errc <- err
	}()
	select {
	case <-d.arrived:
	case <-time.After(3 * time.Second):
		t.Fatal("the daemon never received the second message")
	}
	if got := c.Dials(); got != before+1 {
		t.Errorf("dials = %d, want %d: the cancelled connection was reused", got, before+1)
	}
}
