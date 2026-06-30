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
	want := []string{"save_routine", "list_routines"}
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

// TestRoutinesMCP_ListRoutines_ForwardsToWorkspace verifies list_routines
// hits the workspace pipelines endpoint and returns the list payload.
func TestRoutinesMCP_ListRoutines_ForwardsToWorkspace(t *testing.T) {
	var gotPath string
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
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

	if gotPath != "/api/v1/workspaces/ws-9/pipelines" {
		t.Errorf("path = %q, want workspace pipelines list", gotPath)
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
