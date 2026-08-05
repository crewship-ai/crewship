package sidecar

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// newAssignmentServer creates a Server configured for assignment testing with a mock crewshipd.
//
// A fixture that leaves IPCConfig.AgentID empty gets a boot identity here.
// /assign attributes every dispatch to an acting agent (#1754) and fails closed
// when it can resolve none — a sidecar with no boot identity and no per-agent
// tokens can attribute nothing, which is #1059's rule, not a quirk of this
// route. Production always mints AgentID (orchestrator_run.go's ipcCfg). The
// tests below are about target/crew validation; identity has its own file,
// assignment_identity_test.go.
func newAssignmentServer(t *testing.T, ipc *IPCConfig, members []CrewMember) *Server {
	t.Helper()
	if ipc != nil && ipc.AgentID == "" {
		ipc.AgentID = "boot-agent"
		ipc.AgentSlug = "boot"
	}
	return NewServer(ServerConfig{
		Addr:        "127.0.0.1:0",
		Logger:      slog.Default(),
		IPC:         ipc,
		CrewMembers: members,
	})
}

func TestHandleAssign_NoIPC(t *testing.T) {
	srv := newAssignmentServer(t, nil, nil)

	req := httptest.NewRequest(http.MethodPost, "/assign", strings.NewReader(`{"target":"viktor","task":"write tests"}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	srv.handleAssign(w, req)

	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("expected 503, got %d", w.Code)
	}
}

func TestHandleAssign_InvalidJSON(t *testing.T) {
	srv := newAssignmentServer(t, &IPCConfig{BaseURL: "http://x", Token: "tok"}, nil)

	req := httptest.NewRequest(http.MethodPost, "/assign", strings.NewReader(`not-json`))
	w := httptest.NewRecorder()

	srv.handleAssign(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", w.Code)
	}
}

func TestHandleAssign_MissingFields(t *testing.T) {
	srv := newAssignmentServer(t, &IPCConfig{BaseURL: "http://x", Token: "tok"}, nil)

	req := httptest.NewRequest(http.MethodPost, "/assign", strings.NewReader(`{"target":"viktor"}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	srv.handleAssign(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", w.Code)
	}
}

func TestHandleAssign_UnknownTarget(t *testing.T) {
	srv := newAssignmentServer(t, &IPCConfig{BaseURL: "http://x", Token: "tok"}, []CrewMember{
		{Slug: "alice", Name: "Alice"},
	})

	req := httptest.NewRequest(http.MethodPost, "/assign", strings.NewReader(`{"target":"bob","task":"do something"}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	srv.handleAssign(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", w.Code)
	}
	var body map[string]string
	json.NewDecoder(w.Body).Decode(&body)
	if !strings.Contains(body["error"], "bob") {
		t.Errorf("expected error about 'bob', got %q", body["error"])
	}
}

func TestHandleAssign_ForwardsToCrewshipd(t *testing.T) {
	// Mock crewshipd server
	var receivedToken, receivedBody string
	mockCrewshipd := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/internal/assignments" {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		receivedToken = r.Header.Get("X-Internal-Token")
		bodyBytes, _ := io.ReadAll(r.Body)
		receivedBody = string(bodyBytes)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		w.Write([]byte(`{"assignment_id":"test-123","status":"PENDING"}`))
	}))
	defer mockCrewshipd.Close()

	srv := newAssignmentServer(t, &IPCConfig{
		BaseURL:     mockCrewshipd.URL,
		Token:       "secret-token",
		CrewID:      "crew-1",
		WorkspaceID: "ws-1",
		ChatID:      "chat-1",
	}, []CrewMember{
		{Slug: "viktor", Name: "Viktor"},
	})

	req := httptest.NewRequest(http.MethodPost, "/assign", strings.NewReader(`{"target":"viktor","task":"write a hello world script"}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	srv.handleAssign(w, req)

	if w.Code != http.StatusCreated {
		t.Errorf("expected 201, got %d; body: %s", w.Code, w.Body.String())
	}
	if receivedToken != "secret-token" {
		t.Errorf("expected X-Internal-Token=secret-token, got %q", receivedToken)
	}
	var forwarded map[string]string
	if err := json.Unmarshal([]byte(receivedBody), &forwarded); err != nil {
		t.Fatalf("invalid forwarded body: %v", err)
	}
	if forwarded["target_slug"] != "viktor" {
		t.Errorf("expected target_slug=viktor, got %q", forwarded["target_slug"])
	}
	if forwarded["task"] != "write a hello world script" {
		t.Errorf("expected task forwarded, got %q", forwarded["task"])
	}
	if forwarded["crew_id"] != "crew-1" {
		t.Errorf("expected crew_id=crew-1, got %q", forwarded["crew_id"])
	}

	var result map[string]string
	json.NewDecoder(w.Body).Decode(&result)
	if result["assignment_id"] != "test-123" {
		t.Errorf("expected assignment_id=test-123 in response, got %q", result["assignment_id"])
	}
}

// #1040: handleResults forwards the sidecar's OWN trusted workspace_id (so the
// internal handler can scope the row) and rejects an assignment id that could
// smuggle a query string (which would otherwise override that workspace_id).
func TestHandleResults_ForwardsBoundWorkspaceAndRejectsInjection(t *testing.T) {
	var gotPath, gotQuery string
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotQuery = r.URL.RawQuery
		writeJSONResponse(w, http.StatusOK, map[string]string{"status": "COMPLETED"})
	}))
	defer mock.Close()
	srv := newAssignmentServer(t, &IPCConfig{BaseURL: mock.URL, Token: "t", WorkspaceID: "ws-1"}, nil)

	// Clean id → proxies with the trusted workspace_id.
	req := httptest.NewRequest(http.MethodGet, "/results/clh3assign0001", nil)
	w := httptest.NewRecorder()
	srv.handleResults(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("clean id: got %d: %s", w.Code, w.Body.String())
	}
	if gotPath != "/api/v1/internal/assignments/clh3assign0001" {
		t.Errorf("path = %q", gotPath)
	}
	if !strings.Contains(gotQuery, "workspace_id=ws-1") {
		t.Errorf("query = %q, want trusted workspace_id=ws-1", gotQuery)
	}

	// Injection id (%3F → '?') → 400 before proxy.
	req2 := httptest.NewRequest(http.MethodGet, "/results/abc%3Fworkspace_id=ws-evil", nil)
	w2 := httptest.NewRecorder()
	srv.handleResults(w2, req2)
	if w2.Code != http.StatusBadRequest {
		t.Errorf("injection assignment id must 400, got %d", w2.Code)
	}

	// Dot-dot id → 400 (the path-traversal guard must survive the charset
	// rewrite; ".." contains none of /?#&=% and PathEscape leaves '.' intact).
	req3 := httptest.NewRequest(http.MethodGet, "/results/..", nil)
	w3 := httptest.NewRecorder()
	srv.handleResults(w3, req3)
	if w3.Code != http.StatusBadRequest {
		t.Errorf("dot-dot assignment id must 400, got %d", w3.Code)
	}
}

func TestHandleResults_NoIPC(t *testing.T) {
	srv := newAssignmentServer(t, nil, nil)

	req := httptest.NewRequest(http.MethodGet, "/results/abc123", nil)
	w := httptest.NewRecorder()

	srv.handleResults(w, req)

	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("expected 503, got %d", w.Code)
	}
}

