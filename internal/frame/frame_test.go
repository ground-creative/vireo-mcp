package frame

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"testing"
	"testing/iotest"
)

func TestRoundTrip(t *testing.T) {
	var buf bytes.Buffer
	in := Frame{StreamID: 42, Type: 0x10, Payload: []byte(`{"protocol":"portico/1"}`)}
	if err := Write(&buf, in); err != nil {
		t.Fatalf("write: %v", err)
	}

	r := NewReader(&buf)
	got, err := r.Read()
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if got.StreamID != in.StreamID || got.Type != in.Type || !bytes.Equal(got.Payload, in.Payload) {
		t.Fatalf("got %+v want %+v", got, in)
	}
	if _, err := r.Read(); !errors.Is(err, io.EOF) {
		t.Fatalf("second read: want io.EOF, got %v", err)
	}
}

func TestSplitReads(t *testing.T) {
	var buf bytes.Buffer
	first := Frame{StreamID: 1, Type: 0x01, Payload: []byte("hello frame")}
	second := Frame{StreamID: 2, Type: 0x05, Payload: nil}
	if err := Write(&buf, first); err != nil {
		t.Fatalf("write first: %v", err)
	}
	if err := Write(&buf, second); err != nil {
		t.Fatalf("write second: %v", err)
	}

	r := NewReader(iotest.OneByteReader(&buf))
	got, err := r.Read()
	if err != nil || got.StreamID != first.StreamID || got.Type != first.Type || !bytes.Equal(got.Payload, first.Payload) {
		t.Fatalf("first frame: got %+v err %v", got, err)
	}
	got, err = r.Read()
	if err != nil || got.StreamID != second.StreamID || got.Type != second.Type || len(got.Payload) != 0 {
		t.Fatalf("second frame: got %+v err %v", got, err)
	}
}

func TestEmptyPayload(t *testing.T) {
	var buf bytes.Buffer
	if err := Write(&buf, Frame{StreamID: 7, Type: 0x03}); err != nil {
		t.Fatalf("write: %v", err)
	}
	r := NewReader(&buf)
	f, err := r.Read()
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if f.StreamID != 7 || f.Type != 0x03 || f.Payload != nil {
		t.Fatalf("got %+v", f)
	}
}

func TestTruncatedPayload(t *testing.T) {
	var buf bytes.Buffer
	_ = Write(&buf, Frame{StreamID: 1, Type: 0x01, Payload: []byte("0123456789")})
	buf.Truncate(buf.Len() - 4) // cut the payload mid-way

	r := NewReader(&buf)
	if _, err := r.Read(); err == nil {
		t.Fatal("truncated payload: want error, got nil")
	}
}

func TestTooLarge(t *testing.T) {
	var buf bytes.Buffer
	hdr := make([]byte, HeaderSize)
	binary.BigEndian.PutUint32(hdr[0:4], 1)
	hdr[4] = 0x01
	binary.BigEndian.PutUint32(hdr[5:9], uint32(MaxPayload)+1)
	buf.Write(hdr)

	r := NewReader(&buf)
	if _, err := r.Read(); !errors.Is(err, ErrFrameTooLarge) {
		t.Fatalf("want ErrFrameTooLarge, got %v", err)
	}
}
