package sidecar

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// routine_mcp.go — the in-container MCP server that exposes routine authoring
// (save_routine / list_routines) as native tool calls so the model never has
// to shell out to curl /pipelines/save. tools/call reuses the exact
// savePipeline / listPipelines flow as the HTTP /pipelines/* handlers.
// ---------------------------------------------------------------------------

func newRoutineMCPTestServer(t *testing.T, ipc *IPCConfig) *Server {
	t.Helper()
	if ipc != nil && ipc.AgentID == "" {
		ipc.AgentID = "test-boot-agent"
	}
	return NewServer(ServerConfig{
		Addr:   "127.0.0.1:0",
		IPC:    ipc,
		Logger: pipelinesSilentLogger(),
	})
}

// TestRoutinesMCP_ToolsList_ValidSchema verifies tools/list surfaces
// save_routine + list_routines, and that each descriptor's inputSchema is
// valid JSON Schema (parses + declares type "object"). Adapters key on this
// list; a malformed schema silently breaks the model's tool wiring.
func TestRoutinesMCP_ToolsList_ValidSchema(t *testing.T) {
	s := newRoutineMCPTestServer(t, &IPCConfig{BaseURL: "http://x", Token: "t", WorkspaceID: "ws"})

	req := httptest.NewRequest("POST", "/mcp/routines",
		strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`))
	req.Host = "127.0.0.1:9119"
	w := httptest.NewRecorder()
	s.handleRoutinesMCP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", w.Code, w.Body.String())
	}
	var resp struct {
		Result struct {
			Tools []struct {
				Name        string          `json:"name"`
				Description string          `json:"description"`
				InputSchema json.RawMessage `json:"inputSchema"`
			} `json:"tools"`
		} `json:"result"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	got := make([]string, 0, len(resp.Result.Tools))
	for _, tl := range resp.Result.Tools {
		got = append(got, tl.Name)
		if len(tl.InputSchema) == 0 {
			t.Errorf("tool %q missing inputSchema", tl.Name)
		}
		// inputSchema must be a valid JSON Schema object.
		var schema map[string]any
		if err := json.Unmarshal(tl.InputSchema, &schema); err != nil {
			t.Errorf("tool %q inputSchema not valid JSON: %v", tl.Name, err)
		}
		if schema["type"] != "object" {
			t.Errorf("tool %q inputSchema.type = %v, want object", tl.Name, schema["type"])
		}
	}
	want := []string{"save_routine", "list_routines", "run_routine", "save_page", "discover_capabilities", "validate_manifest"}
	if len(got) != len(want) {
		t.Fatalf("tools = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("tools[%d] = %q, want %q", i, got[i], want[i])
		}
	}

	// save_routine schema must require name + definition.
	var saveSchema struct {
		Required   []string                  `json:"required"`
		Properties map[string]map[string]any `json:"properties"`
	}
	_ = json.Unmarshal(resp.Result.Tools[0].InputSchema, &saveSchema)
	reqSet := map[string]bool{}
	for _, r := range saveSchema.Required {
		reqSet[r] = true
	}
	if !reqSet["name"] || !reqSet["definition"] {
		t.Errorf("save_routine required = %v, want name+definition", saveSchema.Required)
	}
	for _, p := range []string{"name", "description", "definition", "sample_inputs"} {
		if _, ok := saveSchema.Properties[p]; !ok {
			t.Errorf("save_routine schema missing property %q", p)
		}
	}
}

func TestRoutinesMCP_ValidateManifest_UsesShippingSchemaWithoutWriting(t *testing.T) {
	s := newRoutineMCPTestServer(t, &IPCConfig{BaseURL: "http://must-not-be-called", Token: "t", WorkspaceID: "ws"})
	yaml := `apiVersion: crewship/v1
kind: Page
metadata:
  name: Operations
  slug: operations
spec:
  panels:
    - id: services
      schema: status.v1
      owner: crew/ops
      producer: script/watch.sh
      sla: 1m`
	args, _ := json.Marshal(map[string]string{"yaml": yaml})
	body := `{"jsonrpc":"2.0","id":41,"method":"tools/call","params":{"name":"validate_manifest","arguments":` + string(args) + `}}`
	req := httptest.NewRequest(http.MethodPost, "/mcp/routines", strings.NewReader(body))
	req.Host = "127.0.0.1:9119"
	w := httptest.NewRecorder()
	s.handleRoutinesMCP(w, req)

	var resp struct {
		Result struct {
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
			IsError bool `json:"isError"`
		} `json:"result"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Result.IsError || len(resp.Result.Content) != 1 || !strings.Contains(resp.Result.Content[0].Text, `"valid":true`) {
		t.Fatalf("valid Page manifest was refused: %s", w.Body.String())
	}
	if !strings.Contains(resp.Result.Content[0].Text, "No workspace state was changed") {
		t.Fatalf("validation result does not distinguish validate from apply: %s", resp.Result.Content[0].Text)
	}
}

func TestRoutinesMCP_ValidateManifest_ReturnsParserError(t *testing.T) {
	s := newRoutineMCPTestServer(t, &IPCConfig{BaseURL: "http://must-not-be-called", Token: "t", WorkspaceID: "ws"})
	args, _ := json.Marshal(map[string]string{"yaml": "apiVersion: crewship/v1\nkind: Page\nmetadata: {name: Broken, slug: broken}\nspec: {panels: []}"})
	body := `{"jsonrpc":"2.0","id":42,"method":"tools/call","params":{"name":"validate_manifest","arguments":` + string(args) + `}}`
	req := httptest.NewRequest(http.MethodPost, "/mcp/routines", strings.NewReader(body))
	req.Host = "127.0.0.1:9119"
	w := httptest.NewRecorder()
	s.handleRoutinesMCP(w, req)

	var resp struct {
		Result struct {
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
			IsError bool `json:"isError"`
		} `json:"result"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	if !resp.Result.IsError || len(resp.Result.Content) != 1 || !strings.Contains(resp.Result.Content[0].Text, "at least one panel") {
		t.Fatalf("invalid Page manifest did not carry the schema error: %s", w.Body.String())
	}
}

// TestRoutinesMCP_Initialize_ReturnsServerInfo exercises the handshake.
func TestRoutinesMCP_Initialize_ReturnsServerInfo(t *testing.T) {
	s := newRoutineMCPTestServer(t, &IPCConfig{BaseURL: "http://x", Token: "t", WorkspaceID: "ws"})
	req := httptest.NewRequest("POST", "/mcp/routines",
		strings.NewReader(`{"jsonrpc":"2.0","id":0,"method":"initialize","params":{}}`))
	req.Host = "127.0.0.1:9119"
	w := httptest.NewRecorder()
	s.handleRoutinesMCP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	var resp struct {
		Result struct {
			ProtocolVersion string `json:"protocolVersion"`
			ServerInfo      struct {
				Name string `json:"name"`
			} `json:"serverInfo"`
		} `json:"result"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Result.ProtocolVersion == "" {
		t.Error("initialize missing protocolVersion")
	}
	if resp.Result.ServerInfo.Name != RoutinesMCPServerName {
		t.Errorf("serverInfo.name = %q, want %q", resp.Result.ServerInfo.Name, RoutinesMCPServerName)
	}
}

// TestRoutinesMCP_SaveRoutine_HappyPath drives a tools/call save_routine
// through the same test_run→save flow as the HTTP handler and asserts the
// saved routine JSON is returned (isError=false) with IPC author identity.
func TestRoutinesMCP_SaveRoutine_HappyPath(t *testing.T) {
	var saveBody map[string]any
	var sawTestRun bool
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/test_run"):
			sawTestRun = true
			var got map[string]any
			_ = json.NewDecoder(r.Body).Decode(&got)
			if got["author_crew_id"] != "crew-real" {
				t.Errorf("test_run author_crew_id = %v, want crew-real", got["author_crew_id"])
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"status":"COMPLETED"}`))
		case strings.HasSuffix(r.URL.Path, "/internal/pipelines/save"):
			_ = json.NewDecoder(r.Body).Decode(&saveBody)
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"slug":"build-site","saved":true}`))
		default:
			http.Error(w, "unexpected path "+r.URL.Path, http.StatusNotFound)
		}
	}))
	defer mock.Close()

	s := newRoutineMCPTestServer(t, &IPCConfig{
		BaseURL: mock.URL, Token: "t", WorkspaceID: "ws-real",
		CrewID: "crew-real", AgentID: "agent-real", ChatID: "chat-real",
	})
	// Agent forges author_* — must be ignored by the shared savePipeline.
	body := `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{
		"name":"save_routine",
		"arguments":{
			"name":"Build Site",
			"description":"deploy script",
			"definition":{"steps":[]},
			"sample_inputs":{"env":"dev"},
			"author_crew_id":"crew-FORGED"
		}}}`
	req := httptest.NewRequest("POST", "/mcp/routines", strings.NewReader(body))
	req.Host = "127.0.0.1:9119"
	w := httptest.NewRecorder()
	s.handleRoutinesMCP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	if !sawTestRun {
		t.Error("test_run was never called")
	}
	var resp struct {
		Result struct {
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
			IsError bool `json:"isError"`
		} `json:"result"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Result.IsError {
		t.Errorf("isError=true on happy path; content=%v", resp.Result.Content)
	}
	if len(resp.Result.Content) == 0 || !strings.Contains(resp.Result.Content[0].Text, "build-site") {
		t.Errorf("content should carry saved routine JSON, got %+v", resp.Result.Content)
	}
	if saveBody["author_crew_id"] != "crew-real" {
		t.Errorf("save author_crew_id = %v, want crew-real (forged value overwritten)", saveBody["author_crew_id"])
	}
	if saveBody["slug"] != "build-site" {
		t.Errorf("slug = %v, want build-site", saveBody["slug"])
	}
}

