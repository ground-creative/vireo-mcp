// Package frame implements the vireo wire framing: a fixed 9-byte header
// followed by an opaque payload.
//
//	0               4       5               9
//	+---------------+-------+---------------+
//	|   stream id   | type  |    length     |  payload...
//	+---------------+-------+---------------+
//	       uint32      uint8      uint32
//
// All integers are big-endian. A reader consumes exactly one frame per call
// regardless of how the underlying transport splits the bytes; a writer loops
// until the whole frame is out.
package frame

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
)

const (
	HeaderSize = 9
	MaxPayload = 8 << 20
)

var ErrFrameTooLarge = errors.New("frame: payload exceeds limit")

type Frame struct {
	StreamID uint32
	Type     uint8
	Payload  []byte
}

type Reader struct {
	src io.Reader
	hdr [HeaderSize]byte
}

func NewReader(r io.Reader) *Reader { return &Reader{src: r} }

// Read returns the next complete frame. It returns io.EOF only at a clean
// frame boundary; a payload cut short mid-frame is an error, not EOF.
func (r *Reader) Read() (Frame, error) {
	if _, err := io.ReadFull(r.src, r.hdr[:]); err != nil {
		return Frame{}, err
	}
	f := Frame{
		StreamID: binary.BigEndian.Uint32(r.hdr[0:4]),
		Type:     r.hdr[4],
	}
	n := binary.BigEndian.Uint32(r.hdr[5:9])
	if n > MaxPayload {
		return Frame{}, ErrFrameTooLarge
	}
	if n == 0 {
		return f, nil
	}
	f.Payload = make([]byte, n)
	if _, err := io.ReadFull(r.src, f.Payload); err != nil {
		return Frame{}, fmt.Errorf("frame: truncated payload: %w", err)
	}
	return f, nil
}

// Write emits one complete frame, retrying on short writes.
func Write(w io.Writer, f Frame) error {
	if len(f.Payload) > MaxPayload {
		return ErrFrameTooLarge
	}
	buf := make([]byte, HeaderSize+len(f.Payload))
	binary.BigEndian.PutUint32(buf[0:4], f.StreamID)
	buf[4] = f.Type
	binary.BigEndian.PutUint32(buf[5:9], uint32(len(f.Payload)))
	copy(buf[HeaderSize:], f.Payload)
	for len(buf) > 0 {
		n, err := w.Write(buf)
		if err != nil {
			return err
		}
		buf = buf[n:]
	}
	return nil
}
