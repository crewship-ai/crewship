package journal

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

// emitProvenanceRun writes a run.started + run.completed pair whose terminal
// metadata is `meta` — the map the run driver hands the journal after merging
// the CLI's session-init provenance into it.
func emitProvenanceRun(t *testing.T, w *Writer, traceID string, meta map[string]any) {
	t.Helper()
	ctx := context.Background()
	now := time.Now().UTC()
	if _, err := w.Emit(ctx, Entry{
		WorkspaceID: "ws_test",
		AgentID:     "agent_a",
		Type:        EntryRunStarted,
		ActorType:   ActorSidecar,
		Summary:     "started",
		Payload:     map[string]any{"trigger_type": "USER"},
		TraceID:     traceID,
		TS:          now,
	}); err != nil {
		t.Fatalf("emit started: %v", err)
	}
	if _, err := w.Emit(ctx, Entry{
		WorkspaceID: "ws_test",
		AgentID:     "agent_a",
		Type:        EntryRunCompleted,
		ActorType:   ActorSidecar,
		Summary:     "completed",
		Payload:     map[string]any{"exit_code": float64(0), "metadata": meta},
		TraceID:     traceID,
		TS:          now.Add(time.Minute),
	}); err != nil {
		t.Fatalf("emit completed: %v", err)
	}
	_ = w.Flush(ctx)
	time.Sleep(50 * time.Millisecond)
}

// TestListRuns_SurfacesSessionProvenance proves the four scalar
// session-provenance keys the run driver stamps on the terminal entry reach
// the run read model — the record an operator queries to answer "which binary,
// which key, which permission posture served this run".
func TestListRuns_SurfacesSessionProvenance(t *testing.T) {
	tests := []struct {
		name string
		meta map[string]any
		want RunAggregated // only the provenance fields are compared
	}{
		{
			name: "full session-init provenance",
			meta: map[string]any{
				"model":           "claude-sonnet-4-5",
				"cli_version":     "2.1.204",
				"api_key_source":  "ANTHROPIC_API_KEY",
				"permission_mode": "bypassPermissions",
				"session_id":      "9f1c2b7e-0000-4a11-9b0c-1d2e3f405162",
			},
			want: RunAggregated{
				CLIVersion:     "2.1.204",
				APIKeySource:   "ANTHROPIC_API_KEY",
				PermissionMode: "bypassPermissions",
				SessionID:      "9f1c2b7e-0000-4a11-9b0c-1d2e3f405162",
			},
		},
		{
			name: "adapter that reports no session-init leaves every field blank",
			meta: map[string]any{"model": "gpt-5", "duration_ms": float64(1200)},
			want: RunAggregated{},
		},
		{
			name: "partial provenance carries only what was recorded",
			meta: map[string]any{"cli_version": "2.1.219"},
			want: RunAggregated{CLIVersion: "2.1.219"},
		},
		{
			name: "non-string values are dropped rather than coerced",
			meta: map[string]any{"cli_version": float64(2), "session_id": nil, "api_key_source": ""},
			want: RunAggregated{},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			db := openTestDB(t)
			defer db.Close()
			w := NewWriter(db, quietLogger(), WriterOptions{FlushSize: 1})
			defer w.Close()

			emitProvenanceRun(t, w, "run_prov", tc.meta)

			runs, _, err := ListRuns(context.Background(), db, RunsQuery{WorkspaceID: "ws_test"})
			if err != nil {
				t.Fatalf("list: %v", err)
			}
			if len(runs) != 1 {
				t.Fatalf("rows: got %d want 1", len(runs))
			}
			got := runs[0]
			if got.CLIVersion != tc.want.CLIVersion {
				t.Errorf("CLIVersion = %q, want %q", got.CLIVersion, tc.want.CLIVersion)
			}
			if got.APIKeySource != tc.want.APIKeySource {
				t.Errorf("APIKeySource = %q, want %q", got.APIKeySource, tc.want.APIKeySource)
			}
			if got.PermissionMode != tc.want.PermissionMode {
				t.Errorf("PermissionMode = %q, want %q", got.PermissionMode, tc.want.PermissionMode)
			}
			if got.SessionID != tc.want.SessionID {
				t.Errorf("SessionID = %q, want %q", got.SessionID, tc.want.SessionID)
			}
		})
	}
}

// TestListRuns_SurfacesMCPServerErrors covers the one provenance field that is
// a finding rather than a label: an MCP server the CLI skipped at startup, on a
// run that still exited 0.
func TestListRuns_SurfacesMCPServerErrors(t *testing.T) {
	tests := []struct {
		name string
		meta map[string]any
		want []MCPServerError
	}{
		{
			name: "raw array from the adapter decodes into entries",
			meta: map[string]any{
				// json.RawMessage is exactly what the Claude adapter puts in
				// the metadata map — it never parses the CLI's array.
				"mcp_server_errors": json.RawMessage(
					`[{"name":"crewship-memory","type":"config_error","message":"no such file"},` +
						`{"name":"sentry","type":"connection_error","message":"timeout"}]`),
			},
			want: []MCPServerError{
				{Name: "crewship-memory", Type: "config_error", Message: "no such file"},
				{Name: "sentry", Type: "connection_error", Message: "timeout"},
			},
		},
		{
			name: "nothing skipped means the key is absent, and the field stays empty",
			meta: map[string]any{"model": "claude-sonnet-4-5"},
			want: nil,
		},
		{
			name: "a value of the wrong shape is dropped, not half-decoded",
			meta: map[string]any{"mcp_server_errors": "crewship-memory failed"},
			want: nil,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			db := openTestDB(t)
			defer db.Close()
			w := NewWriter(db, quietLogger(), WriterOptions{FlushSize: 1})
			defer w.Close()

			emitProvenanceRun(t, w, "run_mcp", tc.meta)

			runs, _, err := ListRuns(context.Background(), db, RunsQuery{WorkspaceID: "ws_test"})
			if err != nil {
				t.Fatalf("list: %v", err)
			}
			if len(runs) != 1 {
				t.Fatalf("rows: got %d want 1", len(runs))
			}
			got := runs[0].MCPServerErrors
			if len(got) != len(tc.want) {
				t.Fatalf("MCPServerErrors = %+v, want %+v", got, tc.want)
			}
			for i := range tc.want {
				if got[i] != tc.want[i] {
					t.Errorf("MCPServerErrors[%d] = %+v, want %+v", i, got[i], tc.want[i])
				}
			}
		})
	}
}

// TestGetRunByID_SurfacesProvenance guards the single-run read path too: it
// scans through the same helper, and `crewship run get` is the only surface
// that shows these fields in full.
func TestGetRunByID_SurfacesProvenance(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	w := NewWriter(db, quietLogger(), WriterOptions{FlushSize: 1})
	defer w.Close()

	emitProvenanceRun(t, w, "run_one", map[string]any{
		"cli_version":       "2.1.204",
		"mcp_server_errors": json.RawMessage(`[{"name":"sentry","type":"config_error","message":"bad json"}]`),
	})

	got, err := GetRunByID(context.Background(), db, "ws_test", "run_one")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got == nil {
		t.Fatal("run not found")
	}
	if got.CLIVersion != "2.1.204" {
		t.Errorf("CLIVersion = %q, want 2.1.204", got.CLIVersion)
	}
	if len(got.MCPServerErrors) != 1 || got.MCPServerErrors[0].Name != "sentry" {
		t.Errorf("MCPServerErrors = %+v, want one entry for sentry", got.MCPServerErrors)
	}
}
