package sidecar

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// --- POST /issue/create tests ---

func TestHandleIssueCreate_NoIPC(t *testing.T) {
	srv := newQueryServer(t, nil, nil)

	req := httptest.NewRequest(http.MethodPost, "/issue/create",
		strings.NewReader(`{"title":"Bug"}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	srv.handleIssueCreate(w, req)

	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("expected 503, got %d", w.Code)
	}
}

func TestHandleIssueCreate_InvalidJSON(t *testing.T) {
	srv := newQueryServer(t, &IPCConfig{BaseURL: "http://x", Token: "tok"}, nil)

	req := httptest.NewRequest(http.MethodPost, "/issue/create",
		strings.NewReader(`not-json`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	srv.handleIssueCreate(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", w.Code)
	}
	var body map[string]string
	if err := json.NewDecoder(w.Body).Decode(&body); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if body["error"] != "invalid JSON body" {
		t.Errorf("expected 'invalid JSON body', got %q", body["error"])
	}
}

func TestHandleIssueCreate_MissingTitle(t *testing.T) {
	srv := newQueryServer(t, &IPCConfig{BaseURL: "http://x", Token: "tok", CrewID: "c1"}, nil)

	req := httptest.NewRequest(http.MethodPost, "/issue/create",
		strings.NewReader(`{"description":"some bug"}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	srv.handleIssueCreate(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", w.Code)
	}
	var body map[string]string
	json.NewDecoder(w.Body).Decode(&body)
	if !strings.Contains(body["error"], "title") {
		t.Errorf("expected error about title, got %q", body["error"])
	}
}

func TestHandleIssueCreate_EmptyTitleString(t *testing.T) {
	srv := newQueryServer(t, &IPCConfig{BaseURL: "http://x", Token: "tok", CrewID: "c1"}, nil)

	req := httptest.NewRequest(http.MethodPost, "/issue/create",
		strings.NewReader(`{"title":"","description":"something"}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	srv.handleIssueCreate(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", w.Code)
	}
}

func TestHandleIssueCreate_MissingCrewID(t *testing.T) {
	// Neither request CrewID nor IPCConfig CrewID set.
	srv := newQueryServer(t, &IPCConfig{BaseURL: "http://x", Token: "tok"}, nil)

	req := httptest.NewRequest(http.MethodPost, "/issue/create",
		strings.NewReader(`{"title":"Bug"}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	srv.handleIssueCreate(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", w.Code)
	}
	var body map[string]string
	json.NewDecoder(w.Body).Decode(&body)
	if !strings.Contains(body["error"], "crew_id") {
		t.Errorf("expected error about crew_id, got %q", body["error"])
	}
}

func TestHandleIssueCreate_ForwardsToCrewshipd(t *testing.T) {
	var receivedToken, receivedPath, receivedMethod string
	var receivedBody map[string]interface{}
	mockCrewshipd := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedToken = r.Header.Get("X-Internal-Token")
		receivedPath = r.URL.Path
		receivedMethod = r.Method
		bodyBytes, _ := io.ReadAll(r.Body)
		json.Unmarshal(bodyBytes, &receivedBody)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		w.Write([]byte(`{"issue_id":"iss-1","status":"OPEN"}`))
	}))
	defer mockCrewshipd.Close()

	srv := newQueryServer(t, &IPCConfig{
		BaseURL:     mockCrewshipd.URL,
		Token:       "secret-token",
		CrewID:      "crew-1",
		WorkspaceID: "ws-1",
	}, nil)

	body := `{"title":"Login broken","description":"500 on POST /login","priority":"high","project_id":"proj-1","assignee_id":"agent-2","crew_id":"crew-override"}`
	req := httptest.NewRequest(http.MethodPost, "/issue/create",
		strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	srv.handleIssueCreate(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d; body: %s", w.Code, w.Body.String())
	}
	if receivedToken != "secret-token" {
		t.Errorf("expected X-Internal-Token=secret-token, got %q", receivedToken)
	}
	if receivedMethod != http.MethodPost {
		t.Errorf("expected forwarded method POST, got %q", receivedMethod)
	}
	if receivedPath != "/api/v1/internal/issues" {
		t.Errorf("expected forwarded path /api/v1/internal/issues, got %q", receivedPath)
	}
	if receivedBody["title"] != "Login broken" {
		t.Errorf("expected title forwarded, got %v", receivedBody["title"])
	}
	if receivedBody["description"] != "500 on POST /login" {
		t.Errorf("expected description forwarded, got %v", receivedBody["description"])
	}
	if receivedBody["priority"] != "high" {
		t.Errorf("expected priority=high forwarded, got %v", receivedBody["priority"])
	}
	if receivedBody["project_id"] != "proj-1" {
		t.Errorf("expected project_id=proj-1 forwarded, got %v", receivedBody["project_id"])
	}
	if receivedBody["assignee_id"] != "agent-2" {
		t.Errorf("expected assignee_id=agent-2 forwarded, got %v", receivedBody["assignee_id"])
	}
	if receivedBody["assignee_type"] != "agent" {
		t.Errorf("expected assignee_type=agent (always), got %v", receivedBody["assignee_type"])
	}
	if receivedBody["crew_id"] != "crew-1" {
		t.Errorf("request crew_id must be ignored; expected trusted IPC crew, got %v", receivedBody["crew_id"])
	}
	if receivedBody["workspace_id"] != "ws-1" {
		t.Errorf("expected workspace_id from IPC, got %v", receivedBody["workspace_id"])
	}

	// Response body should be the upstream JSON forwarded back verbatim.
	var result map[string]interface{}
	if err := json.NewDecoder(w.Body).Decode(&result); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if result["issue_id"] != "iss-1" {
		t.Errorf("expected issue_id=iss-1 forwarded, got %v", result["issue_id"])
	}
}

func TestHandleIssueCreate_DefaultsCrewIDFromIPC(t *testing.T) {
	var receivedBody map[string]interface{}
	mockCrewshipd := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		bodyBytes, _ := io.ReadAll(r.Body)
		json.Unmarshal(bodyBytes, &receivedBody)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		w.Write([]byte(`{}`))
	}))
	defer mockCrewshipd.Close()

	srv := newQueryServer(t, &IPCConfig{
		BaseURL:     mockCrewshipd.URL,
		Token:       "tok",
		CrewID:      "ipc-crew",
		WorkspaceID: "ws-1",
	}, nil)

	// Request omits crew_id — handler must fall back to s.ipc.CrewID.
	req := httptest.NewRequest(http.MethodPost, "/issue/create",
		strings.NewReader(`{"title":"Bug"}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	srv.handleIssueCreate(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d; body: %s", w.Code, w.Body.String())
	}
	if receivedBody["crew_id"] != "ipc-crew" {
		t.Errorf("expected crew_id default from IPC, got %v", receivedBody["crew_id"])
	}
}

// TestSecIssueCreate_RequestCrewIDIgnored is the security regression test for
// the cross-crew override vulnerability: a request-supplied crew_id must be
// IGNORED. The sidecar always forwards its trusted IPC crew + agent identity.
func TestSecIssueCreate_RequestCrewIDIgnored(t *testing.T) {
	var receivedBody map[string]interface{}
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		json.Unmarshal(b, &receivedBody)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		w.Write([]byte(`{}`))
	}))
	defer mock.Close()

	srv := newQueryServer(t, &IPCConfig{
		BaseURL: mock.URL, Token: "t", CrewID: "ipc-crew", WorkspaceID: "w", AgentID: "ipc-agent",
	}, nil)

	req := httptest.NewRequest(http.MethodPost, "/issue/create",
		strings.NewReader(`{"title":"x","crew_id":"override-crew"}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	srv.handleIssueCreate(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d", w.Code)
	}
	if receivedBody["crew_id"] != "ipc-crew" {
		t.Errorf("request crew_id must be ignored; expected trusted IPC crew, got %v", receivedBody["crew_id"])
	}
	if receivedBody["author_agent_id"] != "ipc-agent" {
		t.Errorf("expected author_agent_id forwarded from IPC, got %v", receivedBody["author_agent_id"])
	}
}

func TestHandleIssueCreate_OmitsEmptyOptionalFields(t *testing.T) {
	var receivedBody map[string]interface{}
	mockCrewshipd := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		bodyBytes, _ := io.ReadAll(r.Body)
		json.Unmarshal(bodyBytes, &receivedBody)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		w.Write([]byte(`{}`))
	}))
	defer mockCrewshipd.Close()

	srv := newQueryServer(t, &IPCConfig{
		BaseURL:     mockCrewshipd.URL,
		Token:       "tok",
		CrewID:      "c1",
		WorkspaceID: "ws-1",
	}, nil)

	// Only required fields — description, priority, project_id, assignee_id all omitted.
	req := httptest.NewRequest(http.MethodPost, "/issue/create",
		strings.NewReader(`{"title":"Bug"}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	srv.handleIssueCreate(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d", w.Code)
	}
	for _, key := range []string{"description", "priority", "project_id", "assignee_id"} {
		if _, ok := receivedBody[key]; ok {
			t.Errorf("expected %q to be omitted when empty, but it was forwarded as %v",
				key, receivedBody[key])
		}
	}
	// Required fields still present.
	if receivedBody["title"] != "Bug" {
		t.Errorf("expected title=Bug, got %v", receivedBody["title"])
	}
	if receivedBody["assignee_type"] != "agent" {
		t.Errorf("expected assignee_type=agent always set, got %v", receivedBody["assignee_type"])
	}
}

func TestHandleIssueCreate_UpstreamErrorPassedThrough(t *testing.T) {
	mockCrewshipd := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		w.Write([]byte(`{"error":"insufficient permissions"}`))
	}))
	defer mockCrewshipd.Close()

	srv := newQueryServer(t, &IPCConfig{
		BaseURL: mockCrewshipd.URL,
		Token:   "tok",
		CrewID:  "c1",
	}, nil)

	req := httptest.NewRequest(http.MethodPost, "/issue/create",
		strings.NewReader(`{"title":"Bug"}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	srv.handleIssueCreate(w, req)

	if w.Code != http.StatusForbidden {
		t.Errorf("expected 403 forwarded, got %d", w.Code)
	}
	var result map[string]interface{}
	json.NewDecoder(w.Body).Decode(&result)
	if result["error"] != "insufficient permissions" {
		t.Errorf("expected upstream error body forwarded, got %v", result["error"])
	}
}

func TestHandleIssueCreate_UpstreamUnreachable(t *testing.T) {
	// Use a closed listener address so the upstream Do() fails immediately.
	srv := newQueryServer(t, &IPCConfig{
		BaseURL: "http://127.0.0.1:1", // port 1 reserved, refused
		Token:   "tok",
		CrewID:  "c1",
	}, nil)

	req := httptest.NewRequest(http.MethodPost, "/issue/create",
		strings.NewReader(`{"title":"Bug"}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	srv.handleIssueCreate(w, req)

	if w.Code != http.StatusBadGateway {
		t.Errorf("expected 502 on upstream unreachable, got %d", w.Code)
	}
	var result map[string]string
	json.NewDecoder(w.Body).Decode(&result)
	if !strings.Contains(result["error"], "issue create") {
		t.Errorf("expected error label to contain 'issue create', got %q", result["error"])
	}
}
