package sidecar

// keeper_approval_bridge_test.go — #2574, the sidecar half.
//
// The agent-facing surface of consumable approvals: the approval reference
// (body field or X-Keeper-Approval header) must travel to the server on both
// keeper routes, and the GET poll — the only way an agent can learn what a
// person decided — must forward with the ACTING agent pinned so a sibling
// cannot read a peer's escalation.

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// captureCrewshipd records the last internal request it served.
type captureCrewshipd struct {
	server *httptest.Server
	method string
	path   string
	query  string
	body   map[string]string
	status int
	resp   map[string]any
}

func newCaptureCrewshipd(t *testing.T, status int, resp map[string]any) *captureCrewshipd {
	t.Helper()
	c := &captureCrewshipd{status: status, resp: resp}
	c.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c.method = r.Method
		c.path = r.URL.Path
		c.query = r.URL.RawQuery
		c.body = map[string]string{}
		if r.Body != nil {
			raw, _ := io.ReadAll(r.Body)
			_ = json.Unmarshal(raw, &c.body)
		}
		if r.Header.Get("X-Internal-Token") != "test-token" {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(resp)
	}))
	t.Cleanup(c.server.Close)
	return c
}

func keeperIPC(url string) *IPCConfig {
	return &IPCConfig{
		BaseURL:     url,
		Token:       "test-token",
		AgentID:     "boot-agent",
		AgentSlug:   "boot-agent",
		CrewID:      "crew1",
		WorkspaceID: "ws1",
		ContainerID: "container1",
	}
}

// The body field travels on the access route.
func TestKeeperBridge_ForwardsApprovalRequestID(t *testing.T) {
	c := newCaptureCrewshipd(t, http.StatusOK, map[string]any{"decision": "ALLOW"})
	srv := newKeeperServer(t, keeperIPC(c.server.URL))

	body := strings.NewReader(`{"credential_id":"cred1","intent":"rotate the certs","approval_request_id":"kr_abc123"}`)
	req := httptest.NewRequest(http.MethodPost, "/keeper/request", body)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.handleKeeperRequest(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("got %d: %s", w.Code, w.Body.String())
	}
	if got := c.body["approval_request_id"]; got != "kr_abc123" {
		t.Fatalf("approval_request_id = %q on the forwarded body, want kr_abc123", got)
	}
}

// The header travels on the execute route — a client library that cannot add
// body fields can still present an approval.
func TestKeeperBridge_ForwardsApprovalHeader(t *testing.T) {
	c := newCaptureCrewshipd(t, http.StatusOK, map[string]any{"decision": "ALLOW"})
	srv := newKeeperServer(t, keeperIPC(c.server.URL))

	body := strings.NewReader(`{"credential_id":"cred1","intent":"rotate the certs","command":"pg_dump --all"}`)
	req := httptest.NewRequest(http.MethodPost, "/keeper/execute", body)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Keeper-Approval", "kr_def456")
	w := httptest.NewRecorder()
	srv.handleKeeperExecute(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("got %d: %s", w.Code, w.Body.String())
	}
	if got := c.body["approval_request_id"]; got != "kr_def456" {
		t.Fatalf("header approval forwarded as %q, want kr_def456", got)
	}
}

// The approval reference is an id, not arbitrary input: anything outside the
// id character class is refused before it reaches a URL or a payload.
func TestKeeperBridge_RejectsMalformedApprovalReference(t *testing.T) {
	c := newCaptureCrewshipd(t, http.StatusOK, map[string]any{"decision": "ALLOW"})
	srv := newKeeperServer(t, keeperIPC(c.server.URL))

	req := httptest.NewRequest(http.MethodPost, "/keeper/request",
		strings.NewReader(`{"credential_id":"cred1","intent":"rotate the certs","approval_request_id":"../../secrets"}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.handleKeeperRequest(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("a traversal-shaped approval reference got %d, want 400: %s", w.Code, w.Body.String())
	}
	if c.path != "" {
		t.Fatalf("the malformed reference was forwarded (%s %s)", c.method, c.path)
	}
}

// The poll: GET /keeper/request/{id} forwards to the internal status route
// with the ACTING agent pinned — the server 404s rows belonging to anybody
// else, which is what makes an agent-facing poll safe.
func TestKeeperBridge_PollForwardsWithActingAgent(t *testing.T) {
	c := newCaptureCrewshipd(t, http.StatusOK, map[string]any{
		"id": "kr_abc123", "decision": "ALLOW", "request_type": "access",
	})
	srv := newKeeperServer(t, keeperIPC(c.server.URL))

	req := httptest.NewRequest(http.MethodGet, "/keeper/request/kr_abc123", nil)
	w := httptest.NewRecorder()
	srv.handleKeeperRequestStatus(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("got %d: %s", w.Code, w.Body.String())
	}
	if c.method != http.MethodGet || c.path != "/api/v1/internal/keeper/request/kr_abc123" {
		t.Fatalf("forwarded %s %s, want GET /api/v1/internal/keeper/request/kr_abc123", c.method, c.path)
	}
	if c.query != "agent_id=boot-agent" {
		t.Fatalf("query = %q, want agent_id=boot-agent — the acting agent must be pinned", c.query)
	}
	var body map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("response passthrough broken: %v", err)
	}
	if body["decision"] != "ALLOW" {
		t.Fatalf("decision = %v, want ALLOW", body["decision"])
	}
}

func TestKeeperBridge_PollRejectsTraversalShapedID(t *testing.T) {
	c := newCaptureCrewshipd(t, http.StatusOK, nil)
	srv := newKeeperServer(t, keeperIPC(c.server.URL))

	req := httptest.NewRequest(http.MethodGet, "/keeper/request/..%2Fsecrets", nil)
	w := httptest.NewRecorder()
	srv.handleKeeperRequestStatus(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("got %d, want 400", w.Code)
	}
	if c.path != "" {
		t.Fatalf("the malformed id was forwarded (%s %s)", c.method, c.path)
	}
}
