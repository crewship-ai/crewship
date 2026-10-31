package server

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/journal"
	"github.com/crewship-ai/crewship/internal/orchestrator"
	"github.com/crewship-ai/crewship/internal/work"
)

// A retained run whose ownership can never be verified must not stay RUNNING
// forever (#4700). It settles without projecting anything: no output chunk is
// written anywhere, and nothing is ever written under a conflicting owner.
func TestRetainedRunWithUnverifiableOwnershipSettlesWithoutProjection(t *testing.T) {
	const (
		ws    = "retained-ws"
		agent = "retained-agent"
	)
	type journalRow struct {
		workspace, agent string
		kind             journal.EntryType
	}
	cases := []struct {
		name string
		// mutate rewrites the recorded run ownership before the flush.
		mutate  func(*orchestrator.RunState)
		journal []journalRow
		// finished=false keeps the source active: nothing may settle yet.
		unfinished bool
		// foreignWork binds the run to a work attempt of another workspace.
		foreignWork bool
		// reason is the settlement marker recorded on the run state.
		reason string
		// wantStatus is what GET /agents/{id}/runs reports for the run's own
		// recorded owner; "" means the owner has no visible run row.
		wantStatus journal.RunStatus
	}{
		{
			name: "start recorded under another agent of the same trace",
			journal: []journalRow{
				{ws, agent, journal.EntryRunStarted},
				{ws, "other-agent", journal.EntryRunStarted},
			},
			reason:     "conflicting_journal_ownership",
			wantStatus: journal.RunStatusFailed,
		},
		{
			name: "terminal recorded under another workspace without agent",
			journal: []journalRow{
				{ws, agent, journal.EntryRunStarted},
				{"other-ws", "", journal.EntryRunCompleted},
			},
			reason:     "conflicting_journal_ownership",
			wantStatus: journal.RunStatusFailed,
		},
		{
			name: "start recorded only under a foreign owner",
			journal: []journalRow{
				{"other-ws", "other-agent", journal.EntryRunStarted},
			},
			reason: "conflicting_journal_ownership",
		},
		{
			name:    "recorded workspace disagrees with agent",
			mutate:  func(r *orchestrator.RunState) { r.WorkspaceID = "other-ws" },
			journal: []journalRow{{ws, agent, journal.EntryRunStarted}},
			reason:  "ownership_disagrees_with_agent",
			// The run state is not trusted to name the owner, so the start
			// recorded under the agent's workspace is deliberately not settled.
			wantStatus: journal.RunStatusRunning,
		},
		{
			name:   "recorded agent no longer exists",
			mutate: func(r *orchestrator.RunState) { r.AgentID = "ghost-agent" },
			reason: "ownership_disagrees_with_agent",
		},
		{
			name:   "run lacks original workspace",
			mutate: func(r *orchestrator.RunState) { r.WorkspaceID = "" },
			reason: "lacks_original_ownership",
		},
		{
			name:        "work attempt owned by another workspace",
			foreignWork: true,
			reason:      "work_scope_differs",
		},
		{
			name: "conflict while the source is still running",
			journal: []journalRow{
				{ws, agent, journal.EntryRunStarted},
				{ws, "other-agent", journal.EntryRunStarted},
			},
			unfinished: true,
			wantStatus: journal.RunStatusRunning,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := newTestServerWithDeps(t)
			mustExec(t, s.db, `INSERT INTO workspaces(id,name,slug) VALUES(?,?,?),(?,?,?)`, ws, "Recovery", ws, "other-ws", "Other", "other-ws")
			mustExec(t, s.db, `INSERT INTO agents(id,workspace_id,name,slug,status) VALUES(?,?,?,?,'RUNNING'),('other-agent',?,'Other','other','IDLE')`, agent, ws, "Agent", "retained", ws)
			runID := "retained-own-" + strings.ReplaceAll(tc.name, " ", "-")
			var workStore *work.Store
			var attempt *work.Claimed
			if tc.foreignWork {
				workStore, attempt = retainedWorkFixture(t, s, "other-ws")
				runID = attempt.RunID
			}
			run := seedHostOutputWith(t, s, runID, "secret output line\n", !tc.unfinished)
			if tc.mutate != nil {
				tc.mutate(&run)
				raw, err := json.Marshal(run)
				if err != nil {
					t.Fatal(err)
				}
				if err := s.state.Set(t.Context(), "agent_runs", run.ID, raw); err != nil {
					t.Fatal(err)
				}
			}
			started := time.Now().UTC().Add(-time.Minute)
			for i, row := range tc.journal {
				if _, err := s.journalWriter.EmitSync(t.Context(), journal.Entry{
					ID: runID + ":fixture:" + string(rune('a'+i)), WorkspaceID: row.workspace, AgentID: row.agent,
					TS: started, Type: row.kind, Severity: journal.SeverityInfo, ActorType: journal.ActorSidecar,
					Summary: "fixture", TraceID: runID, Payload: map[string]any{},
				}); err != nil {
					t.Fatal(err)
				}
			}
			foreignBefore := countForeignRows(t, s, runID, ws, agent)

			// Two background ticks: the first settles, the second must find
			// nothing left to warn about.
			first, second := s.flushRetainedOutput(t.Context()), s.flushRetainedOutput(t.Context())

			raw, err := s.state.Get(t.Context(), "agent_runs", runID)
			if err != nil {
				t.Fatal(err)
			}
			var state struct {
				Status string         `json:"status"`
				Output map[string]any `json:"output"`
			}
			if err := json.Unmarshal(raw, &state); err != nil {
				t.Fatal(err)
			}
			if tc.unfinished {
				if first == nil || second == nil {
					t.Fatalf("active source reported settled: first=%v second=%v", first, second)
				}
				if state.Status != "running" || state.Output["unprojected"] != nil {
					t.Fatalf("active source settled: %s", raw)
				}
			} else {
				if first != nil || second != nil {
					t.Fatalf("settlement kept warning: first=%v second=%v", first, second)
				}
				if state.Status != "error" || state.Output["unprojected"] != tc.reason || state.Output["acknowledged"] == true {
					t.Fatalf("run state not settled as %s: %s", tc.reason, raw)
				}
			}

			var chunks int
			if err := s.db.QueryRow(`SELECT COUNT(*) FROM journal_entries WHERE trace_id=? AND entry_type='exec.output_chunk'`, runID).Scan(&chunks); err != nil {
				t.Fatal(err)
			}
			if chunks != 0 {
				t.Fatalf("retained output projected: %d chunks", chunks)
			}
			if attempt != nil {
				item, err := workStore.Get(t.Context(), attempt.Item.ID)
				if err != nil || item.State != work.StateNeedsReconciliation {
					t.Fatalf("foreign work changed: %+v %v", item, err)
				}
				if p, _, err := workStore.RunProjection(t.Context(), runID); err != nil || p.Ready {
					t.Fatalf("foreign work result staged: %+v %v", p, err)
				}
			}
			if after := countForeignRows(t, s, runID, ws, agent); after != foreignBefore {
				t.Fatalf("rows under a conflicting owner changed: %d -> %d", foreignBefore, after)
			}

			runs, _, err := journal.ListRuns(t.Context(), s.db, journal.RunsQuery{WorkspaceID: ws, AgentID: agent})
			if err != nil {
				t.Fatal(err)
			}
			var got *journal.RunAggregated
			for i := range runs {
				if runs[i].ID == runID {
					got = &runs[i]
				}
			}
			if tc.wantStatus == "" {
				if got != nil {
					t.Fatalf("settlement invented a run row: %+v", *got)
				}
				var own int
				if err := s.db.QueryRow(`SELECT COUNT(*) FROM journal_entries WHERE trace_id=?`, runID).Scan(&own); err != nil {
					t.Fatal(err)
				}
				if own != len(tc.journal) {
					t.Fatalf("settlement wrote journal rows: %d, fixture %d", own, len(tc.journal))
				}
				return
			}
			if got == nil {
				t.Fatalf("run %s missing from agent runs", runID)
			}
			if got.Status != tc.wantStatus {
				t.Fatalf("agent runs status=%s want %s", got.Status, tc.wantStatus)
			}
			if tc.wantStatus == journal.RunStatusFailed {
				if !strings.Contains(got.ErrorMessage, "ownership") || strings.Contains(got.ErrorMessage, "secret") || got.ExitCode != nil {
					t.Fatalf("terminal leaks or lacks reason: %+v", *got)
				}
			}
		})
	}
}

// countForeignRows counts every row of the trace outside the run's own owner.
func countForeignRows(t *testing.T, s *Server, runID, ws, agent string) int {
	t.Helper()
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM journal_entries WHERE trace_id=? AND (workspace_id<>? OR COALESCE(agent_id,'')<>?)`, runID, ws, agent).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}
