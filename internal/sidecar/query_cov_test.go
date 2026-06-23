package sidecar

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func newConfidenceServer(t *testing.T, ipc *IPCConfig) *Server {
	t.Helper()
	return NewServer(ServerConfig{Addr: "127.0.0.1:0", Logger: covLogger(), IPC: ipc})
}

func TestCovReportConfidence_NoIPC(t *testing.T) {
	srv := newConfidenceServer(t, nil)
	req := httptest.NewRequest("POST", "http://localhost:9119/report-confidence",
		strings.NewReader(`{"confidence":0.4,"reason":"unsure"}`))
	w := httptest.NewRecorder()
	srv.handleReportConfidence(w, req)

	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d", w.Code)
	}
}

func TestCovReportConfidence_InvalidJSON(t *testing.T) {
	srv := newConfidenceServer(t, &IPCConfig{BaseURL: "http://127.0.0.1:1", Token: "t"})
	req := httptest.NewRequest("POST", "http://localhost:9119/report-confidence",
		strings.NewReader("{not json"))
	w := httptest.NewRecorder()
	srv.handleReportConfidence(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), "invalid JSON body") {
		t.Errorf("body = %s", w.Body.String())
	}
}

func TestCovReportConfidence_ForwardsToCrewshipd(t *testing.T) {
	var gotPath, gotToken string
	var gotPayload map[string]interface{}
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotToken = r.Header.Get("X-Internal-Token")
		json.NewDecoder(r.Body).Decode(&gotPayload)
		writeJSONResponse(w, http.StatusOK, map[string]string{"status": "recorded"})
	}))
	defer mock.Close()

	srv := newConfidenceServer(t, &IPCConfig{
		BaseURL:     mock.URL,
		Token:       "internal-tok",
		AgentID:     "agent-9",
		CrewID:      "crew-3",
		WorkspaceID: "ws-1",
		ChatID:      "chat-7",
	})

	req := httptest.NewRequest("POST", "http://localhost:9119/report-confidence",
		strings.NewReader(`{"confidence":0.85,"reason":"tests passing"}`))
	w := httptest.NewRecorder()
	srv.handleReportConfidence(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	if gotPath != "/api/v1/internal/report-confidence" {
		t.Errorf("path = %q", gotPath)
	}
	if gotToken != "internal-tok" {
		t.Errorf("token = %q", gotToken)
	}
	if gotPayload["agent_id"] != "agent-9" || gotPayload["crew_id"] != "crew-3" ||
		gotPayload["workspace_id"] != "ws-1" || gotPayload["chat_id"] != "chat-7" {
		t.Errorf("identity fields wrong: %+v", gotPayload)
	}
	if gotPayload["confidence"] != 0.85 {
		t.Errorf("confidence = %v", gotPayload["confidence"])
	}
	if gotPayload["reason"] != "tests passing" {
		t.Errorf("reason = %v", gotPayload["reason"])
	}
	if !strings.Contains(w.Body.String(), `"status":"recorded"`) {
		t.Errorf("response = %s", w.Body.String())
	}
}

func TestCovReportConfidence_UpstreamErrorStatusPassthrough(t *testing.T) {
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSONResponse(w, http.StatusUnprocessableEntity, map[string]string{"error": "confidence out of range"})
	}))
	defer mock.Close()

	srv := newConfidenceServer(t, &IPCConfig{BaseURL: mock.URL, Token: "t"})
	req := httptest.NewRequest("POST", "http://localhost:9119/report-confidence",
		strings.NewReader(`{"confidence":7.5,"reason":"oops"}`))
	w := httptest.NewRecorder()
	srv.handleReportConfidence(w, req)

	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("expected 422 passthrough, got %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), "confidence out of range") {
		t.Errorf("body = %s", w.Body.String())
	}
}

func TestCovReportConfidence_CrewshipdDown(t *testing.T) {
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	mock.Close() // already closed → connection refused

	srv := newConfidenceServer(t, &IPCConfig{BaseURL: mock.URL, Token: "t"})
	req := httptest.NewRequest("POST", "http://localhost:9119/report-confidence",
		strings.NewReader(`{"confidence":0.5,"reason":"r"}`))
	w := httptest.NewRecorder()
	srv.handleReportConfidence(w, req)

	if w.Code != http.StatusBadGateway {
		t.Fatalf("expected 502, got %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), "confidence report failed") {
		t.Errorf("body = %s", w.Body.String())
	}
}

// --- handleQuery error branches ---

func TestCovQuery_EnvDepthLimitEnforced(t *testing.T) {
	t.Setenv("CREWSHIP_QUERY_DEPTH", "2")
	srv := NewServer(ServerConfig{
		Addr: "127.0.0.1:0", Logger: covLogger(),
		IPC:         &IPCConfig{BaseURL: "http://127.0.0.1:1", Token: "t"},
		CrewMembers: []CrewMember{{Slug: "nela"}},
	})

	req := httptest.NewRequest("POST", "http://localhost:9119/query",
		strings.NewReader(`{"target":"nela","question":"what db?","from":"viktor"}`))
	w := httptest.NewRecorder()
	srv.handleQuery(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 at env depth limit, got %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), "query depth limit reached") {
		t.Errorf("body = %s", w.Body.String())
	}
}

