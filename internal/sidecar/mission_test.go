package sidecar

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// --- handleMissionCreate ---

func TestHandleMissionCreate_NoIPC(t *testing.T) {
	srv := newQueryServer(t, nil, nil)
	req := httptest.NewRequest(http.MethodPost, "/mission/create",
		strings.NewReader(`{"title":"x"}`))
	w := httptest.NewRecorder()

	srv.handleMissionCreate(w, req)

	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("expected 503, got %d", w.Code)
	}
}

func TestHandleMissionCreate_InvalidJSON(t *testing.T) {
	srv := newQueryServer(t, &IPCConfig{BaseURL: "http://x", Token: "t", CrewID: "c"}, nil)
	req := httptest.NewRequest(http.MethodPost, "/mission/create",
		strings.NewReader(`not-json`))
	w := httptest.NewRecorder()

	srv.handleMissionCreate(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", w.Code)
	}
	var body map[string]string
	json.NewDecoder(w.Body).Decode(&body)
	if body["error"] != "invalid JSON body" {
		t.Errorf("expected 'invalid JSON body', got %q", body["error"])
	}
}

func TestHandleMissionCreate_MissingTitle(t *testing.T) {
	srv := newQueryServer(t, &IPCConfig{BaseURL: "http://x", Token: "t", CrewID: "c"}, nil)
	req := httptest.NewRequest(http.MethodPost, "/mission/create",
		strings.NewReader(`{}`))
	w := httptest.NewRecorder()

	srv.handleMissionCreate(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", w.Code)
	}
	var body map[string]string
	json.NewDecoder(w.Body).Decode(&body)
	if !strings.Contains(body["error"], "title") {
		t.Errorf("expected error about title, got %q", body["error"])
	}
}

func TestHandleMissionCreate_MissingCrewID(t *testing.T) {
	// Neither IPC.CrewID nor request crew_id set.
	srv := newQueryServer(t, &IPCConfig{BaseURL: "http://x", Token: "t"}, nil)
	req := httptest.NewRequest(http.MethodPost, "/mission/create",
		strings.NewReader(`{"title":"Mission Alpha"}`))
	w := httptest.NewRecorder()

	srv.handleMissionCreate(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", w.Code)
	}
	var body map[string]string
	json.NewDecoder(w.Body).Decode(&body)
	if !strings.Contains(body["error"], "crew_id") {
		t.Errorf("expected error about crew_id, got %q", body["error"])
	}
}

func TestHandleMissionCreate_SlugResolvesToAgentID(t *testing.T) {
	var sentBody map[string]interface{}
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		json.Unmarshal(b, &sentBody)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		w.Write([]byte(`{"mission_id":"m1"}`))
	}))
	defer mock.Close()

	members := []CrewMember{
		{ID: "agent-7", Slug: "nela", Name: "Nela"},
	}
	srv := newQueryServer(t, &IPCConfig{
		BaseURL: mock.URL, Token: "tok",
		CrewID: "crew-1", WorkspaceID: "ws-1", AgentID: "lead-1",
	}, members)

	body := `{
		"title":"Build feature",
		"tasks":[{"title":"Spec","assigned_to":"nela","task_order":1}]
	}`
	req := httptest.NewRequest(http.MethodPost, "/mission/create",
		strings.NewReader(body))
	w := httptest.NewRecorder()

	srv.handleMissionCreate(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d; body: %s", w.Code, w.Body.String())
	}
	tasks, _ := sentBody["tasks"].([]interface{})
	if len(tasks) != 1 {
		t.Fatalf("expected 1 task forwarded, got %d", len(tasks))
	}
	tk := tasks[0].(map[string]interface{})
	if tk["assigned_agent_id"] != "agent-7" {
		t.Errorf("expected slug 'nela' resolved to agent-7, got %v", tk["assigned_agent_id"])
	}
}

func TestHandleMissionCreate_UnknownSlugIsBadRequest(t *testing.T) {
	srv := newQueryServer(t, &IPCConfig{
		BaseURL: "http://x", Token: "t", CrewID: "c",
	}, []CrewMember{{ID: "a1", Slug: "alice"}})

	body := `{"title":"x","tasks":[{"title":"t","assigned_to":"ghost"}]}`
	req := httptest.NewRequest(http.MethodPost, "/mission/create", strings.NewReader(body))
	w := httptest.NewRecorder()

	srv.handleMissionCreate(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for unknown slug, got %d", w.Code)
	}
	var resp map[string]string
	json.NewDecoder(w.Body).Decode(&resp)
	if !strings.Contains(resp["error"], "ghost") {
		t.Errorf("expected error to name unknown slug 'ghost', got %q", resp["error"])
	}
}

