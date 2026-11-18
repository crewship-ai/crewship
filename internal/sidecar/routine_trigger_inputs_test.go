package sidecar

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/pipeline"
	"github.com/crewship-ai/crewship/internal/testutil"
)

// Exercise the advertised MCP schema and its real JSON/IPC/store round trip.
// Only the host HTTP authorization/test-token boundary is a test stub; the
// schedule preset validation and SQLite persistence use the production store.
func TestRoutinesMCP_TriggerPresetInputs_RoundTrip(t *testing.T) {
	db := testutil.MigratedSQLDB(t)
	for _, query := range []string{
		`INSERT INTO workspaces(id,name,slug) VALUES('ws-mcp','MCP','mcp')`,
		`INSERT INTO crews(id,workspace_id,name,slug) VALUES('crew-mcp','ws-mcp','Crew','crew')`,
		`INSERT INTO agents(id,workspace_id,crew_id,name,slug) VALUES('agent-mcp','ws-mcp','crew-mcp','Agent','agent')`,
	} {
		if _, err := db.Exec(query); err != nil {
			t.Fatal(err)
		}
	}
	store := pipeline.NewStore(db)
	var scheduleID string
	host := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/api/v1/internal/pipelines/test_run" {
			_, _ = w.Write([]byte(`{"status":"DRY_RUN_OK","save_token":"test-host-token"}`))
			return
		}
		if r.URL.Path != "/api/v1/internal/pipelines/save" {
			http.NotFound(w, r)
			return
		}
		var body struct {
			WorkspaceID string                `json:"workspace_id"`
			Slug        string                `json:"slug"`
			Name        string                `json:"name"`
			CrewID      string                `json:"author_crew_id"`
			AgentID     string                `json:"author_agent_id"`
			Definition  json.RawMessage       `json:"definition"`
			Trigger     pipelinesTriggerInput `json:"trigger"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
			http.Error(w, "invalid body", http.StatusBadRequest)
			return
		}
		now := time.Now()
		_, schedule, err := store.SaveWithTrigger(r.Context(), pipeline.SaveInput{
			WorkspaceID: body.WorkspaceID, Slug: body.Slug, Name: body.Name,
			DefinitionJSON: string(body.Definition), DSLVersion: "1.0",
			Author:        pipeline.AuthorMeta{CrewID: body.CrewID, AgentID: body.AgentID, Via: pipeline.AuthoredViaAgent},
			LastTestRunAt: &now, LastTestRunPassed: true,
		}, &pipeline.TriggerInput{Kind: pipeline.TriggerKind(body.Trigger.Kind), CronExpr: body.Trigger.CronExpr,
			Timezone: body.Trigger.Timezone, Inputs: body.Trigger.Inputs})
		if err != nil {
			http.Error(w, err.Error(), http.StatusUnprocessableEntity)
			return
		}
		scheduleID = schedule.ID
		_ = json.NewEncoder(w).Encode(map[string]any{"slug": body.Slug, "trigger": map[string]any{"schedule_id": scheduleID}})
	}))
	defer host.Close()
	s := newRoutineMCPTestServer(t, &IPCConfig{BaseURL: host.URL, Token: "test", WorkspaceID: "ws-mcp", CrewID: "crew-mcp", AgentID: "agent-mcp"})
	call := func(method string, params any) map[string]any {
		encoded, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": method, "params": params})
		if err != nil {
			t.Fatal(err)
		}
		req := httptest.NewRequest(http.MethodPost, "/mcp/routines", strings.NewReader(string(encoded)))
		req.Host = "127.0.0.1:9119"
		rr := httptest.NewRecorder()
		s.handleRoutinesMCP(rr, req)
		var result map[string]any
		if err := json.Unmarshal(rr.Body.Bytes(), &result); err != nil {
			t.Fatalf("MCP result: %v %s", err, rr.Body.String())
		}
		return result["result"].(map[string]any)
	}
	tools := call("tools/list", map[string]any{})["tools"].([]any)
	var presetSchema map[string]any
	for _, raw := range tools {
		tool := raw.(map[string]any)
		if tool["name"] == "save_routine" {
			properties := tool["inputSchema"].(map[string]any)["properties"].(map[string]any)
			trigger := properties["trigger"].(map[string]any)["properties"].(map[string]any)
			presetSchema, _ = trigger["inputs"].(map[string]any)
		}
	}
	if presetSchema["type"] != "object" || presetSchema["additionalProperties"] != true {
		t.Fatalf("save_routine does not advertise a JSON-object trigger preset: %v", presetSchema)
	}
	definition := json.RawMessage(`{"dsl_version":"1.0","name":"MCP preset","agentless":true,"inputs":[{"name":"source","type":"string"},{"name":"count","type":"integer","min":1,"max":5}],"steps":[{"id":"copy","type":"transform","transform":{"input":"{{ inputs.source }}","expression":"."}}]}`)
	preset := map[string]any{"source": "cron", "count": float64(3), "enabled": true, "nested": map[string]any{"tags": []any{"one", "two"}}}
	trigger := map[string]any{"kind": "schedule", "cron": "0 9 * * *", "timezone": "UTC", "inputs": preset}
	arguments := map[string]any{"name": "MCP preset", "definition": definition, "sample_inputs": map[string]any{"source": "sample-only", "count": 1}, "trigger": trigger}
	result := call("tools/call", map[string]any{"name": "save_routine", "arguments": arguments})
	if failed, _ := result["isError"].(bool); failed || scheduleID == "" {
		t.Fatalf("valid preset save failed: %v", result)
	}
	saved, err := pipeline.NewScheduleStore(db).GetByID(t.Context(), scheduleID)
	if err != nil {
		t.Fatal(err)
	}
	var actual map[string]any
	if err := json.Unmarshal([]byte(saved.InputsJSON), &actual); err != nil || !reflect.DeepEqual(actual, preset) {
		t.Fatalf("persisted schedule inputs = %v err=%v, want %v; sample inputs must not replace presets", actual, err, preset)
	}
	// Retain the existing backend form bounds; the schema addition bypasses none.
	preset["count"] = 6
	result = call("tools/call", map[string]any{"name": "save_routine", "arguments": arguments})
	if failed, _ := result["isError"].(bool); !failed {
		t.Fatalf("out-of-bounds preset accepted: %v", result)
	}
	trigger["inputs"] = []any{"not-an-object"}
	result = call("tools/call", map[string]any{"name": "save_routine", "arguments": arguments})
	if failed, _ := result["isError"].(bool); !failed {
		t.Fatalf("non-object trigger inputs accepted: %v", result)
	}
	var persisted string
	if err := db.QueryRow(`SELECT inputs_json FROM pipeline_schedules WHERE id=?`, scheduleID).Scan(&persisted); err != nil || persisted != saved.InputsJSON {
		t.Fatalf("rejected preset changed the saved schedule: %s err=%v", persisted, err)
	}
}
