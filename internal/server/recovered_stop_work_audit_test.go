package server

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/crewship-ai/crewship/internal/orchestrator"
	"github.com/crewship-ai/crewship/internal/work"
)

func TestRecoveredStopAuditDoesNotSettleWork(t *testing.T) {
	for _, started := range []bool{false, true} {
		t.Run(fmt.Sprintf("started-%v", started), func(t *testing.T) {
			s := newTestServerWithDeps(t)
			mustExec(t, s.db, `INSERT INTO workspaces(id,name,slug) VALUES('rw','Recovery','rw')`)
			mustExec(t, s.db, `INSERT INTO agents(id,workspace_id,name,slug,status) VALUES('a','rw','Agent','a','RUNNING')`)
			store := work.NewStore(s.db)
			tx, err := s.db.BeginTx(t.Context(), nil)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback()
			if _, err := store.AcceptTx(t.Context(), tx, work.AcceptRequest{WorkspaceID: "rw", AgentID: "a", Source: work.SourceWebhook, Class: work.ClassBackground}); err != nil {
				t.Fatal(err)
			}
			if err := tx.Commit(); err != nil {
				t.Fatal(err)
			}
			attempt, err := store.Claim(t.Context(), work.ClaimOptions{LeaseOwner: "original-dispatcher"})
			if err != nil || attempt == nil {
				t.Fatalf("claim: %v %+v", err, attempt)
			}
			if started {
				seedRecoveryTrace(t, s, attempt.RunID, "a")
			}
			// Snapshot every column, including fencing, leases, outcome and cost.
			snapshot := func(table string) string {
				t.Helper()
				rows, err := s.db.Query("SELECT * FROM " + table)
				if err != nil {
					t.Fatal(err)
				}
				defer rows.Close()
				cols, err := rows.Columns()
				if err != nil {
					t.Fatal(err)
				}
				var all [][]any
				for rows.Next() {
					values := make([]any, len(cols))
					dest := make([]any, len(cols))
					for i := range values {
						dest[i] = &values[i]
					}
					if err := rows.Scan(dest...); err != nil {
						t.Fatal(err)
					}
					all = append(all, values)
				}
				if err := rows.Err(); err != nil {
					t.Fatal(err)
				}
				raw, err := json.Marshal(all)
				if err != nil {
					t.Fatal(err)
				}
				return string(raw)
			}
			before := map[string]string{}
			for _, table := range []string{"work_items", "work_attempts", "work_events", "cost_ledger", "approvals_queue", "pipeline_runs"} {
				before[table] = snapshot(table)
			}
			run := orchestrator.RunState{ID: attempt.RunID, AgentID: "a", WorkspaceID: "rw", Status: "cancelled", StopJournalPending: true}
			raw, err := json.Marshal(run)
			if err != nil {
				t.Fatal(err)
			}
			if err := s.state.Set(t.Context(), "agent_runs", run.ID, raw); err != nil {
				t.Fatal(err)
			}
			for i := 0; i < 2; i++ {
				if err := s.flushRecoveredStops(t.Context()); err != nil {
					t.Fatal(err)
				}
			}
			for table, want := range before {
				if got := snapshot(table); got != want {
					t.Errorf("recovery mutated %s: before=%s after=%s", table, want, got)
				}
			}
			var audits, terminals int
			if err := s.db.QueryRow(`SELECT COALESCE(SUM(entry_type='run.recovered_stop'),0),COALESCE(SUM(entry_type IN ('run.completed','run.failed','run.cancelled','run.timeout')),0) FROM journal_entries WHERE trace_id=?`, run.ID).Scan(&audits, &terminals); err != nil {
				t.Fatal(err)
			}
			wantAudit := 1
			if started {
				wantAudit = 0
			}
			if audits != wantAudit || terminals != 0 {
				t.Errorf("audit=%d terminal=%d, want audit=%d and no settlement", audits, terminals, wantAudit)
			}
		})
	}
}

func TestRecoveredStopAuditRetriesFailedWrite(t *testing.T) {
	s := newTestServerWithDeps(t)
	mustExec(t, s.db, `INSERT INTO workspaces(id,name,slug) VALUES('rw','Recovery','rw')`)
	mustExec(t, s.db, `INSERT INTO agents(id,workspace_id,name,slug,status) VALUES('a','rw','Agent','a','RUNNING')`)
	run := orchestrator.RunState{ID: "retry-audit", AgentID: "a", WorkspaceID: "rw", Status: "cancelled", StopJournalPending: true}
	raw, err := json.Marshal(run)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.state.Set(t.Context(), "agent_runs", run.ID, raw); err != nil {
		t.Fatal(err)
	}
	mustExec(t, s.db, `CREATE TRIGGER fail_recovered_audit BEFORE INSERT ON journal_entries WHEN NEW.entry_type='run.recovered_stop' BEGIN SELECT RAISE(FAIL,'audit unavailable'); END`)
	if err := s.flushRecoveredStops(t.Context()); err == nil {
		t.Error("failed audit write not reported")
	}
	if !pendingStop(t, s, run.ID) {
		t.Error("failed audit lost durable retry")
	}
	mustExec(t, s.db, `DROP TRIGGER fail_recovered_audit`)
	if err := s.flushRecoveredStops(t.Context()); err != nil {
		t.Fatal(err)
	}
	var rows int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM journal_entries WHERE trace_id=? AND entry_type='run.recovered_stop'`, run.ID).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != 1 || pendingStop(t, s, run.ID) {
		t.Errorf("retry rows=%d pending=%v", rows, pendingStop(t, s, run.ID))
	}
}