func TestHandleMissionCreate_AssignedToIDTakesPrecedenceOverSlug(t *testing.T) {
	var sentBody map[string]interface{}
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		json.Unmarshal(b, &sentBody)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		w.Write([]byte(`{}`))
	}))
	defer mock.Close()

	srv := newQueryServer(t, &IPCConfig{
		BaseURL: mock.URL, Token: "t",
		CrewID: "c1", WorkspaceID: "w1", AgentID: "lead",
	}, []CrewMember{{ID: "would-resolve", Slug: "alice"}})

	// Both slug AND id provided → id wins, slug must NOT be used.
	body := `{"title":"x","tasks":[{"title":"t","assigned_to":"alice","assigned_to_id":"explicit-id"}]}`
	req := httptest.NewRequest(http.MethodPost, "/mission/create", strings.NewReader(body))
	w := httptest.NewRecorder()

	srv.handleMissionCreate(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d; body: %s", w.Code, w.Body.String())
	}
	tasks := sentBody["tasks"].([]interface{})
	tk := tasks[0].(map[string]interface{})
	if tk["assigned_agent_id"] != "explicit-id" {
		t.Errorf("expected assigned_to_id to win, got %v", tk["assigned_agent_id"])
	}
}

func TestHandleMissionCreate_UnassignedTaskOmitsAgentID(t *testing.T) {
	var sentBody map[string]interface{}
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		json.Unmarshal(b, &sentBody)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		w.Write([]byte(`{}`))
	}))
	defer mock.Close()

	srv := newQueryServer(t, &IPCConfig{
		BaseURL: mock.URL, Token: "t", CrewID: "c1", WorkspaceID: "w1", AgentID: "lead",
	}, nil)

	body := `{"title":"x","tasks":[{"title":"unassigned","task_order":1}]}`
	req := httptest.NewRequest(http.MethodPost, "/mission/create", strings.NewReader(body))
	w := httptest.NewRecorder()

	srv.handleMissionCreate(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d", w.Code)
	}
	tasks := sentBody["tasks"].([]interface{})
	tk := tasks[0].(map[string]interface{})
	if _, ok := tk["assigned_agent_id"]; ok {
		t.Errorf("unassigned task should omit assigned_agent_id, but got %v", tk["assigned_agent_id"])
	}
}

func TestHandleMissionCreate_ForwardsCoreFields(t *testing.T) {
	var sentBody map[string]interface{}
	var receivedPath string
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedPath = r.URL.Path
		b, _ := io.ReadAll(r.Body)
		json.Unmarshal(b, &sentBody)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		w.Write([]byte(`{"mission_id":"m1"}`))
	}))
	defer mock.Close()

	maxIters := 3
	bodyJSON, _ := json.Marshal(map[string]interface{}{
		"title":       "Build pipeline",
		"description": "from PRD",
		"plan":        "step 1 then step 2",
		"tasks": []map[string]interface{}{
			{
				"title":          "Phase 1",
				"description":    "design",
				"task_order":     1,
				"depends_on":     []string{"prereq"},
				"max_iterations": maxIters,
			},
		},
	})

	srv := newQueryServer(t, &IPCConfig{
		BaseURL: mock.URL, Token: "tok",
		CrewID: "crew-1", WorkspaceID: "ws-7", AgentID: "lead-99",
	}, nil)

	req := httptest.NewRequest(http.MethodPost, "/mission/create",
		strings.NewReader(string(bodyJSON)))
	w := httptest.NewRecorder()

	srv.handleMissionCreate(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d; body: %s", w.Code, w.Body.String())
	}
	if receivedPath != "/api/v1/internal/missions" {
		t.Errorf("expected forwarded path /api/v1/internal/missions, got %q", receivedPath)
	}
	if sentBody["title"] != "Build pipeline" {
		t.Errorf("title not forwarded: %v", sentBody["title"])
	}
	if sentBody["description"] != "from PRD" {
		t.Errorf("description not forwarded: %v", sentBody["description"])
	}
	if sentBody["plan"] != "step 1 then step 2" {
		t.Errorf("plan not forwarded: %v", sentBody["plan"])
	}
	if sentBody["lead_agent_id"] != "lead-99" {
		t.Errorf("lead_agent_id from IPC: got %v", sentBody["lead_agent_id"])
	}
	if sentBody["crew_id"] != "crew-1" {
		t.Errorf("crew_id from IPC: got %v", sentBody["crew_id"])
	}
	if sentBody["workspace_id"] != "ws-7" {
		t.Errorf("workspace_id from IPC: got %v", sentBody["workspace_id"])
	}

	tasks := sentBody["tasks"].([]interface{})
	if len(tasks) != 1 {
		t.Fatalf("expected 1 task, got %d", len(tasks))
	}
	tk := tasks[0].(map[string]interface{})
	if tk["title"] != "Phase 1" {
		t.Errorf("task title: %v", tk["title"])
	}
	if tk["description"] != "design" {
		t.Errorf("task description: %v", tk["description"])
	}
	// task_order is forwarded as float in JSON unmarshal; cast.
	if tk["task_order"].(float64) != 1 {
		t.Errorf("task_order: %v", tk["task_order"])
	}
	if tk["max_iterations"].(float64) != 3 {
		t.Errorf("max_iterations: %v", tk["max_iterations"])
	}
	depends := tk["depends_on"].([]interface{})
	if len(depends) != 1 || depends[0] != "prereq" {
		t.Errorf("depends_on: %v", depends)
	}
}

