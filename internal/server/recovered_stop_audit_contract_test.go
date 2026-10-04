package server

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/crewship-ai/crewship/internal/journal"
	"github.com/crewship-ai/crewship/internal/orchestrator"
	"github.com/crewship-ai/crewship/internal/provider/bbolt"
)

// Exercise the real outbox drain, not the unused ReconcileRecoveredRun helper.
func TestRecoveredStopAuditWithoutStartContract(t *testing.T) {
	s := newTestServerWithDeps(t)
	statePath := filepath.Join(t.TempDir(), "audit-state.db")
	durable, err := bbolt.New(statePath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = durable.Close() }()
	s.state = durable
	mustExec(t, s.db, `INSERT INTO workspaces(id,name,slug) VALUES('rw','Recovery','rw')`)
	mustExec(t, s.db, `INSERT INTO agents(id,workspace_id,name,slug,status) VALUES('a','rw','Agent','a','RUNNING')`)
	run := orchestrator.RunState{ID: "audit-stop", AgentID: "a", WorkspaceID: "rw", Status: "cancelled", StopJournalPending: true}
	raw, err := json.Marshal(run)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.state.Set(t.Context(), "agent_runs", run.ID, raw); err != nil {
		t.Fatal(err)
	}
	before := map[string]int{}
	for _, table := range []string{"cost_ledger", "approvals_queue", "pipeline_runs", "work_items", "work_attempts"} {
		var n int
		if err := s.db.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&n); err != nil {
			t.Fatal(err)
		}
		before[table] = n
	}
	var observed atomic.Int64
	s.journalWriter.SetCommitObserver(func(entries []journal.Entry) { observed.Add(int64(len(entries))) })
	if err = s.flushRecoveredStops(t.Context()); err != nil {
		t.Fatal(err)
	}
	var kind, payload, summary string
	if err = s.db.QueryRow(`SELECT entry_type,payload,summary FROM journal_entries WHERE trace_id=?`, run.ID).Scan(&kind, &payload, &summary); err != nil {
		t.Fatal(err)
	}
	if kind != "run.recovered_stop" {
		t.Errorf("missing-start audit type=%q, want run.recovered_stop", kind)
	}
	var p map[string]any
	if err = json.Unmarshal([]byte(payload), &p); err != nil {
		t.Fatal(err)
	}
	if v, ok := p["start_recorded"].(bool); !ok || v {
		t.Errorf("start_recorded must be explicitly false: %s", payload)
	}
	if summary != "Zastaveno během obnovy, začátek nebyl zaznamenán." {
		t.Errorf("misleading recovery summary: %q", summary)
	}
	if observed.Load() != 0 {
		t.Errorf("audit invoked %d downstream observer entries", observed.Load())
	}
	// Recreate the controller facade and retry the durable marker, as after a
	// crash between journal commit and acknowledgement. Same run ID stays unique.
	if err = s.state.Set(t.Context(), "agent_runs", run.ID, raw); err != nil {
		t.Fatal(err)
	}
	if err = durable.Close(); err != nil {
		t.Fatal(err)
	}
	durable, err = bbolt.New(statePath)
	if err != nil {
		t.Fatal(err)
	}
	s.state = durable
	resumedWriter := journal.NewWriter(s.db, s.logger, journal.WriterOptions{})
	defer resumedWriter.Close()
	resumedWriter.SetCommitObserver(func(entries []journal.Entry) { observed.Add(int64(len(entries))) })
	resumed := &Server{state: durable, db: s.db, journalWriter: resumedWriter, logger: s.logger}
	if err = resumed.flushRecoveredStops(t.Context()); err != nil {
		t.Fatal(err)
	}
	var entries, starts int
	if err = s.db.QueryRow(`SELECT COUNT(*),COALESCE(SUM(entry_type='run.started'),0) FROM journal_entries WHERE trace_id=?`, run.ID).Scan(&entries, &starts); err != nil {
		t.Fatal(err)
	}
	if entries != 1 || starts != 0 {
		t.Errorf("entries=%d starts=%d; want one audit and no invented start", entries, starts)
	}
	if pendingStop(t, s, run.ID) {
		t.Error("acknowledgement not persisted")
	}
	_, total, err := journal.ListRuns(t.Context(), s.db, journal.RunsQuery{WorkspaceID: "rw"})
	if err != nil {
		t.Fatal(err)
	}
	if total != 0 {
		t.Errorf("audit fabricated %d ordinary runs", total)
	}
	insights, err := journal.RunInsights(t.Context(), s.db, "rw", journal.RunInsightsWindow("24h"))
	if err != nil {
		t.Fatal(err)
	}
	if insights.Total != 0 {
		t.Errorf("audit changed run statistics: %+v", insights)
	}
	for table, n := range before {
		var got int
		if err := s.db.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&got); err != nil {
			t.Fatal(err)
		}
		if got != n {
			t.Errorf("audit changed %s: %d -> %d", table, n, got)
		}
	}
	if observed.Load() != 0 {
		t.Errorf("retry invoked %d observer entries", observed.Load())
	}
	// Late delivery of a real start must not reclassify this audit-only trace
	// as a completed or running execution, or invent another terminal event.
	seedRecoveryTrace(t, s, run.ID, "a")
	s.recoverOrphanedRuns(t.Context())
	if err := s.journalWriter.Flush(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err = s.db.QueryRow(`SELECT COUNT(*) FROM journal_entries WHERE trace_id=? AND entry_type<>'run.started'`, run.ID).Scan(&entries); err != nil {
		t.Fatal(err)
	}
	if entries != 1 {
		t.Errorf("late start produced %d terminal/audit entries", entries)
	}
	_, total, err = journal.ListRuns(t.Context(), s.db, journal.RunsQuery{WorkspaceID: "rw"})
	if err != nil {
		t.Fatal(err)
	}
	if total != 0 {
		t.Errorf("late start reclassified audit as %d ordinary runs", total)
	}
	stats, err := journal.RunStats(t.Context(), s.db, "rw")
	if err != nil {
		t.Fatal(err)
	}
	if stats != (journal.RunStatsResult{}) {
		t.Errorf("late start changed stats: %+v", stats)
	}
	insights, err = journal.RunInsights(t.Context(), s.db, "rw", journal.RunInsightsWindow("24h"))
	if err != nil {
		t.Fatal(err)
	}
	if insights.Total != 0 {
		t.Errorf("late start changed insights: %+v", insights)
	}
	// Ordinary entries still use the real observer fan-out.
	if _, err = s.journalWriter.EmitSync(t.Context(), journal.Entry{WorkspaceID: "rw", Type: journal.EntryAgentMentioned, ActorType: journal.ActorSystem, Summary: "observer positive control"}); err != nil {
		t.Fatal(err)
	}
	if observed.Load() != 1 {
		t.Errorf("ordinary observer positive control=%d, want1", observed.Load())
	}

}

