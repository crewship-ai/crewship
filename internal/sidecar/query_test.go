package sidecar

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

// newQueryServer creates a Server configured for query/escalation testing.
func newQueryServer(t *testing.T, ipc *IPCConfig, members []CrewMember) *Server {
	t.Helper()
	return NewServer(ServerConfig{
		Addr:        "127.0.0.1:0",
		Logger:      slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn})),
		IPC:         ipc,
		CrewMembers: members,
	})
}

// --- NoIPC tests (consolidated) ---

func TestHandlers_NoIPC(t *testing.T) {
	srv := newQueryServer(t, nil, nil)

	cases := []struct {
		name    string
		method  string
		path    string
		body    string
		handler func(http.ResponseWriter, *http.Request)
	}{
		{"Query", http.MethodPost, "/query", `{"target":"nela","question":"what framework?","from":"viktor"}`, nil},
		{"Standup", http.MethodGet, "/standup", "", nil},
		{"Escalate", http.MethodPost, "/escalate", `{"from":"nela","reason":"need decision"}`, nil},
	}
	// Assign handlers after srv is created
	cases[0].handler = srv.handleQuery
	cases[1].handler = srv.handleStandup
	cases[2].handler = srv.handleEscalate

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var bodyReader io.Reader
			if tc.body != "" {
				bodyReader = strings.NewReader(tc.body)
			}
			req := httptest.NewRequest(tc.method, tc.path, bodyReader)
			if tc.body != "" {
				req.Header.Set("Content-Type", "application/json")
			}
			w := httptest.NewRecorder()

			tc.handler(w, req)

			if w.Code != http.StatusServiceUnavailable {
				t.Errorf("expected 503, got %d", w.Code)
			}
		})
	}
}

// --- POST /query tests ---

func TestHandleQuery_InvalidJSON(t *testing.T) {
	srv := newQueryServer(t, &IPCConfig{BaseURL: "http://x", Token: "tok"}, nil)

	req := httptest.NewRequest(http.MethodPost, "/query", strings.NewReader(`not-json`))
	w := httptest.NewRecorder()

	srv.handleQuery(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", w.Code)
	}
}

func TestHandleQuery_MissingFields(t *testing.T) {
	srv := newQueryServer(t, &IPCConfig{BaseURL: "http://x", Token: "tok"}, nil)

	req := httptest.NewRequest(http.MethodPost, "/query",
		strings.NewReader(`{"target":"nela"}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	srv.handleQuery(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", w.Code)
	}
}

func TestHandleQuery_UnknownTarget(t *testing.T) {
	srv := newQueryServer(t, &IPCConfig{BaseURL: "http://x", Token: "tok"}, []CrewMember{
		{Slug: "alice", Name: "Alice"},
	})

	req := httptest.NewRequest(http.MethodPost, "/query",
		strings.NewReader(`{"target":"bob","question":"hello?","from":"alice"}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	srv.handleQuery(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", w.Code)
	}
	var body map[string]string
	if err := json.NewDecoder(w.Body).Decode(&body); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if !strings.Contains(body["error"], "bob") {
		t.Errorf("expected error about 'bob', got %q", body["error"])
	}
}

func TestHandleQuery_DepthLimit(t *testing.T) {
	srv := newQueryServer(t, &IPCConfig{BaseURL: "http://x", Token: "tok"}, []CrewMember{
		{Slug: "nela", Name: "Nela"},
	})

	req := httptest.NewRequest(http.MethodPost, "/query",
		strings.NewReader(`{"target":"nela","question":"hello?","from":"viktor","depth":2}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	srv.handleQuery(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for depth limit, got %d", w.Code)
	}
	var body map[string]string
	if err := json.NewDecoder(w.Body).Decode(&body); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if !strings.Contains(body["error"], "depth limit") {
		t.Errorf("expected depth limit error, got %q", body["error"])
	}
}

func TestHandleQuery_ForwardsToCrewshipd(t *testing.T) {
	var receivedToken, receivedBody string
	mockCrewshipd := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/internal/queries" {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		receivedToken = r.Header.Get("X-Internal-Token")
		bodyBytes, _ := io.ReadAll(r.Body)
		receivedBody = string(bodyBytes)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"query_id":"q-123","response":"Tailwind CSS 4","status":"COMPLETED"}`))
	}))
	defer mockCrewshipd.Close()

	srv := newQueryServer(t, &IPCConfig{
		BaseURL:     mockCrewshipd.URL,
		Token:       "secret-token",
		CrewID:      "crew-1",
		WorkspaceID: "ws-1",
		ChatID:      "chat-1",
	}, []CrewMember{
		{Slug: "nela", Name: "Nela"},
	})

	req := httptest.NewRequest(http.MethodPost, "/query",
		strings.NewReader(`{"target":"nela","question":"What CSS framework?","from":"viktor"}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	srv.handleQuery(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d; body: %s", w.Code, w.Body.String())
	}
	if receivedToken != "secret-token" {
		t.Errorf("expected X-Internal-Token=secret-token, got %q", receivedToken)
	}

	var forwarded map[string]interface{}
	if err := json.Unmarshal([]byte(receivedBody), &forwarded); err != nil {
		t.Fatalf("invalid forwarded body: %v", err)
	}
	if forwarded["target_slug"] != "nela" {
		t.Errorf("expected target_slug=nela, got %v", forwarded["target_slug"])
	}
	if forwarded["question"] != "What CSS framework?" {
		t.Errorf("expected question forwarded, got %v", forwarded["question"])
	}
	if forwarded["crew_id"] != "crew-1" {
		t.Errorf("expected crew_id=crew-1, got %v", forwarded["crew_id"])
	}

	var result map[string]interface{}
	if err := json.NewDecoder(w.Body).Decode(&result); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if result["query_id"] != "q-123" {
		t.Errorf("expected query_id=q-123, got %v", result["query_id"])
	}
}

// --- GET /standup tests ---

func TestHandleStandup_ProxiesToCrewshipd(t *testing.T) {
	var receivedPath string
	mockCrewshipd := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedPath = r.URL.String()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"standup":"[CREW STANDUP]...","crew_id":"crew-1"}`))
	}))
	defer mockCrewshipd.Close()

	srv := newQueryServer(t, &IPCConfig{
		BaseURL: mockCrewshipd.URL,
		Token:   "tok",
		CrewID:  "crew-1",
	}, nil)

	req := httptest.NewRequest(http.MethodGet, "/standup", nil)
	w := httptest.NewRecorder()

	srv.handleStandup(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", w.Code)
	}
	if !strings.Contains(receivedPath, "crew_id=crew-1") {
		t.Errorf("expected crew_id in forwarded request, got %q", receivedPath)
	}
}