// TestRoutinesMCP_SaveRoutine_BadDSL_ReturnsIsError verifies a test_run
// failure surfaces as a recoverable MCP tool error (isError=true) carrying
// the upstream validation message, so the model can fix the DSL and retry.
func TestRoutinesMCP_SaveRoutine_BadDSL_ReturnsIsError(t *testing.T) {
	var savedHit bool
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/test_run") {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":"unknown step type"}`))
			return
		}
		savedHit = true
		w.WriteHeader(http.StatusOK)
	}))
	defer mock.Close()

	s := newRoutineMCPTestServer(t, &IPCConfig{BaseURL: mock.URL, Token: "t", WorkspaceID: "ws", CrewID: "c", AgentID: "a"})
	body := `{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{
		"name":"save_routine",
		"arguments":{"name":"my pipe","definition":{"steps":[{"kind":"bogus"}]}}}}`
	req := httptest.NewRequest("POST", "/mcp/routines", strings.NewReader(body))
	req.Host = "127.0.0.1:9119"
	w := httptest.NewRecorder()
	s.handleRoutinesMCP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}
	if savedHit {
		t.Error("save was incorrectly invoked after test_run failure")
	}
	var resp struct {
		Result struct {
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
			IsError bool `json:"isError"`
		} `json:"result"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !resp.Result.IsError {
		t.Fatalf("expected isError=true on bad DSL, got result=%+v", resp.Result)
	}
	if len(resp.Result.Content) == 0 || !strings.Contains(resp.Result.Content[0].Text, "unknown step type") {
		t.Errorf("content should carry upstream validation error, got %+v", resp.Result.Content)
	}
}