func TestHandleMissionCreate_OmitsEmptyDescriptionAndPlan(t *testing.T) {
	var sentBody map[string]interface{}
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		json.Unmarshal(b, &sentBody)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		w.Write([]byte(`{}`))
	}))
	defer mock.Close()

	srv := newQueryServer(t, &IPCConfig{
		BaseURL: mock.URL, Token: "t", CrewID: "c", WorkspaceID: "w", AgentID: "a",
	}, nil)

	req := httptest.NewRequest(http.MethodPost, "/mission/create",
		strings.NewReader(`{"title":"x"}`))
	w := httptest.NewRecorder()

	srv.handleMissionCreate(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d", w.Code)
	}
	if _, ok := sentBody["description"]; ok {
		t.Errorf("empty description should be omitted, got %v", sentBody["description"])
	}
	if _, ok := sentBody["plan"]; ok {
		t.Errorf("empty plan should be omitted, got %v", sentBody["plan"])
	}
}

// TestHandleMissionCreate_RequestCrewIDIgnored is the security regression test
// for the cross-crew override vulnerability: a request-supplied crew_id must be
// IGNORED. The sidecar always forwards its trusted IPC crew identity, so a
// compromised agent cannot create a mission in another crew.
func TestHandleMissionCreate_RequestCrewIDIgnored(t *testing.T) {
	var sentBody map[string]interface{}
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		json.Unmarshal(b, &sentBody)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		w.Write([]byte(`{}`))
	}))
	defer mock.Close()

	srv := newQueryServer(t, &IPCConfig{
		BaseURL: mock.URL, Token: "t", CrewID: "ipc-crew", WorkspaceID: "w", AgentID: "a",
	}, nil)

	req := httptest.NewRequest(http.MethodPost, "/mission/create",
		strings.NewReader(`{"title":"x","crew_id":"override-crew"}`))
	w := httptest.NewRecorder()

	srv.handleMissionCreate(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d", w.Code)
	}
	if sentBody["crew_id"] != "ipc-crew" {
		t.Errorf("request crew_id must be ignored; expected trusted IPC crew, got %v", sentBody["crew_id"])
	}
}

// --- handleMissionStart ---

func TestHandleMissionStart_NoIPC(t *testing.T) {
	srv := newQueryServer(t, nil, nil)
	req := httptest.NewRequest(http.MethodPost, "/mission/m1/start", nil)
	w := httptest.NewRecorder()

	srv.handleMissionStart(w, req)

	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("expected 503, got %d", w.Code)
	}
}

func TestHandleMissionStart_MissingMissionID(t *testing.T) {
	srv := newQueryServer(t, &IPCConfig{BaseURL: "http://x", Token: "t"}, nil)

	// "/mission/" → TrimPrefix yields "", TrimSuffix("/start") yields "" → 400.
	// (Note: "/mission/start" is NOT this case — the handler treats "start" as the id.)
	req := httptest.NewRequest(http.MethodPost, "/mission/", nil)
	w := httptest.NewRecorder()

	srv.handleMissionStart(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400 when mission_id missing, got %d", w.Code)
	}
}

