package server

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/journal"
	"github.com/crewship-ai/crewship/internal/work"
)

func TestRetainedCompletionUsesCapturedTimeAfterHistoryPruning(t *testing.T) {
	server := newTestServerWithDeps(t)
	mustExec(t, server.db, `INSERT INTO workspaces(id,name,slug) VALUES('retained-ws','Recovery','retained-ws')`)
	mustExec(t, server.db, `INSERT INTO agents(id,workspace_id,name,slug,status) VALUES('retained-agent','retained-ws','Agent','retained','RUNNING')`)
	run := seedHostOutput(t, server, "retained-completion-time")
	run.StartedAt = time.Date(2026, 10, 19, 10, 0, 0, 0, time.UTC)
	raw, err := json.Marshal(run)
	if err != nil {
		t.Fatal(err)
	}
	if err := server.state.Set(t.Context(), "agent_runs", run.ID, raw); err != nil {
		t.Fatal(err)
	}
	snapshot, err := server.orchestrator.ReadRetainedRunResult(t.Context(), run)
	if err != nil {
		t.Fatal(err)
	}
	captured, err := time.Parse(time.RFC3339Nano, snapshot.Result.CompletedAt)
	if err != nil {
		t.Fatal(err)
	}
	var deliveries atomic.Int32
	server.journalWriter.SetCommitObserver(func(entries []journal.Entry) {
		for _, entry := range entries {
			if entry.Type == journal.EntryRunCompleted {
				deliveries.Add(1)
			}
		}
	})
	if err := server.projectRetainedOutput(t.Context(), run); err != nil {
		t.Fatal(err)
	}
	terminal := "run-terminal:" + run.ID
	var timestamp, fingerprint string
	if err := server.db.QueryRow(`SELECT ts FROM journal_entries WHERE id=?`, terminal).Scan(&timestamp); err != nil {
		t.Fatal(err)
	}
	if timestamp != captured.UTC().Format("2006-01-02T15:04:05.000Z") {
		t.Fatalf("retained terminal used start/recovery time: got %s captured %s", timestamp, snapshot.Result.CompletedAt)
	}
	if err := server.db.QueryRow(`SELECT fingerprint FROM journal_replay_receipts WHERE id=?`, terminal).Scan(&fingerprint); err != nil {
		t.Fatal(err)
	}
	mustExec(t, server.db, `DELETE FROM journal_entries WHERE id=?`, terminal)
	if err := server.projectRetainedOutput(t.Context(), run); err != nil {
		t.Fatal(err)
	}
	var after string
	if err := server.db.QueryRow(`SELECT fingerprint FROM journal_replay_receipts WHERE id=?`, terminal).Scan(&after); err != nil || after != fingerprint || deliveries.Load() != 1 {
		t.Fatalf("pruned terminal replay changed immutable receipt/delivery: deliveries=%d %v", deliveries.Load(), err)
	}
}

func TestRetainedCompletionRejectsStagedTimestampMismatch(t *testing.T) {
	for _, staged := range []string{"", "2026-10-19T10:00:01Z"} {
		t.Run(staged, func(t *testing.T) {
			s := newTestServerWithDeps(t)
			mustExec(t, s.db, `INSERT INTO workspaces(id,name,slug) VALUES('retained-ws','Recovery','retained-ws')`)
			mustExec(t, s.db, `INSERT INTO agents(id,workspace_id,name,slug,status) VALUES('retained-agent','retained-ws','Agent','retained','RUNNING')`)
			store, a := retainedWorkFixture(t, s, "retained-ws")
			run := seedHostOutput(t, s, a.RunID)
			zero := 0
			if _, err := store.RecoverRetainedResult(t.Context(), run.ID, work.RunResult{CompletedAt: staged, ExitCode: &zero}, true); err != nil {
				t.Fatal(err)
			}
			if err := s.projectRetainedOutput(t.Context(), run); err == nil {
				t.Fatal("mismatching protected/staged completion accepted")
			}
			var count int
			if err := s.db.QueryRow(`SELECT COUNT(*) FROM journal_entries WHERE trace_id=? AND entry_type='run.completed'`, run.ID).Scan(&count); err != nil || count != 0 {
				t.Fatalf("mismatch projected terminal: %d %v", count, err)
			}
		})
	}
}

