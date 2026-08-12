package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// emitRunRowWithMeta writes a run.started + run.completed pair whose terminal
// payload carries `metaJSON` as its metadata object — the shape the run driver
// persists once it has merged the CLI's session-init provenance in.
func (f *runsTestFixture) emitRunRowWithMeta(t *testing.T, traceID, metaJSON string, when time.Time) {
	t.Helper()
	ctx := context.Background()
	insertJournal := func(id, kind string, ts time.Time, payload string) {
		_, err := f.h.db.ExecContext(ctx, `
			INSERT INTO journal_entries
				(id, workspace_id, agent_id, ts, entry_type, severity, priority, actor_type, actor_id, summary, payload, refs, trace_id)
			VALUES (?, ?, ?, ?, ?, 'info', 'normal', 'sidecar', ?, 'r', ?, '{}', ?)`,
			id, f.wsID, f.agent, ts.UTC().Format("2006-01-02T15:04:05.000Z"),
			kind, f.agent, payload, traceID)
		if err != nil {
			t.Fatalf("insert %s/%s: %v", kind, traceID, err)
		}
	}
	insertJournal(traceID+"_s", "run.started", when, `{"trigger_type":"USER"}`)
	insertJournal(traceID+"_t", "run.completed", when.Add(time.Minute),
		`{"exit_code":0,"metadata":`+metaJSON+`}`)
}

// TestRunHandler_List_SurfacesSessionProvenance asserts the provenance keys
// reach the run JSON, and — just as important — that a run without them omits
// the keys entirely rather than serialising empty strings a consumer would
// have to tell apart from "recorded as empty".
func TestRunHandler_List_SurfacesSessionProvenance(t *testing.T) {
	f := newRunsTestFixture(t)
	now := time.Now().UTC()
	f.emitRunRowWithMeta(t, "run_prov", `{
		"model":"claude-sonnet-4-5",
		"cli_version":"2.1.204",
		"api_key_source":"ANTHROPIC_API_KEY",
		"permission_mode":"bypassPermissions",
		"session_id":"sess-9f1c",
		"mcp_server_errors":[{"name":"crewship-memory","type":"config_error","message":"no such file"}]
	}`, now.Add(-2*time.Minute))
	f.emitRunRow(t, "run_plain", "COMPLETED", "USER", now.Add(-time.Minute))

	req := httptest.NewRequest("GET", "/api/v1/runs", nil)
	req = withWorkspaceUser(req, f.user, f.wsID, "OWNER")
	rr := httptest.NewRecorder()
	f.h.List(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	var resp runListResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	byID := map[string]runResponse{}
	for _, r := range resp.Data {
		byID[r.ID] = r
	}

	got, ok := byID["run_prov"]
	if !ok {
		t.Fatalf("run_prov missing from response: %+v", resp.Data)
	}
	for _, tc := range []struct {
		field string
		got   *string
		want  string
	}{
		{"cli_version", got.CLIVersion, "2.1.204"},
		{"api_key_source", got.APIKeySource, "ANTHROPIC_API_KEY"},
		{"permission_mode", got.PermissionMode, "bypassPermissions"},
		{"session_id", got.SessionID, "sess-9f1c"},
	} {
		if tc.got == nil || *tc.got != tc.want {
			t.Errorf("run_prov.%s = %v, want %q", tc.field, tc.got, tc.want)
		}
	}
	if len(got.MCPServerErrors) != 1 {
		t.Fatalf("run_prov.mcp_server_errors = %+v, want one entry", got.MCPServerErrors)
	}
	if got.MCPServerErrors[0].Name != "crewship-memory" || got.MCPServerErrors[0].Type != "config_error" {
		t.Errorf("run_prov.mcp_server_errors[0] = %+v, want crewship-memory/config_error", got.MCPServerErrors[0])
	}

	plain, ok := byID["run_plain"]
	if !ok {
		t.Fatal("run_plain missing from response")
	}
	if plain.CLIVersion != nil || plain.APIKeySource != nil || plain.PermissionMode != nil || plain.SessionID != nil {
		t.Errorf("run_plain carried provenance it never recorded: %+v", plain)
	}
	if plain.MCPServerErrors != nil {
		t.Errorf("run_plain.mcp_server_errors = %+v, want nil", plain.MCPServerErrors)
	}

	// Absent means absent on the wire too — a consumer keys off the missing
	// key, so an explicit null/"" would change what "nothing was skipped" and
	// "no session-init reported" look like.
	var raw struct {
		Data []map[string]json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &raw); err != nil {
		t.Fatalf("decode raw: %v", err)
	}
	for _, row := range raw.Data {
		if string(row["id"]) != `"run_plain"` {
			continue
		}
		for _, key := range []string{"cli_version", "api_key_source", "permission_mode", "session_id", "mcp_server_errors"} {
			if _, present := row[key]; present {
				t.Errorf("run_plain serialised %q, want the key omitted", key)
			}
		}
	}
}

// TestRunHandler_Get_SurfacesSessionProvenance covers the per-run endpoint
// `crewship run get` reads — the detail surface for these fields.
func TestRunHandler_Get_SurfacesSessionProvenance(t *testing.T) {
	f := newRunsTestFixture(t)
	f.emitRunRowWithMeta(t, "run_one", `{
		"cli_version":"2.1.219",
		"mcp_server_errors":[{"name":"sentry","type":"connection_error","message":"timeout"}]
	}`, time.Now().UTC().Add(-time.Minute))

	req := httptest.NewRequest("GET", "/api/v1/runs/run_one", nil)
	req.SetPathValue("id", "run_one")
	req = withWorkspaceUser(req, f.user, f.wsID, "OWNER")
	rr := httptest.NewRecorder()
	f.h.Get(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	var got runResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.CLIVersion == nil || *got.CLIVersion != "2.1.219" {
		t.Errorf("cli_version = %v, want 2.1.219", got.CLIVersion)
	}
	if len(got.MCPServerErrors) != 1 || got.MCPServerErrors[0].Message != "timeout" {
		t.Errorf("mcp_server_errors = %+v, want one timeout entry", got.MCPServerErrors)
	}
}