func TestHandleMissionStart_PathContainsSlash(t *testing.T) {
	srv := newQueryServer(t, &IPCConfig{BaseURL: "http://x", Token: "t"}, nil)

	// Slash inside the id portion should be rejected.
	req := httptest.NewRequest(http.MethodPost, "/mission/foo/bar/start", nil)
	w := httptest.NewRecorder()

	srv.handleMissionStart(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400 when path has extra slash, got %d", w.Code)
	}
}

func TestHandleMissionStart_ForwardsToCrewshipd(t *testing.T) {
	var receivedPath, receivedMethod string
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedPath = r.URL.Path
		receivedMethod = r.Method
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"status":"IN_PROGRESS"}`))
	}))
	defer mock.Close()

	srv := newQueryServer(t, &IPCConfig{BaseURL: mock.URL, Token: "tok"}, nil)

	req := httptest.NewRequest(http.MethodPost, "/mission/m-123/start", nil)
	w := httptest.NewRecorder()

	srv.handleMissionStart(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d; body: %s", w.Code, w.Body.String())
	}
	if receivedMethod != http.MethodPost {
		t.Errorf("expected POST forward, got %q", receivedMethod)
	}
	if receivedPath != "/api/v1/internal/missions/m-123/start" {
		t.Errorf("expected forwarded path with mission id, got %q", receivedPath)
	}
}

// --- handleMissionStatus ---

func TestHandleMissionStatus_NoIPC(t *testing.T) {
	srv := newQueryServer(t, nil, nil)
	req := httptest.NewRequest(http.MethodGet, "/mission/m1", nil)
	w := httptest.NewRecorder()

	srv.handleMissionStatus(w, req)

	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("expected 503, got %d", w.Code)
	}
}

func TestHandleMissionStatus_MissingMissionID(t *testing.T) {
	srv := newQueryServer(t, &IPCConfig{BaseURL: "http://x", Token: "t"}, nil)

	req := httptest.NewRequest(http.MethodGet, "/mission/", nil)
	w := httptest.NewRecorder()

	srv.handleMissionStatus(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", w.Code)
	}
}

func TestHandleMissionStatus_PathHasExtraSegment(t *testing.T) {
	srv := newQueryServer(t, &IPCConfig{BaseURL: "http://x", Token: "t"}, nil)

	req := httptest.NewRequest(http.MethodGet, "/mission/m-1/extra", nil)
	w := httptest.NewRecorder()

	srv.handleMissionStatus(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for extra path segments, got %d", w.Code)
	}
}

func TestHandleMissionStatus_ForwardsGet(t *testing.T) {
	var receivedPath, receivedMethod string
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedPath = r.URL.Path
		receivedMethod = r.Method
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"id":"m-9","status":"PLANNING"}`))
	}))
	defer mock.Close()

	srv := newQueryServer(t, &IPCConfig{BaseURL: mock.URL, Token: "tok"}, nil)

	req := httptest.NewRequest(http.MethodGet, "/mission/m-9", nil)
	w := httptest.NewRecorder()

	srv.handleMissionStatus(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d; body: %s", w.Code, w.Body.String())
	}
	if receivedMethod != http.MethodGet {
		t.Errorf("expected GET forward, got %q", receivedMethod)
	}
	if receivedPath != "/api/v1/internal/missions/m-9" {
		t.Errorf("expected forwarded path with mission id, got %q", receivedPath)
	}
}

// --- handleMissionTemplates ---

func TestHandleMissionTemplates_ReturnsOrchestratorTemplates(t *testing.T) {
	// No IPC required — templates are static.
	srv := newQueryServer(t, nil, nil)

	req := httptest.NewRequest(http.MethodGet, "/mission/templates", nil)
	w := httptest.NewRecorder()

	srv.handleMissionTemplates(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	if ct := w.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("expected Content-Type=application/json, got %q", ct)
	}
	var templates []map[string]string
	if err := json.NewDecoder(w.Body).Decode(&templates); err != nil {
		t.Fatalf("decode: %v", err)
	}
	// Each template has name + description keys.
	for i, tpl := range templates {
		if tpl["name"] == "" {
			t.Errorf("template[%d] missing name: %+v", i, tpl)
		}
		if _, ok := tpl["description"]; !ok {
			t.Errorf("template[%d] missing description key: %+v", i, tpl)
		}
	}
}