func TestLegacyRetainedMetadataCannotInventCompletion(t *testing.T) {
	for _, value := range []any{"2026-10-19T10:00:42.125Z", json.Number("123")} {
		t.Run(fmt.Sprint(value), func(t *testing.T) {
			s := newTestServerWithDeps(t)
			mustExec(t, s.db, `INSERT INTO workspaces(id,name,slug) VALUES('retained-ws','Recovery','retained-ws')`)
			mustExec(t, s.db, `INSERT INTO agents(id,workspace_id,name,slug,status) VALUES('retained-agent','retained-ws','Agent','retained','RUNNING')`)
			store, a := retainedWorkFixture(t, s, "retained-ws")
			run := seedHostOutput(t, s, a.RunID)
			run.StartedAt = time.Date(2026, 10, 19, 10, 0, 0, 0, time.UTC)
			state, err := json.Marshal(run)
			if err != nil {
				t.Fatal(err)
			}
			if err := s.state.Set(t.Context(), "agent_runs", run.ID, state); err != nil {
				t.Fatal(err)
			}
			// Only this test's synthetic spool is converted to a historical exit record.
			snapshot, err := s.orchestrator.ReadRetainedRunResult(t.Context(), run)
			if err != nil {
				t.Fatal(err)
			}
			encoded, _ := json.Marshal(snapshot.Result.CompletedAt)
			path := filepath.Join(run.Output.Directory, "events.jsonl")
			raw, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			raw = bytes.Replace(raw, append([]byte(`,"completed_at":`), encoded...), nil, 1)
			if err := os.WriteFile(path, raw, 0600); err != nil {
				t.Fatal(err)
			}
			cpPath := filepath.Join(run.Output.Directory, "checkpoint.json")
			cpRaw, err := os.ReadFile(cpPath)
			if err != nil {
				t.Fatal(err)
			}
			var cp map[string]any
			if err := json.Unmarshal(cpRaw, &cp); err != nil {
				t.Fatal(err)
			}
			cp["bytes"] = len(raw)
			cpRaw, _ = json.Marshal(cp)
			if err := os.WriteFile(cpPath, cpRaw, 0600); err != nil {
				t.Fatal(err)
			}
			zero := 0
			if _, err := store.RecoverRetainedResult(t.Context(), run.ID, work.RunResult{ExitCode: &zero, Metadata: map[string]any{"captured_completed_at": value}}, true); err != nil {
				t.Fatal(err)
			}
			if err := s.projectRetainedOutput(t.Context(), run); err != nil {
				t.Fatal(err)
			}
			terminal := "work-terminal:" + run.ID
			var timestamp, payload, before, after string
			if err := s.db.QueryRow(`SELECT ts,payload FROM journal_entries WHERE id=?`, terminal).Scan(&timestamp, &payload); err != nil {
				t.Fatal(err)
			}
			expected, _ := json.Marshal(value)
			if timestamp != "2026-10-19T10:00:00.000Z" || !strings.Contains(payload, `"captured_completed_at":`+string(expected)) {
				t.Fatalf("legacy time/metadata changed: %s %s", timestamp, payload)
			}
			if err := s.db.QueryRow(`SELECT fingerprint FROM journal_replay_receipts WHERE id=?`, terminal).Scan(&before); err != nil {
				t.Fatal(err)
			}
			mustExec(t, s.db, `DELETE FROM journal_entries WHERE id=?`, terminal)
			if err := s.projectRetainedOutput(t.Context(), run); err != nil {
				t.Fatal(err)
			}
			if err := s.db.QueryRow(`SELECT fingerprint FROM journal_replay_receipts WHERE id=?`, terminal).Scan(&after); err != nil || before != after {
				t.Fatalf("legacy MAC changed: %v", err)
			}
		})
	}
}
