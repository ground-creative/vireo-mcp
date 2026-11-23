package client

import (
	"net"
	"strings"
	"testing"
)

// fakeResolver answers SRV lookups from a table.
type fakeResolver struct {
	records map[string][]*net.SRV
	err     error
}

func (f *fakeResolver) LookupSRV(service, proto, name string) (string, []*net.SRV, error) {
	if f.err != nil {
		return "", nil, f.err
	}
	return "", f.records[name], nil
}

// An address with a port is taken as given: no lookup, no guessing.
func TestDiscoverExplicitAddress(t *testing.T) {
	addr, via, err := DiscoverDetail("daemon.example.com:9999", &fakeResolver{})
	if err != nil {
		t.Fatalf("DiscoverDetail: %v", err)
	}
	if addr != "daemon.example.com:9999" {
		t.Fatalf("addr = %q", addr)
	}
	if via != ViaExplicit {
		t.Fatalf("via = %q", via)
	}
}

// A published record is how a domain resolves. The port comes from the record.
func TestDiscoverUsesSRVRecord(t *testing.T) {
	res := &fakeResolver{records: map[string][]*net.SRV{
		"daemon.example.com": {{Target: "agent.example.com.", Port: 7777, Priority: 1, Weight: 1}},
	}}
	addr, via, err := DiscoverDetail("daemon.example.com", res)
	if err != nil {
		t.Fatalf("DiscoverDetail: %v", err)
	}
	if addr != "agent.example.com:7777" {
		t.Fatalf("addr = %q", addr)
	}
	if via != ViaSRV {
		t.Fatalf("via = %q", via)
	}
}

// The lowest priority value wins.
func TestDiscoverPrefersLowestPriority(t *testing.T) {
	res := &fakeResolver{records: map[string][]*net.SRV{
		"daemon.example.com": {
			{Target: "backup.example.com.", Port: 7777, Priority: 10, Weight: 1},
			{Target: "agent.example.com.", Port: 7777, Priority: 1, Weight: 1},
		},
	}}
	addr, _, err := DiscoverDetail("daemon.example.com", res)
	if err != nil {
		t.Fatalf("DiscoverDetail: %v", err)
	}
	if addr != "agent.example.com:7777" {
		t.Fatalf("addr = %q, want agent.example.com:7777", addr)
	}
}

// No record means no daemon: the port is never guessed.
func TestDiscoverWithoutRecordFails(t *testing.T) {
	_, _, err := DiscoverDetail("daemon.example.com", &fakeResolver{})
	if err == nil {
		t.Fatal("a domain without an SRV record must not resolve")
	}
	if !strings.Contains(err.Error(), "_frontdoor._tcp.daemon.example.com") {
		t.Fatalf("error does not name the record: %v", err)
	}
	if !strings.Contains(err.Error(), "host:port") {
		t.Fatalf("error does not say how to connect anyway: %v", err)
	}
}

// A bracketed IPv6 literal without a port still needs a record.
func TestDiscoverIPv6LiteralNeedsRecord(t *testing.T) {
	if _, _, err := DiscoverDetail("[::1]", &fakeResolver{}); err == nil {
		t.Fatal("an IPv6 literal without a port must not resolve without a record")
	}
}