// --- POST /escalate tests ---

func TestHandleEscalate_MissingFields(t *testing.T) {
	srv := newQueryServer(t, &IPCConfig{BaseURL: "http://x", Token: "tok"}, nil)

	req := httptest.NewRequest(http.MethodPost, "/escalate",
		strings.NewReader(`{"from":"nela"}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	srv.handleEscalate(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", w.Code)
	}
}

func TestHandleEscalate_ForwardsToCrewshipd(t *testing.T) {
	var receivedToken string
	var receivedBody map[string]string
	mockCrewshipd := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/internal/escalations" && r.Method == http.MethodPost {
			receivedToken = r.Header.Get("X-Internal-Token")
			bodyBytes, _ := io.ReadAll(r.Body)
			if err := json.Unmarshal(bodyBytes, &receivedBody); err != nil {
				http.Error(w, "bad json", http.StatusBadRequest)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusCreated)
			w.Write([]byte(`{"escalation_id":"esc-1","status":"PENDING"}`))
			return
		}
		if strings.HasPrefix(r.URL.Path, "/api/v1/internal/escalations/") && strings.HasSuffix(r.URL.Path, "/wait") {
			// Immediately return resolved for test speed.
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(`{"status":"RESOLVED","resolution":"Approved","action":"approve"}`))
			return
		}
		http.Error(w, "not found", http.StatusNotFound)
	}))
	defer mockCrewshipd.Close()

	srv := newQueryServer(t, &IPCConfig{
		BaseURL:     mockCrewshipd.URL,
		Token:       "secret-token",
		CrewID:      "crew-1",
		WorkspaceID: "ws-1",
		ChatID:      "chat-1",
	}, nil)

	req := httptest.NewRequest(http.MethodPost, "/escalate",
		strings.NewReader(`{"from":"nela","reason":"API conflict","context":"Viktor changed endpoints"}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	srv.handleEscalate(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d; body: %s", w.Code, w.Body.String())
	}
	if receivedToken != "secret-token" {
		t.Errorf("expected X-Internal-Token=secret-token, got %q", receivedToken)
	}
	if receivedBody["from_slug"] != "nela" {
		t.Errorf("expected from_slug=nela, got %q", receivedBody["from_slug"])
	}
	if receivedBody["reason"] != "API conflict" {
		t.Errorf("expected reason='API conflict', got %q", receivedBody["reason"])
	}
	if receivedBody["crew_id"] != "crew-1" {
		t.Errorf("expected crew_id=crew-1, got %q", receivedBody["crew_id"])
	}

	// Verify the response contains resolution from the wait endpoint.
	var result map[string]interface{}
	json.NewDecoder(w.Body).Decode(&result)
	if result["status"] != "RESOLVED" {
		t.Errorf("expected status=RESOLVED, got %v", result["status"])
	}
	if result["resolution"] != "Approved" {
		t.Errorf("expected resolution=Approved, got %v", result["resolution"])
	}
	if result["escalation_id"] != "esc-1" {
		t.Errorf("expected escalation_id=esc-1, got %v", result["escalation_id"])
	}
}

func TestHandleEscalate_BlocksUntilResolution(t *testing.T) {
	waitCalled := make(chan struct{})
	mockCrewshipd := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/internal/escalations" && r.Method == http.MethodPost {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusCreated)
			w.Write([]byte(`{"escalation_id":"esc-2","status":"PENDING"}`))
			return
		}
		if strings.HasSuffix(r.URL.Path, "/wait") {
			close(waitCalled)
			// Simulate delay then resolve.
			time.Sleep(100 * time.Millisecond)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(`{"status":"RESOLVED","resolution":"Go ahead","action":"approve"}`))
			return
		}
		http.Error(w, "not found", http.StatusNotFound)
	}))
	defer mockCrewshipd.Close()

	srv := newQueryServer(t, &IPCConfig{
		BaseURL:     mockCrewshipd.URL,
		Token:       "tok",
		CrewID:      "c1",
		WorkspaceID: "ws1",
		ChatID:      "ch1",
	}, nil)

	req := httptest.NewRequest(http.MethodPost, "/escalate",
		strings.NewReader(`{"from":"nela","reason":"need help"}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	srv.handleEscalate(w, req)

	// Ensure wait was called.
	select {
	case <-waitCalled:
	default:
		t.Error("wait endpoint was not called")
	}

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d; body: %s", w.Code, w.Body.String())
	}

	var result map[string]interface{}
	json.NewDecoder(w.Body).Decode(&result)
	if result["resolution"] != "Go ahead" {
		t.Errorf("expected resolution='Go ahead', got %v", result["resolution"])
	}
	if result["escalation_id"] != "esc-2" {
		t.Errorf("expected escalation_id=esc-2, got %v", result["escalation_id"])
	}
}

func TestHandleEscalate_TimeoutReturnsStatus(t *testing.T) {
	mockCrewshipd := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/internal/escalations" && r.Method == http.MethodPost {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusCreated)
			w.Write([]byte(`{"escalation_id":"esc-3","status":"PENDING"}`))
			return
		}
		if strings.HasSuffix(r.URL.Path, "/wait") {
			// Simulate timeout by returning 408.
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusRequestTimeout)
			w.Write([]byte(`{"status":"TIMEOUT","error":"escalation not resolved in time"}`))
			return
		}
		http.Error(w, "not found", http.StatusNotFound)
	}))
	defer mockCrewshipd.Close()

	srv := newQueryServer(t, &IPCConfig{
		BaseURL:     mockCrewshipd.URL,
		Token:       "tok",
		CrewID:      "c1",
		WorkspaceID: "ws1",
		ChatID:      "ch1",
	}, nil)

	req := httptest.NewRequest(http.MethodPost, "/escalate",
		strings.NewReader(`{"from":"nela","reason":"need help"}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	srv.handleEscalate(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d; body: %s", w.Code, w.Body.String())
	}

	var result map[string]interface{}
	json.NewDecoder(w.Body).Decode(&result)
	if result["status"] != "TIMEOUT" {
		t.Errorf("expected status=TIMEOUT, got %v", result["status"])
	}
}

func TestHandleEscalate_ForwardsEvidencePack(t *testing.T) {
	var receivedBody map[string]string
	mockCrewshipd := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/internal/escalations" && r.Method == http.MethodPost {
			bodyBytes, _ := io.ReadAll(r.Body)
			json.Unmarshal(bodyBytes, &receivedBody)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusCreated)
			w.Write([]byte(`{"escalation_id":"esc-4","status":"PENDING"}`))
			return
		}
		if strings.HasSuffix(r.URL.Path, "/wait") {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(`{"status":"RESOLVED","resolution":"OK","action":"approve"}`))
			return
		}
		http.Error(w, "not found", http.StatusNotFound)
	}))
	defer mockCrewshipd.Close()

	srv := newQueryServer(t, &IPCConfig{
		BaseURL:     mockCrewshipd.URL,
		Token:       "tok",
		CrewID:      "c1",
		WorkspaceID: "ws1",
		ChatID:      "ch1",
	}, nil)

	// Uses proper JSON escaping for the evidence_pack payload.
	reqBody := `{"from":"nela","reason":"Permission denied","evidence_pack":"{\"task_title\":\"Process invoices\",\"agent_slug\":\"nela\",\"error\":\"403 forbidden\"}"}`

	req := httptest.NewRequest(http.MethodPost, "/escalate",
		strings.NewReader(reqBody))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	srv.handleEscalate(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d; body: %s", w.Code, w.Body.String())
	}

	// The evidence_pack should have been forwarded as metadata.
	if receivedBody["metadata"] == "" {
		t.Error("expected evidence_pack to be forwarded as metadata")
	}
	if !strings.Contains(receivedBody["metadata"], "Process invoices") {
		t.Errorf("expected metadata to contain evidence pack, got %q", receivedBody["metadata"])
	}
}