func TestHandleResults_ProxiesToCrewshipd(t *testing.T) {
	mockCrewshipd := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/internal/assignments/abc123" {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"id":"abc123","status":"COMPLETED","result_summary":"hello world"}`))
	}))
	defer mockCrewshipd.Close()

	srv := newAssignmentServer(t, &IPCConfig{BaseURL: mockCrewshipd.URL, Token: "tok"}, nil)

	req := httptest.NewRequest(http.MethodGet, "/results/abc123", nil)
	w := httptest.NewRecorder()

	srv.handleResults(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", w.Code)
	}
	var result map[string]interface{}
	json.NewDecoder(w.Body).Decode(&result)
	if result["status"] != "COMPLETED" {
		t.Errorf("expected status=COMPLETED, got %v", result["status"])
	}
}

// ── Cross-crew assignment ───────────────────────────────────────────────
//
// The server has enforced "crews must be linked" on this path since crew
// connections existed (assignments_run.go), but nothing could ever reach that
// branch: the sidecar rejected any target outside its own crew and then sent
// its OWN crew_id regardless. So the check was dead code and a lead had no
// way to hand work to a crew it is linked to — a live delegation to a
// connected crew failed with "not found in crew".
//
// Naming a crew makes the target explicit; the server still decides whether
// it is allowed (link required, plus the workspace binding on crew_id).

