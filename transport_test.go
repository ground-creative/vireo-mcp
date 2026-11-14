package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ground-creative/vireo-mcp/internal/dialer"
	"github.com/ground-creative/vireo-mcp/internal/protocol"
)

// harness is a server wired to a temp registry and a recording dialer factory,
// so tests can see which client ids were created without any daemon.
type harness struct {
	*server
	ids []string
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	h := &harness{}
	reg, err := openCallers(filepath.Join(t.TempDir(), "callers.json"))
	if err != nil {
		t.Fatalf("openCallers: %v", err)
	}
	h.server = &server{
		baseID: "vireo_base",
		name:   "test",
		newDialer: func(id string) *dialer.Dialer {
			h.ids = append(h.ids, id)
			return dialer.New(dialer.Options{ClientID: id, Timeout: 2 * time.Second})
		},
		instances: map[string]*instance{},
		callers:   reg,
	}
	h.d = h.newDialer("vireo_base")
	h.ids = nil
	return h
}

func (h *harness) url(t *testing.T) string {
	t.Helper()
	srv := httptest.NewServer(h.handler())
	t.Cleanup(srv.Close)
	return srv.URL
}

// post sends one JSON-RPC message with the given bearer token. An empty token
// means no Authorization header at all.
func post(t *testing.T, url, token, body string) (int, http.Header, string) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, url+"/mcp", strings.NewReader(body))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, resp.Header, string(b)
}

// issue mints a token in the harness registry, the way -issue-token does.
func (h *harness) issue(t *testing.T, label string) string {
	t.Helper()
	entry, err := h.callers.Issue(label)
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	return entry.Token
}

const initializeBody = `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"t","version":"1"}}}`

// ---- stdio ----

func TestStdioHandshake(t *testing.T) {
	h := newHarness(t)

	req := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"t","version":"1"}}}`
	resp := h.handleLine(h.d, []byte(req))
	if resp == "" {
		t.Fatal("initialize produced no response")
	}
	if !strings.Contains(resp, `"serverInfo"`) {
		t.Fatalf("response = %s", resp)
	}
}

func TestStdioBadJSON(t *testing.T) {
	h := newHarness(t)
	resp := h.handleLine(h.d, []byte("{not json"))
	if !strings.Contains(resp, "-32700") {
		t.Fatalf("response = %s", resp)
	}
}

func TestAboutReportsVersion(t *testing.T) {
	h := newHarness(t)
	resp := h.handleLine(h.d, []byte(`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"about","arguments":{}}}`))
	if !strings.Contains(resp, serverName) {
		t.Fatalf("response = %s", resp)
	}
}

// ---- http ----

// Without an Authorization header the server must not guess who the caller is.
func TestHTTPRequiresAuthorization(t *testing.T) {
	h := newHarness(t)
	url := h.url(t)

	code, _, body := post(t, url, "", `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)
	if code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401 (%s)", code, body)
	}
	if !strings.Contains(body, "missing authorization") {
		t.Fatalf("body = %s", body)
	}
}

// A provisioned token unlocks the server, and it is the only thing that does.
func TestHTTPAcceptsProvisionedToken(t *testing.T) {
	h := newHarness(t)
	url := h.url(t)
	token := h.issue(t, "cursor")

	code, _, body := post(t, url, token, `{"jsonrpc":"2.0","id":2,"method":"tools/list"}`)
	if code != http.StatusOK {
		t.Fatalf("status with token = %d (%s)", code, body)
	}
	if !strings.Contains(body, `"tools"`) {
		t.Fatalf("body = %s", body)
	}
	if !strings.Contains(body, `"send_message"`) {
		t.Fatalf("tool catalog does not offer send_message: %s", body)
	}
}

func TestHTTPRejectsUnknownToken(t *testing.T) {
	h := newHarness(t)
	url := h.url(t)

	code, _, body := post(t, url, "vmcp_00000000000000000000000000000000", `{"jsonrpc":"2.0","id":1,"method":"ping"}`)
	if code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401 (%s)", code, body)
	}
}

// A bare token without the "Bearer " prefix is accepted too.
func TestHTTPAcceptsBareToken(t *testing.T) {
	h := newHarness(t)
	url := h.url(t)
	token := h.issue(t, "cursor")

	req, _ := http.NewRequest(http.MethodPost, url+"/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"ping"}`))
	req.Header.Set("Authorization", token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
}

func TestHTTPNotificationIsAccepted(t *testing.T) {
	h := newHarness(t)
	url := h.url(t)
	token := h.issue(t, "cursor")

	code, _, _ := post(t, url, token, `{"jsonrpc":"2.0","method":"notifications/initialized"}`)
	if code != http.StatusAccepted {
		t.Fatalf("notification status = %d, want 202", code)
	}
}

// Two tokens are two callers: two client ids, two sets of conversations.
func TestHTTPCallersAreIsolated(t *testing.T) {
	h := newHarness(t)
	url := h.url(t)

	a := h.issue(t, "one")
	b := h.issue(t, "two")
	if a == b {
		t.Fatal("two callers got the same token")
	}

	post(t, url, a, `{"jsonrpc":"2.0","id":2,"method":"ping"}`)
	post(t, url, b, `{"jsonrpc":"2.0","id":3,"method":"ping"}`)

	if len(h.ids) != 2 {
		t.Fatalf("created %d instances, want one per caller: %v", len(h.ids), h.ids)
	}
	if h.ids[0] == h.ids[1] {
		t.Fatalf("callers share client id %q", h.ids[0])
	}
	for _, id := range h.ids {
		if err := protocol.ValidateClientID(id); err != nil {
			t.Errorf("client id %q is not in the shape the daemon accepts: %v", id, err)
		}
	}
}

// The same token must keep the same client id across requests, or the daemon
// would see a new caller every time.
func TestHTTPTokenKeepsOneClientID(t *testing.T) {
	h := newHarness(t)
	url := h.url(t)
	token := h.issue(t, "cursor")

	post(t, url, token, `{"jsonrpc":"2.0","id":2,"method":"ping"}`)
	post(t, url, token, `{"jsonrpc":"2.0","id":3,"method":"ping"}`)

	if len(h.ids) != 1 {
		t.Fatalf("created %d instances for one token, want 1: %v", len(h.ids), h.ids)
	}
}

func TestHTTPHealthAndRoutes(t *testing.T) {
	h := newHarness(t)
	url := h.url(t)

	for _, path := range []string{"/health", "/routes"} {
		resp, err := http.Get(url + path)
		if err != nil {
			t.Fatalf("GET %s: %v", path, err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Errorf("GET %s = %d", path, resp.StatusCode)
		}
		if !json.Valid(body) {
			t.Errorf("GET %s is not JSON: %s", path, body)
		}
	}
}
