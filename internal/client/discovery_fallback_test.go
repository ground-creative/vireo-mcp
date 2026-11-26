package client

// discovery_fallback_test.go covers PROTO-48: a client may fall back to the
// retired _vireo._tcp record when _portico._tcp does not resolve, and must
// treat both as the same service.
//
// This was not hypothetical. The live deployment at ziwa.ai published only the
// retired record for a week after the specification renamed it, so a client
// following the current text could not reach a daemon that was working fine —
// including our own adapter reaching our own endpoint. The fallback is what
// keeps a rename from becoming a flag day.

import (
	"net"
	"strings"
	"testing"
)

func srvRecord(target string, port uint16) *net.SRV {
	return &net.SRV{Target: target, Port: port, Priority: 10, Weight: 5}
}

// The published record is used when it resolves, and the retired one is not
// consulted even though it is also present.
func TestDiscoverPrefersThePublishedRecord(t *testing.T) {
	res := &fakeResolver{byService: map[string][]*net.SRV{
		SRVService:       {srvRecord("new.example.", 7778)},
		SRVServiceLegacy: {srvRecord("old.example.", 9999)},
	}}
	addr, via, err := DiscoverDetail("example.com", res)
	if err != nil {
		t.Fatalf("DiscoverDetail: %v", err)
	}
	if addr != "new.example:7778" {
		t.Errorf("addr = %q, want new.example:7778", addr)
	}
	if via != ViaSRV {
		t.Errorf("via = %q, want %q", via, ViaSRV)
	}
}

// A deployment that has not migrated must still be reachable, and the caller
// must be able to see that the retired record answered.
func TestDiscoverFallsBackToTheRetiredRecord(t *testing.T) {
	res := &fakeResolver{byService: map[string][]*net.SRV{
		SRVServiceLegacy: {srvRecord("old.example.", 7778)},
	}}
	addr, via, err := DiscoverDetail("example.com", res)
	if err != nil {
		t.Fatalf("DiscoverDetail: %v", err)
	}
	if addr != "old.example:7778" {
		t.Errorf("addr = %q, want old.example:7778", addr)
	}
	if via != ViaSRVLegacy {
		t.Errorf("via = %q, want %q so the caller can report an unmigrated deployment", via, ViaSRVLegacy)
	}
}

// With neither record the answer is an error naming both, not a guessed port.
func TestDiscoverWithoutAnyRecordNamesBothRecords(t *testing.T) {
	_, _, err := DiscoverDetail("example.com", &fakeResolver{})
	if err == nil {
		t.Fatal("expected an error when neither record resolves")
	}
	for _, want := range []string{"_portico._tcp", "_vireo._tcp"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error does not mention %s: %v", want, err)
		}
	}
}
