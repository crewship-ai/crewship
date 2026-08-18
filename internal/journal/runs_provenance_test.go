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

// A permission denial is the difference between "the agent decided not to
// write the file" and "the agent was not allowed to". Recording it and then
// reading it back nowhere leaves the operator with the first reading, which is
// the misdiagnosis the field exists to prevent.
//
// The producer projects each denial down to its tool name before storing it —
// the denied tool INPUT is arbitrary agent text and this record is
// hash-chained — so the names are all there is to read.
func TestApplySessionProvenance_ReadsPermissionDenials(t *testing.T) {
	cases := []struct {
		name string
		md   map[string]any
		want []string
	}{
		{
			name: "decoded maps",
			md: map[string]any{"permission_denials": []map[string]any{
				{"tool_name": "Bash"}, {"tool_name": "Write"},
			}},
			want: []string{"Bash", "Write"},
		},
		{
			name: "raw JSON as read back from the DB",
			md:   map[string]any{"permission_denials": json.RawMessage(`[{"tool_name":"Bash"}]`)},
			want: []string{"Bash"},
		},
		{
			name: "nothing denied",
			md:   map[string]any{},
			want: nil,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var r RunAggregated
			r.applySessionProvenance(tc.md)
			if len(r.PermissionDenials) != len(tc.want) {
				t.Fatalf("PermissionDenials = %v, want %v", r.PermissionDenials, tc.want)
			}
			for i, want := range tc.want {
				if r.PermissionDenials[i].ToolName != want {
					t.Errorf("PermissionDenials[%d] = %q, want %q", i, r.PermissionDenials[i].ToolName, want)
				}
			}
		})
	}
}

// The producer collapses an agent's repeated refusals of one tool into a single
// entry and attaches how many times it was refused, deliberately: one denial is
// an agent that tried something once, forty is an agent hammering a wall it
// cannot see, and those call for different fixes. The read model dropped the
// count on the floor, so the signal died one hop after it was created.
func TestApplySessionProvenance_KeepsTheDenialCount(t *testing.T) {
	cases := []struct {
		name string
		md   map[string]any
		want []DeniedTool
	}{
		{
			name: "raw JSON as read back from the DB",
			md: map[string]any{"permission_denials": json.RawMessage(
				`[{"tool_name":"Bash","count":39},{"tool_name":"WebFetch","count":1}]`)},
			want: []DeniedTool{{ToolName: "Bash", Count: 39}, {ToolName: "WebFetch", Count: 1}},
		},
		{
			name: "decoded maps",
			md: map[string]any{"permission_denials": []map[string]any{
				{"tool_name": "Bash", "count": 3},
			}},
			want: []DeniedTool{{ToolName: "Bash", Count: 3}},
		},
		{
			// Rows written before the producer attached a count: the name is
			// still the diagnosis, and a fabricated 1 would be a claim we cannot
			// make.
			name: "a row that predates the count",
			md:   map[string]any{"permission_denials": json.RawMessage(`[{"tool_name":"Bash"}]`)},
			want: []DeniedTool{{ToolName: "Bash", Count: 0}},
		},
		{
			// The sentinel now counts the refusals it stands in for.
			name: "unreadable-shape sentinel with a count",
			md: map[string]any{"permission_denials": json.RawMessage(
				`[{"tool_name":"Bash","count":2},{"type":"unrecognized_shape","count":4}]`)},
			want: []DeniedTool{{ToolName: "Bash", Count: 2}, {ToolName: "unrecognized_shape", Count: 4}},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var r RunAggregated
			r.applySessionProvenance(tc.md)
			if len(r.PermissionDenials) != len(tc.want) {
				t.Fatalf("PermissionDenials = %+v, want %+v", r.PermissionDenials, tc.want)
			}
			for i, want := range tc.want {
				if r.PermissionDenials[i] != want {
					t.Errorf("PermissionDenials[%d] = %+v, want %+v — how hard the agent hammered the "+
						"blocked tool is the difference between one fix and another",
						i, r.PermissionDenials[i], want)
				}
			}
		})
	}
}

