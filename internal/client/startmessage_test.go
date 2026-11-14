package client

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"math/big"
	"net"
	"testing"
	"time"

	"github.com/ground-creative/vireo-mcp/internal/frame"
	"github.com/ground-creative/vireo-mcp/internal/protocol"
)

// selfSigned makes a throwaway certificate for the fake daemon.
func selfSigned(t *testing.T) tls.Certificate {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "localhost"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		IPAddresses:           []net.IP{net.ParseIP("127.0.0.1")},
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("create certificate: %v", err)
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
}

// fakeDaemon answers a negotiate and one session request, the way a vireo
// daemon does.
func fakeDaemon(t *testing.T, greeting string) string {
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

		// The handshake: a manifest answers the negotiate frame.
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

		// The session request: a data frame with the greeting, then an end.
		f, err := rd.Read()
		if err != nil {
			return
		}
		answer, _ := json.Marshal(protocol.NewSessionResponse{
			Session:      protocol.SessionInfo{ID: "s-1", Agent: "concierge", Status: protocol.StatusOpen},
			StartMessage: greeting,
		})
		if err := frame.Write(conn, frame.Frame{StreamID: f.StreamID, Type: protocol.TypeData, Payload: answer}); err != nil {
			return
		}
		end, _ := json.Marshal(protocol.EndPayload{OK: true})
		_ = frame.Write(conn, frame.Frame{StreamID: f.StreamID, Type: protocol.TypeEnd, Payload: end})
	}()

	return ln.Addr().String()
}

// A conversation starts with the agent's greeting, taken from the session.new
// answer.
func TestNewSessionReturnsTheStartMessage(t *testing.T) {
	addr := fakeDaemon(t, "Hello there")

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

	started, err := c.NewSession(context.Background(), "concierge", "")
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	if started.StartMessage != "Hello there" {
		t.Errorf("start message = %q, want %q", started.StartMessage, "Hello there")
	}
	if started.Session.ID != "s-1" {
		t.Errorf("session id = %q, want %q", started.Session.ID, "s-1")
	}
}

// An agent without a greeting starts the conversation with nothing.
func TestNewSessionWithoutStartMessage(t *testing.T) {
	addr := fakeDaemon(t, "")

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

	started, err := c.NewSession(context.Background(), "concierge", "")
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	if started.StartMessage != "" {
		t.Errorf("start message = %q, want it empty", started.StartMessage)
	}
}