// TestRoutinesMCP_SaveRoutine_MissingDefinition_IsError verifies the local
// validation gate (name+definition required) surfaces through the tool.
func TestRoutinesMCP_SaveRoutine_MissingDefinition_IsError(t *testing.T) {
	s := newRoutineMCPTestServer(t, &IPCConfig{BaseURL: "http://x", Token: "t", WorkspaceID: "ws"})
	body := `{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{
		"name":"save_routine","arguments":{"name":"only a name"}}}`
	req := httptest.NewRequest("POST", "/mcp/routines", strings.NewReader(body))
	req.Host = "127.0.0.1:9119"
	w := httptest.NewRecorder()
	s.handleRoutinesMCP(w, req)

	var resp struct {
		Result struct {
			IsError bool `json:"isError"`
		} `json:"result"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	if !resp.Result.IsError {
		t.Fatalf("expected isError=true when definition missing, got %s", w.Body.String())
	}
}

// TestRoutinesMCP_SavePage_HappyPath verifies save_page forwards to the
// internal page-save route with identity injected from IPC — mirroring
// TestRoutinesMCP_SaveRoutine_HappyPath's forged-field assertion: the agent
// cannot claim a different crew_id than the one it is actually bound to.
func TestRoutinesMCP_SavePage_HappyPath(t *testing.T) {
	var saveBody map[string]any
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/internal/pages/save") {
			http.Error(w, "unexpected path "+r.URL.Path, http.StatusNotFound)
			return
		}
		_ = json.NewDecoder(r.Body).Decode(&saveBody)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"slug":"ops-status","owner":"crew/eng"}`))
	}))
	defer mock.Close()

	s := newRoutineMCPTestServer(t, &IPCConfig{
		BaseURL: mock.URL, Token: "t", WorkspaceID: "ws-real",
		CrewID: "crew-real", AgentID: "agent-real", ChatID: "chat-real",
	})
	// Agent forges crew_id — must be ignored by the shared savePage (it is
	// not even in pagesSaveRequest, so there is nothing to forge here, but
	// the assertion below still proves the injected value is IPC's own).
	body := `{"jsonrpc":"2.0","id":10,"method":"tools/call","params":{
		"name":"save_page",
		"arguments":{
			"name":"Ops Status",
			"description":"status board",
			"panels":[{"id":"p1","schema":"status.v1","owner":"crew/eng","producer":"agent/lead","sla_seconds":30,"span":4}]
		}}}`
	req := httptest.NewRequest("POST", "/mcp/routines", strings.NewReader(body))
	req.Host = "127.0.0.1:9119"
	w := httptest.NewRecorder()
	s.handleRoutinesMCP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	var resp struct {
		Result struct {
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
			IsError bool `json:"isError"`
		} `json:"result"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Result.IsError {
		t.Errorf("isError=true on happy path; content=%v", resp.Result.Content)
	}
	if len(resp.Result.Content) == 0 || !strings.Contains(resp.Result.Content[0].Text, "ops-status") {
		t.Errorf("content should carry saved page JSON, got %+v", resp.Result.Content)
	}
	if saveBody["workspace_id"] != "ws-real" {
		t.Errorf("save workspace_id = %v, want ws-real", saveBody["workspace_id"])
	}
	if saveBody["crew_id"] != "crew-real" {
		t.Errorf("save crew_id = %v, want crew-real (from IPC, agent cannot set it)", saveBody["crew_id"])
	}
	if saveBody["agent_id"] != "agent-real" {
		t.Errorf("save agent_id = %v, want agent-real (the acting agent)", saveBody["agent_id"])
	}
}

// TestRoutinesMCP_SavePage_HeldByPolicy_ReturnsIsError verifies a held
// (403, pending_review) response from the internal save route surfaces as a
// recoverable MCP tool error, so the model tells the user the page needs
// operator approval instead of claiming it was created.
func TestRoutinesMCP_SavePage_HeldByPolicy_ReturnsIsError(t *testing.T) {
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"error":"Page creation held by policy","pending_review":true}`))
	}))
	defer mock.Close()

	s := newRoutineMCPTestServer(t, &IPCConfig{BaseURL: mock.URL, Token: "t", WorkspaceID: "ws", CrewID: "c", AgentID: "a"})
	body := `{"jsonrpc":"2.0","id":11,"method":"tools/call","params":{
		"name":"save_page",
		"arguments":{"name":"Ops Status","panels":[{"id":"p1","schema":"status.v1"}]}}}`
	req := httptest.NewRequest("POST", "/mcp/routines", strings.NewReader(body))
	req.Host = "127.0.0.1:9119"
	w := httptest.NewRecorder()
	s.handleRoutinesMCP(w, req)

	var resp struct {
		Result struct {
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
			IsError bool `json:"isError"`
		} `json:"result"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !resp.Result.IsError {
		t.Fatalf("expected isError=true when held by policy, got %s", w.Body.String())
	}
	if len(resp.Result.Content) == 0 || !strings.Contains(resp.Result.Content[0].Text, "pending_review") {
		t.Errorf("content should carry the held response verbatim, got %+v", resp.Result.Content)
	}
}

// TestRoutinesMCP_SavePage_MissingPanels_IsError verifies the local
// validation gate (name+panels required) surfaces through the tool without
// ever reaching IPC.
func TestRoutinesMCP_SavePage_MissingPanels_IsError(t *testing.T) {
	s := newRoutineMCPTestServer(t, &IPCConfig{BaseURL: "http://must-not-be-called", Token: "t", WorkspaceID: "ws"})
	body := `{"jsonrpc":"2.0","id":12,"method":"tools/call","params":{
		"name":"save_page","arguments":{"name":"only a name"}}}`
	req := httptest.NewRequest("POST", "/mcp/routines", strings.NewReader(body))
	req.Host = "127.0.0.1:9119"
	w := httptest.NewRecorder()
	s.handleRoutinesMCP(w, req)

	var resp struct {
		Result struct {
			IsError bool `json:"isError"`
		} `json:"result"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	if !resp.Result.IsError {
		t.Fatalf("expected isError=true when panels missing, got %s", w.Body.String())
	}
}