func TestRecoveredStopAuditRequiresConfirmedOwnership(t *testing.T) {
	for _, tc := range []struct{ name, agent, workspace string }{
		{"missing-agent", "gone", "rw"}, {"missing-workspace", "a", ""},
		{"other-workspace", "a", "other"}, {"missing-agent-id", "", "rw"},
		{"missing-run-id", "a", "rw"}, {"deleted-agent", "a", "rw"},
		{"conflicting-journal-agent", "a", "rw"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newTestServerWithDeps(t)
			var logs bytes.Buffer
			s.logger = slog.New(slog.NewTextHandler(&logs, nil))
			mustExec(t, s.db, `INSERT INTO workspaces(id,name,slug) VALUES('rw','Recovery','rw'),('other','Other','other')`)
			mustExec(t, s.db, `INSERT INTO agents(id,workspace_id,name,slug,status) VALUES('a','rw','Agent','a','RUNNING')`)
			run := orchestrator.RunState{ID: tc.name, AgentID: tc.agent, WorkspaceID: tc.workspace, Status: "cancelled", StopJournalPending: true}
			if tc.name == "missing-run-id" {
				run.ID = ""
			}
			if tc.name == "deleted-agent" {
				mustExec(t, s.db, `UPDATE agents SET deleted_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE id='a'`)
			}
			if tc.name == "conflicting-journal-agent" {
				mustExec(t, s.db, `INSERT INTO agents(id,workspace_id,name,slug,status) VALUES('other-agent','rw','Other','other-agent','RUNNING')`)
				mustExec(t, s.db, `INSERT INTO journal_entries(id,workspace_id,agent_id,ts,entry_type,severity,actor_type,summary,payload,refs,trace_id,priority) VALUES('foreign-audit','rw','other-agent',strftime('%Y-%m-%dT%H:%M:%fZ','now'),'run.recovered_stop','notice','system','foreign audit','{"start_recorded":false}','{}',?,'normal')`, run.ID)
			}
			raw, err := json.Marshal(run)
			if err != nil {
				t.Fatal(err)
			}
			if err = s.state.Set(t.Context(), "agent_runs", tc.name, raw); err != nil {
				t.Fatal(err)
			}
			_ = s.flushRecoveredStops(t.Context()) // a returned scope error is conservative too
			var count int
			if err = s.db.QueryRow(`SELECT COUNT(*) FROM journal_entries WHERE trace_id=? AND id<>'foreign-audit'`, run.ID).Scan(&count); err != nil {
				t.Fatal(err)
			}
			if count != 0 {
				t.Errorf("ambiguous ownership published %d entries", count)
			}
			var status string
			if err := s.db.QueryRow(`SELECT status FROM agents WHERE id='a'`).Scan(&status); err != nil {
				t.Fatal(err)
			}
			if status != "RUNNING" {
				t.Errorf("ownership refusal changed agent status: %s", status)
			}
			if logs.Len() == 0 {
				t.Error("ownership refusal missing from operational log")
			}
		})
	}
}

