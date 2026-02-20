package sidecar

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// newAssignmentServer creates a Server configured for assignment testing with a mock crewshipd.
func newAssignmentServer(t *testing.T, ipc *IPCConfig, members []CrewMember) *Server {
	t.Helper()
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
		bodyBytes := make([]byte, 4096)
		n, _ := r.Body.Read(bodyBytes)
		receivedBody = string(bodyBytes[:n])
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