func TestCovQuery_CrewshipdDown(t *testing.T) {
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	mock.Close()

	srv := NewServer(ServerConfig{
		Addr: "127.0.0.1:0", Logger: covLogger(),
		IPC:         &IPCConfig{BaseURL: mock.URL, Token: "t"},
		CrewMembers: []CrewMember{{Slug: "nela"}},
	})
	req := httptest.NewRequest("POST", "http://localhost:9119/query",
		strings.NewReader(`{"target":"nela","question":"what db?","from":"viktor"}`))
	w := httptest.NewRecorder()
	srv.handleQuery(w, req)

	if w.Code != http.StatusBadGateway {
		t.Fatalf("expected 502, got %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), "query request failed") {
		t.Errorf("body = %s", w.Body.String())
	}
}

func TestCovQuery_InvalidUpstreamResponse(t *testing.T) {
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("not-json"))
	}))
	defer mock.Close()

	srv := NewServer(ServerConfig{
		Addr: "127.0.0.1:0", Logger: covLogger(),
		IPC:         &IPCConfig{BaseURL: mock.URL, Token: "t"},
		CrewMembers: []CrewMember{{Slug: "nela"}},
	})
	req := httptest.NewRequest("POST", "http://localhost:9119/query",
		strings.NewReader(`{"target":"nela","question":"what db?","from":"viktor"}`))
	w := httptest.NewRecorder()
	srv.handleQuery(w, req)

	if w.Code != http.StatusBadGateway {
		t.Fatalf("expected 502, got %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), "invalid response from crewshipd") {
		t.Errorf("body = %s", w.Body.String())
	}
}

// --- handleEscalate error branches ---

func covEscalate(t *testing.T, baseURL string) *httptest.ResponseRecorder {
	t.Helper()
	srv := NewServer(ServerConfig{
		Addr: "127.0.0.1:0", Logger: covLogger(),
		IPC: &IPCConfig{BaseURL: baseURL, Token: "t", CrewID: "crew-1", WorkspaceID: "ws-1", ChatID: "chat-1"},
	})
	req := httptest.NewRequest("POST", "http://localhost:9119/escalate",
		strings.NewReader(`{"from":"nela","reason":"need a decision"}`))
	w := httptest.NewRecorder()
	srv.handleEscalate(w, req)
	return w
}

func TestCovEscalate_CreateRejectedPassthrough(t *testing.T) {
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSONResponse(w, http.StatusForbidden, map[string]string{"error": "escalations disabled"})
	}))
	defer mock.Close()

	w := covEscalate(t, mock.URL)
	if w.Code != http.StatusForbidden {
		t.Fatalf("expected 403 passthrough, got %d: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "escalations disabled") {
		t.Errorf("body = %s", w.Body.String())
	}
}

func TestCovEscalate_MissingEscalationID(t *testing.T) {
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSONResponse(w, http.StatusCreated, map[string]string{"status": "created-but-no-id"})
	}))
	defer mock.Close()

	w := covEscalate(t, mock.URL)
	if w.Code != http.StatusBadGateway {
		t.Fatalf("expected 502, got %d: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "missing escalation_id") {
		t.Errorf("body = %s", w.Body.String())
	}
}

func TestCovEscalate_WaitNon200TreatedAsTimeout(t *testing.T) {
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/wait") {
			writeJSONResponse(w, http.StatusRequestTimeout, map[string]string{"error": "no human responded"})
			return
		}
		writeJSONResponse(w, http.StatusCreated, map[string]string{"escalation_id": "esc-1"})
	}))
	defer mock.Close()

	w := covEscalate(t, mock.URL)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var body map[string]interface{}
	json.Unmarshal(w.Body.Bytes(), &body)
	if body["status"] != "TIMEOUT" || body["escalation_id"] != "esc-1" {
		t.Errorf("body = %+v", body)
	}
}

func TestCovEscalate_WaitInvalidJSONTreatedAsTimeout(t *testing.T) {
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/wait") {
			w.Write([]byte("not json"))
			return
		}
		writeJSONResponse(w, http.StatusCreated, map[string]string{"escalation_id": "esc-2"})
	}))
	defer mock.Close()

	w := covEscalate(t, mock.URL)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var body map[string]interface{}
	json.Unmarshal(w.Body.Bytes(), &body)
	if body["status"] != "TIMEOUT" || body["escalation_id"] != "esc-2" {
		t.Errorf("body = %+v", body)
	}
}

func TestCovEscalate_CreateInvalidJSONResponse(t *testing.T) {
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("}{"))
	}))
	defer mock.Close()

	w := covEscalate(t, mock.URL)
	if w.Code != http.StatusBadGateway {
		t.Fatalf("expected 502, got %d: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "invalid response from crewshipd") {
		t.Errorf("body = %s", w.Body.String())
	}
}

func TestCovEscalate_CrewshipdDown(t *testing.T) {
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	mock.Close()

	w := covEscalate(t, mock.URL)
	if w.Code != http.StatusBadGateway {
		t.Fatalf("expected 502, got %d: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "escalation request failed") {
		t.Errorf("body = %s", w.Body.String())
	}
}

func TestCovReportConfidence_InvalidUpstreamResponse(t *testing.T) {
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("plain text, not json"))
	}))
	defer mock.Close()

	srv := newConfidenceServer(t, &IPCConfig{BaseURL: mock.URL, Token: "t"})
	req := httptest.NewRequest("POST", "http://localhost:9119/report-confidence",
		strings.NewReader(`{"confidence":0.5,"reason":"r"}`))
	w := httptest.NewRecorder()
	srv.handleReportConfidence(w, req)

	if w.Code != http.StatusBadGateway {
		t.Fatalf("expected 502, got %d", w.Code)
	}
	var body map[string]string
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("error body not JSON: %v", err)
	}
	if body["error"] != "invalid response from confidence endpoint" {
		t.Errorf("error = %q", body["error"])
	}
	if body["status_code"] != "200" {
		t.Errorf("status_code = %q", body["status_code"])
	}
}