// TestRoutinesMCP_ListRoutines_ForwardsToWorkspace verifies list_routines
// hits the INTERNAL pipelines endpoint and returns the list payload.
//
// The mock upstream returns 200 for anything, so this proves which route
// is called and nothing about whether the server accepts it — which is
// exactly how #1763 stayed invisible: the tool aimed at the public,
// JWT-authed route while the sidecar carries only X-Internal-Token, so
// every real call answered 401 while this stayed green. The real-
// middleware counterpart is in internal/api/internal_pipelines_read_test.go.
func TestRoutinesMCP_ListRoutines_ForwardsToWorkspace(t *testing.T) {
	var gotPath, gotWorkspace string
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotWorkspace = r.URL.Query().Get("workspace_id")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"items":[{"slug":"daily-report"}]}`))
	}))
	defer mock.Close()

	s := newRoutineMCPTestServer(t, &IPCConfig{BaseURL: mock.URL, Token: "t", WorkspaceID: "ws-9"})
	body := `{"jsonrpc":"2.0","id":5,"method":"tools/call","params":{"name":"list_routines","arguments":{}}}`
	req := httptest.NewRequest("POST", "/mcp/routines", strings.NewReader(body))
	req.Host = "127.0.0.1:9119"
	w := httptest.NewRecorder()
	s.handleRoutinesMCP(w, req)

	if gotPath != "/api/v1/internal/pipelines" {
		t.Errorf("path = %q, want the internal pipelines list", gotPath)
	}
	// The internal route takes the workspace from the query and answers
	// 400 without it. Omitting it would turn every list into a failure —
	// and the workspace no longer travels in the path, so nothing else
	// carries it.
	if gotWorkspace != "ws-9" {
		t.Errorf("workspace_id = %q, want ws-9", gotWorkspace)
	}
	var resp struct {
		Result struct {
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
			IsError bool `json:"isError"`
		} `json:"result"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	if resp.Result.IsError {
		t.Error("list_routines should not be an error on 200")
	}
	if len(resp.Result.Content) == 0 || !strings.Contains(resp.Result.Content[0].Text, "daily-report") {
		t.Errorf("list content = %+v, want the routine list", resp.Result.Content)
	}
}