// stubCrewshipdForAssign returns a crewshipd stub that answers the crew
// lookup the sidecar uses to resolve a slug, and records the forwarded
// assignment body.
func stubCrewshipdForAssign(t *testing.T, crews string, forwarded *map[string]string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/internal/crews":
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(crews))
		case "/api/v1/internal/assignments":
			bodyBytes, _ := io.ReadAll(r.Body)
			var got map[string]string
			if err := json.Unmarshal(bodyBytes, &got); err != nil {
				t.Errorf("invalid forwarded body: %v", err)
			}
			*forwarded = got
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusCreated)
			w.Write([]byte(`{"assignment_id":"a-1","status":"PENDING"}`))
		default:
			http.Error(w, "not found", http.StatusNotFound)
		}
	}))
}

func TestHandleAssign_NamedCrew_ForwardsThatCrewID(t *testing.T) {
	var forwarded map[string]string
	mock := stubCrewshipdForAssign(t,
		`[{"id":"crew-ops","slug":"ops"},{"id":"crew-1","slug":"engineering"}]`, &forwarded)
	defer mock.Close()

	srv := newAssignmentServer(t, &IPCConfig{
		BaseURL: mock.URL, Token: "tok", CrewID: "crew-1", WorkspaceID: "ws-1", ChatID: "chat-1",
	}, []CrewMember{{Slug: "alex", Name: "Alex"}})

	// morgan is NOT a member of this crew — that is the point.
	req := httptest.NewRequest(http.MethodPost, "/assign",
		strings.NewReader(`{"target":"morgan","task":"page the on-call","crew":"ops"}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	srv.handleAssign(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body: %s", w.Code, w.Body.String())
	}
	if forwarded["crew_id"] != "crew-ops" {
		t.Errorf("crew_id = %q, want crew-ops — the named crew, not the caller's", forwarded["crew_id"])
	}
	if forwarded["target_slug"] != "morgan" {
		t.Errorf("target_slug = %q, want morgan", forwarded["target_slug"])
	}
	// workspace_id and chat_id stay the sidecar's own: the agent names WHO,
	// never WHERE FROM.
	if forwarded["workspace_id"] != "ws-1" || forwarded["chat_id"] != "chat-1" {
		t.Errorf("identity fields were not injected by the sidecar: %+v", forwarded)
	}
}

func TestHandleAssign_UnknownCrew_Returns404(t *testing.T) {
	var forwarded map[string]string
	mock := stubCrewshipdForAssign(t, `[{"id":"crew-1","slug":"engineering"}]`, &forwarded)
	defer mock.Close()

	srv := newAssignmentServer(t, &IPCConfig{
		BaseURL: mock.URL, Token: "tok", CrewID: "crew-1", WorkspaceID: "ws-1", ChatID: "chat-1",
	}, []CrewMember{{Slug: "alex", Name: "Alex"}})

	req := httptest.NewRequest(http.MethodPost, "/assign",
		strings.NewReader(`{"target":"morgan","task":"x","crew":"nope"}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	srv.handleAssign(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body: %s", w.Code, w.Body.String())
	}
	if forwarded != nil {
		t.Errorf("an unresolvable crew must not reach crewshipd, forwarded %+v", forwarded)
	}
}

// Naming your own crew is the same request as not naming one, so the
// membership check still applies — otherwise the explicit form would be a way
// to skip it.
func TestHandleAssign_OwnCrewNamed_StillChecksMembership(t *testing.T) {
	var forwarded map[string]string
	mock := stubCrewshipdForAssign(t, `[{"id":"crew-1","slug":"engineering"}]`, &forwarded)
	defer mock.Close()

	srv := newAssignmentServer(t, &IPCConfig{
		BaseURL: mock.URL, Token: "tok", CrewID: "crew-1", WorkspaceID: "ws-1", ChatID: "chat-1",
	}, []CrewMember{{Slug: "alex", Name: "Alex"}})

	req := httptest.NewRequest(http.MethodPost, "/assign",
		strings.NewReader(`{"target":"ghost","task":"x","crew":"engineering"}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	srv.handleAssign(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body: %s", w.Code, w.Body.String())
	}
}