// Three alarms the producer writes and nothing read: how many servers the CLI
// actually skipped (the stored list is shrunk by entries this build could not
// project), and whether either capped list was cut. A truncation marker that
// reaches no reader is an alarm that does not exist.
func TestApplySessionProvenance_ReadsCountsAndTruncationMarkers(t *testing.T) {
	cases := []struct {
		name          string
		md            map[string]any
		wantSkipCount int
		wantSkipTrunc bool
		wantDenyTrunc bool
	}{
		{
			// float64 is what a JSON round-trip through the DB yields.
			name: "as read back from the DB",
			md: map[string]any{
				"mcp_server_error_count":       float64(3),
				"mcp_server_errors_truncated":  true,
				"permission_denials_truncated": true,
			},
			wantSkipCount: 3, wantSkipTrunc: true, wantDenyTrunc: true,
		},
		{
			name:          "in-process ints",
			md:            map[string]any{"mcp_server_error_count": 7},
			wantSkipCount: 7,
		},
		{
			name: "nothing recorded",
			md:   map[string]any{},
		},
		{
			name: "values of the wrong shape are ignored, not coerced",
			md: map[string]any{
				"mcp_server_error_count":       "three",
				"mcp_server_errors_truncated":  "yes",
				"permission_denials_truncated": float64(1),
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var r RunAggregated
			r.applySessionProvenance(tc.md)
			if r.MCPServerErrorCount != tc.wantSkipCount {
				t.Errorf("MCPServerErrorCount = %d, want %d — the stored list is only what this build "+
					"could project, so the count is the only honest total", r.MCPServerErrorCount, tc.wantSkipCount)
			}
			if r.MCPServerErrorsTruncated != tc.wantSkipTrunc {
				t.Errorf("MCPServerErrorsTruncated = %v, want %v", r.MCPServerErrorsTruncated, tc.wantSkipTrunc)
			}
			if r.PermissionDenialsTruncated != tc.wantDenyTrunc {
				t.Errorf("PermissionDenialsTruncated = %v, want %v — the truncation alarm is written and "+
					"decoded nowhere, so an operator reads a capped list as the whole story",
					r.PermissionDenialsTruncated, tc.wantDenyTrunc)
			}
		})
	}
}

// The producer stores a sentinel when the CLI reported a refusal in a shape it
// could not read: the fact is knowable, the tool name is not. That sentinel is
// a CATEGORY, so it lands under "type" rather than claiming a tool is called
// unrecognized_shape — and a decoder that reads tool_name only would drop it,
// putting the run back to reading as one that CHOSE not to act, which is the
// whole reason the sentinel is written.
//
// The same fallback covers a future CLI that names the refusal by category.
func TestApplySessionProvenance_DenialSentinelSurvivesDecoding(t *testing.T) {
	cases := []struct {
		name string
		md   map[string]any
		want []string
	}{
		{
			name: "unreadable shape sentinel",
			md:   map[string]any{"permission_denials": []map[string]any{{"type": "unrecognized_shape"}}},
			want: []string{"unrecognized_shape"},
		},
		{
			name: "raw JSON sentinel as read back from the DB",
			md:   map[string]any{"permission_denials": json.RawMessage(`[{"type":"unrecognized_shape"}]`)},
			want: []string{"unrecognized_shape"},
		},
		{
			// The producer collapses repeats of one tool into a single entry
			// carrying how many times it was refused. The name is what this
			// surface shows; the count must not disturb the decode.
			name: "deduped entry with a retry count",
			md: map[string]any{"permission_denials": json.RawMessage(
				`[{"tool_name":"Bash","count":39},{"tool_name":"WebFetch","count":1}]`)},
			want: []string{"Bash", "WebFetch"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var r RunAggregated
			r.applySessionProvenance(tc.md)
			if len(r.PermissionDenials) != len(tc.want) {
				t.Fatalf("PermissionDenials = %v, want %v", r.PermissionDenials, tc.want)
			}
			for i, want := range tc.want {
				if r.PermissionDenials[i].ToolName != want {
					t.Errorf("PermissionDenials[%d] = %q, want %q", i, r.PermissionDenials[i].ToolName, want)
				}
			}
		})
	}
}