// TestRoutinesMCP_RunRoutine_ForwardsToInternalRun verifies run_routine
// forwards to the internal run endpoint with workspace + invoker identity
// injected from IPC (never the caller), and returns the run result payload.
func TestRoutinesMCP_RunRoutine_ForwardsToInternalRun(t *testing.T) {
	var gotPath string
	var gotBody map[string]any
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"run_id":"run-123","status":"COMPLETED"}`))
	}))
	defer mock.Close()

	s := newRoutineMCPTestServer(t, &IPCConfig{
		BaseURL: mock.URL, Token: "t", WorkspaceID: "ws-real",
		CrewID: "crew-real", AgentID: "agent-real",
	})
	// Agent forges invoker identity — must be ignored; IPC values win.
	body := `{"jsonrpc":"2.0","id":8,"method":"tools/call","params":{
		"name":"run_routine",
		"arguments":{
			"slug":"daily-report",
			"inputs":{"date":"2026-06-30"},
			"invoking_crew_id":"crew-FORGED",
			"workspace_id":"ws-FORGED"
		}}}`
	req := httptest.NewRequest("POST", "/mcp/routines", strings.NewReader(body))
	req.Host = "127.0.0.1:9119"
	w := httptest.NewRecorder()
	s.handleRoutinesMCP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	if gotPath != "/api/v1/internal/pipelines/run" {
		t.Errorf("path = %q, want /api/v1/internal/pipelines/run", gotPath)
	}
	if gotBody["workspace_id"] != "ws-real" {
		t.Errorf("workspace_id = %v, want ws-real (IPC value, forged ignored)", gotBody["workspace_id"])
	}
	if gotBody["invoking_crew_id"] != "crew-real" {
		t.Errorf("invoking_crew_id = %v, want crew-real (forged value overwritten)", gotBody["invoking_crew_id"])
	}
	if gotBody["slug"] != "daily-report" {
		t.Errorf("slug = %v, want daily-report", gotBody["slug"])
	}
	var resp struct {
		Result struct {
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
			IsError bool `json:"isError"`
		} `json:"result"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Result.IsError {
		t.Errorf("isError=true on happy run; content=%v", resp.Result.Content)
	}
	if len(resp.Result.Content) == 0 || !strings.Contains(resp.Result.Content[0].Text, "run-123") {
		t.Errorf("content should carry the run result, got %+v", resp.Result.Content)
	}
}

