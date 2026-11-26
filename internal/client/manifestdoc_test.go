package client

// manifestdoc_test.go covers MAN-59: an adapter must hand its model the manifest
// as the endpoint published it, not a re-encoding of its own struct.
//
// The distinction is not academic. Decoding a document into a Go struct and
// marshalling it back inserts members the struct always emits (an empty
// "session": {}), drops members the struct does not model, and reorders object
// members. A model shown that output is reading a document the business never
// wrote, and a digest computed over it will not match the one the business
// published. This test pins the client to the bytes on the wire.

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/ground-creative/vireo-mcp/internal/frame"
	"github.com/ground-creative/vireo-mcp/internal/protocol"
)

// manifestDaemon serves one manifest, verbatim, and nothing else.
func manifestDaemon(t *testing.T, document []byte) string {
	t.Helper()
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

	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		rd := frame.NewReader(conn)
		if _, err := rd.Read(); err != nil {
			return
		}
		_ = frame.Write(conn, frame.Frame{
			StreamID: 0,
			Type:     protocol.TypeManifest,
			Payload:  document,
		})
	}()
	return ln.Addr().String()
}

// An unknown member survives: a document carrying one is passed through whole,
// because the model must see what the business published, not what this adapter
// happens to model.
func TestManifestDocumentIsThePublishedBytes(t *testing.T) {
	// Written with unusual member order and an extra member no struct declares.
	document := []byte(`{"protocol":"portico/1","endpoint":{"name":"test"},` +
		`"agents":[{"name":"a","sessions":false,"future_field":42}],` +
		`"manifest_version":"1.3"}`)

	addr := manifestDaemon(t, document)
	c, err := Connect(context.Background(), Options{
		Addr:     addr,
		ClientID: "vireo_0123456789abcdef0123456789abcdef",
		Name:     "vireo-mcp",
		Insecure: true,
		Timeout:  5 * time.Second,
	})
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	defer c.Close()

	raw, _, _, err := c.ManifestDocument()
	if err != nil {
		t.Fatalf("ManifestDocument: %v", err)
	}

	if string(raw) != string(document) {
		t.Errorf("document was altered\n got: %s\nwant: %s", raw, document)
	}
	// The specific regressions the re-encoding introduced.
	if strings.Contains(string(raw), `"session":{}`) {
		t.Error("document gained a session member the publisher never wrote")
	}
	if !strings.Contains(string(raw), "future_field") {
		t.Error("document lost a member this adapter does not model")
	}
}

// A digest over the received bytes must match what the publisher computed, which
// is only possible if the bytes are the published ones.
func TestManifestDocumentDigestVerifies(t *testing.T) {
	document := []byte(`{"manifest_version":"1.3","protocol":"portico/1",` +
		`"endpoint":{"name":"Café 日本"},` +
		`"agents":[{"name":"a","sessions":false}]}`)

	computed, _, err := protocol.VerifyDigest(document)
	if err != nil {
		t.Fatalf("VerifyDigest: %v", err)
	}
	var doc map[string]any
	if err := json.Unmarshal(document, &doc); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	doc["digest"] = computed
	withDigest, err := json.Marshal(doc)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	addr := manifestDaemon(t, withDigest)
	c, err := Connect(context.Background(), Options{
		Addr:     addr,
		ClientID: "vireo_0123456789abcdef0123456789abcdef",
		Name:     "vireo-mcp",
		Insecure: true,
		Timeout:  5 * time.Second,
	})
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	defer c.Close()

	_, got, ok, err := c.ManifestDocument()
	if err != nil {
		t.Fatalf("ManifestDocument: %v", err)
	}
	if !ok {
		t.Errorf("digest did not verify; computed %s (a non-ASCII description must not change the answer)", got)
	}
}