// This exercises the production boot path formerly represented only by a test
// of the unused ReconcileRecoveredRun method.
func TestRecoveryMissingContainerIdentityRemainsUnverified(t *testing.T) {
	s := newTestServerWithDeps(t)
	var logs bytes.Buffer
	s.logger = slog.New(slog.NewTextHandler(&logs, nil))
	c := &recoveredProbeContainer{mockContainer: &mockContainer{}, state: "stopped"}
	s.container = c
	s.orchestrator = orchestrator.New(c, s.state, s.logger)
	mustExec(t, s.db, `INSERT INTO workspaces(id,name,slug) VALUES('rw','Recovery','rw')`)
	mustExec(t, s.db, `INSERT INTO agents(id,workspace_id,name,slug,status) VALUES('a','rw','Agent','a','RUNNING')`)
	run := orchestrator.RunState{ID: "missing-runtime", AgentID: "a", WorkspaceID: "rw", AgentSlug: "a", Status: "running"}
	seedRecoveryTrace(t, s, run.ID, "a")
	raw, err := json.Marshal(run)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.state.Set(t.Context(), "agent_runs", run.ID, raw); err != nil {
		t.Fatal(err)
	}
	s.recoverOrphanedRuns(t.Context())
	after, err := s.state.Get(t.Context(), "agent_runs", run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(raw, after) {
		t.Error("unverified runtime changed during boot recovery")
	}
	if n := recoveryTerminalCount(t, s, run.ID); n != 0 {
		t.Errorf("unverified runtime produced %d terminal events", n)
	}
	if !bytes.Contains(logs.Bytes(), []byte("no recorded container identity")) {
		t.Error("missing operational explanation")
	}
}