// TestRoutinesMCP_RunRoutine_MissingSlug_IsError verifies the local guard
// (slug required) surfaces as a recoverable MCP tool error.
func TestRoutinesMCP_RunRoutine_MissingSlug_IsError(t *testing.T) {
	s := newRoutineMCPTestServer(t, &IPCConfig{BaseURL: "http://x", Token: "t", WorkspaceID: "ws"})
	body := `{"jsonrpc":"2.0","id":9,"method":"tools/call","params":{
		"name":"run_routine","arguments":{"inputs":{"a":1}}}}`
	req := httptest.NewRequest("POST", "/mcp/routines", strings.NewReader(body))
	req.Host = "127.0.0.1:9119"
	w := httptest.NewRecorder()
	s.handleRoutinesMCP(w, req)

	var resp struct {
		Result struct {
			IsError bool `json:"isError"`
		} `json:"result"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	if !resp.Result.IsError {
		t.Fatalf("expected isError=true when slug missing, got %s", w.Body.String())
	}
}

// TestRoutinesMCP_UnknownTool_IsError verifies an unknown tool name is a
// recoverable MCP tool error, not a JSON-RPC fatal.
func TestRoutinesMCP_UnknownTool_IsError(t *testing.T) {
	s := newRoutineMCPTestServer(t, &IPCConfig{BaseURL: "http://x", Token: "t", WorkspaceID: "ws"})
	body := `{"jsonrpc":"2.0","id":6,"method":"tools/call","params":{"name":"delete_everything","arguments":{}}}`
	req := httptest.NewRequest("POST", "/mcp/routines", strings.NewReader(body))
	req.Host = "127.0.0.1:9119"
	w := httptest.NewRecorder()
	s.handleRoutinesMCP(w, req)

	var resp struct {
		Result struct {
			IsError bool `json:"isError"`
		} `json:"result"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	if !resp.Result.IsError {
		t.Fatalf("unknown tool should be isError=true, got %s", w.Body.String())
	}
}

// TestRoutinesMCP_UnknownMethod_MethodNotFound mirrors the memory server's
// JSON-RPC -32601 contract for an unrouted method.
func TestRoutinesMCP_UnknownMethod_MethodNotFound(t *testing.T) {
	s := newRoutineMCPTestServer(t, &IPCConfig{BaseURL: "http://x", Token: "t", WorkspaceID: "ws"})
	req := httptest.NewRequest("POST", "/mcp/routines",
		strings.NewReader(`{"jsonrpc":"2.0","id":7,"method":"resources/list"}`))
	req.Host = "127.0.0.1:9119"
	w := httptest.NewRecorder()
	s.handleRoutinesMCP(w, req)

	var resp struct {
		Error *struct {
			Code int `json:"code"`
		} `json:"error"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	if resp.Error == nil || resp.Error.Code != -32601 {
		t.Fatalf("want -32601 method not found, got %s", w.Body.String())
	}
}

// TestRoutinesMCP_SaveRoutine_ForwardsTargetCrewOnBothLegs pins the one
// identity field an agent IS allowed to supply, and the reason it has to
// ride on the test_run leg as well as the save leg.
//
// The save_token the internal test_run mints is signed over the AUTHORING
// crew, and InternalSave re-derives that HMAC over the crew the routine
// actually lands on. Sending target_crew_slug only on the save would dry-run
// as the Guide, mint a token bound to the Guide, and then fail verification
// at the crew the routine is FOR — a signature error, in a flow whose real
// failure was ownership. Both legs or neither.
func TestRoutinesMCP_SaveRoutine_ForwardsTargetCrewOnBothLegs(t *testing.T) {
	var testRunTarget, saveTarget any
	var saveCrewID any
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var got map[string]any
		_ = json.NewDecoder(r.Body).Decode(&got)
		switch {
		case strings.HasSuffix(r.URL.Path, "/test_run"):
			testRunTarget = got["target_crew_slug"]
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"status":"DRY_RUN_OK","save_token":"tok"}`))
		case strings.HasSuffix(r.URL.Path, "/internal/pipelines/save"):
			saveTarget = got["target_crew_slug"]
			saveCrewID = got["author_crew_id"]
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"slug":"uptime","saved":true}`))
		default:
			http.Error(w, "unexpected path "+r.URL.Path, http.StatusNotFound)
		}
	}))
	defer mock.Close()

	s := newRoutineMCPTestServer(t, &IPCConfig{
		BaseURL: mock.URL, Token: "t", WorkspaceID: "ws-real",
		CrewID: "crew-guide", AgentID: "agent-guide", ChatID: "chat-1",
	})
	body := `{"jsonrpc":"2.0","id":7,"method":"tools/call","params":{
		"name":"save_routine",
		"arguments":{
			"name":"Uptime",
			"definition":{"steps":[]},
			"crew":"hlidac-dostupnosti"
		}}}`
	req := httptest.NewRequest("POST", "/mcp/routines", strings.NewReader(body))
	req.Host = "127.0.0.1:9119"
	w := httptest.NewRecorder()
	s.handleRoutinesMCP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	if testRunTarget != "hlidac-dostupnosti" {
		t.Errorf("test_run target_crew_slug = %v, want hlidac-dostupnosti", testRunTarget)
	}
	if saveTarget != "hlidac-dostupnosti" {
		t.Errorf("save target_crew_slug = %v, want hlidac-dostupnosti", saveTarget)
	}
	// The caller's own identity still comes from IPC and is unaffected — the
	// API decides whether the delegation is allowed, and it needs to know who
	// is asking.
	if saveCrewID != "crew-guide" {
		t.Errorf("author_crew_id = %v, want crew-guide (the caller, from IPC)", saveCrewID)
	}
}

// The same field on save_page, plus the read tool that has to agree with it:
// a page's producer refs name the TARGET crew's agents, so asking
// discover_capabilities about the caller would hand the model the wrong
// roster and every ref it wrote would fail to resolve.
func TestRoutinesMCP_SavePageAndDiscover_CarryTheTargetCrew(t *testing.T) {
	var pageTarget any
	var capsQuery string
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/internal/pages/save"):
			var got map[string]any
			_ = json.NewDecoder(r.Body).Decode(&got)
			pageTarget = got["target_crew_slug"]
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"slug":"dostupnost"}`))
		case strings.Contains(r.URL.Path, "/capabilities"):
			capsQuery = r.URL.RawQuery
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"agents":[]}`))
		default:
			http.Error(w, "unexpected path "+r.URL.Path, http.StatusNotFound)
		}
	}))
	defer mock.Close()

	s := newRoutineMCPTestServer(t, &IPCConfig{
		BaseURL: mock.URL, Token: "t", WorkspaceID: "ws-real",
		CrewID: "crew-guide", AgentID: "agent-guide",
	})

	call := func(payload string) {
		t.Helper()
		req := httptest.NewRequest("POST", "/mcp/routines", strings.NewReader(payload))
		req.Host = "127.0.0.1:9119"
		w := httptest.NewRecorder()
		s.handleRoutinesMCP(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
		}
	}

	call(`{"jsonrpc":"2.0","id":8,"method":"tools/call","params":{
		"name":"save_page",
		"arguments":{"name":"Dostupnost","panels":[{"id":"p1"}],"crew":"hlidac-dostupnosti"}}}`)
	if pageTarget != "hlidac-dostupnosti" {
		t.Errorf("save_page target_crew_slug = %v, want hlidac-dostupnosti", pageTarget)
	}

	call(`{"jsonrpc":"2.0","id":9,"method":"tools/call","params":{
		"name":"discover_capabilities","arguments":{"crew":"hlidac-dostupnosti"}}}`)
	if !strings.Contains(capsQuery, "target_crew_slug=hlidac-dostupnosti") {
		t.Errorf("capabilities query = %q, want it to carry target_crew_slug", capsQuery)
	}
}

// Omitting `crew` must stay exactly as it was: an ordinary crew authoring for
// itself is the overwhelmingly common case, and an empty target_crew_slug
// must not read as a delegation attempt on the API side.
func TestRoutinesMCP_DiscoverWithoutCrew_AsksAboutItself(t *testing.T) {
	var capsQuery string
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capsQuery = r.URL.RawQuery
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"agents":[]}`))
	}))
	defer mock.Close()

	s := newRoutineMCPTestServer(t, &IPCConfig{
		BaseURL: mock.URL, Token: "t", WorkspaceID: "ws-real", CrewID: "crew-own",
	})
	req := httptest.NewRequest("POST", "/mcp/routines", strings.NewReader(
		`{"jsonrpc":"2.0","id":10,"method":"tools/call","params":{"name":"discover_capabilities"}}`))
	req.Host = "127.0.0.1:9119"
	w := httptest.NewRecorder()
	s.handleRoutinesMCP(w, req)

	if strings.Contains(capsQuery, "target_crew_slug") {
		t.Errorf("query = %q, must not carry an empty target_crew_slug", capsQuery)
	}
}
