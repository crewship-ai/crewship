package journal

import (
	"context"
	"slices"
	"sync"
	"testing"
	"time"
)

// Audit-only recovery must remain visible to journal readers without entering
// the automation observer fan-out, including a partially rejected batch.
func TestRecoveredStopAuditNeverInvokesObservers(t *testing.T) {
	for _, mode := range []string{"sync", "batch", "poison-batch"} {
		t.Run(mode, func(t *testing.T) {
			db := openTestDB(t)
			defer db.Close()
			if _, err := db.Exec(`CREATE TRIGGER reject_poison BEFORE INSERT ON journal_entries
 WHEN NEW.summary='POISON' BEGIN SELECT RAISE(ABORT, 'constraint failed: test poison'); END`); err != nil {
				t.Fatal(err)
			}
			w := NewWriter(db, quietLogger(), WriterOptions{QueueSize: 16, FlushSize: 16, FlushInterval: time.Hour})
			defer w.Close()
			var mu sync.Mutex
			var observed []string
			var readEntries []string
			callbacks := 0
			w.SetCommitObserver(func(entries []Entry) {
				mu.Lock()
				defer mu.Unlock()
				callbacks++
				for _, e := range entries {
					observed = append(observed, e.Summary)
				}
			})
			w.AddReadObserver(func(entries []Entry) {
				mu.Lock()
				defer mu.Unlock()
				for _, e := range entries {
					readEntries = append(readEntries, e.Summary)
				}
			})
			wake := w.Notify()
			emit := w.Emit
			if mode == "sync" {
				emit = w.EmitSync
			}
			if _, err := emit(t.Context(), Entry{WorkspaceID: "ws1", AgentID: "agent", Type: "run.recovered_stop", ActorType: ActorSystem,
				Summary: "audit", TraceID: "recovered", Payload: map[string]any{"start_recorded": false}}); err != nil {
				t.Fatal(err)
			}
			if mode == "poison-batch" {
				if _, err := emit(t.Context(), Entry{WorkspaceID: "ws1", Type: EntryAgentMentioned, ActorType: ActorSystem, Summary: "POISON"}); err != nil {
					t.Fatal(err)
				}
			}
			if err := w.Flush(t.Context()); err != nil {
				t.Fatal(err)
			}
			select {
			case <-wake:
			default:
				t.Error("audit did not wake journal readers")
			}
			mu.Lock()
			if !slices.Equal(readEntries, []string{"audit"}) {
				t.Errorf("read-only feed=%v, want audit", readEntries)
			}
			if callbacks != 0 {
				t.Errorf("audit-only flush invoked %d callbacks", callbacks)
			}
			observed = nil
			callbacks = 0
			mu.Unlock()
			// Keep ordinary events before and after an audit in one batch.
			// Dropping the entire observer batch would lose valid automation.
			for _, name := range []string{"before", "audit-middle", "after"} {
				kind := EntryAgentMentioned
				if name == "audit-middle" {
					kind = "run.recovered_stop"
				}
				if _, err := emit(t.Context(), Entry{WorkspaceID: "ws1", AgentID: "agent", TraceID: "mixed-run", Type: kind, ActorType: ActorSystem, Summary: name, Payload: map[string]any{"start_recorded": false}}); err != nil {
					t.Fatal(err)
				}
				if mode == "poison-batch" && name == "audit-middle" {
					if _, err := emit(t.Context(), Entry{WorkspaceID: "ws1", Type: EntryAgentMentioned, ActorType: ActorSystem, Summary: "POISON"}); err != nil {
						t.Fatal(err)
					}
				}
			}
			// A normal event still reaches observers after an audit-only flush.
			if _, err := emit(t.Context(), Entry{WorkspaceID: "ws1", Type: EntryAgentMentioned, ActorType: ActorSystem, Summary: "ordinary"}); err != nil {
				t.Fatal(err)
			}
			if err := w.Flush(t.Context()); err != nil {
				t.Fatal(err)
			}
			var rows int
			if err := db.QueryRow(`SELECT COUNT(*) FROM journal_entries WHERE entry_type='run.recovered_stop'`).Scan(&rows); err != nil {
				t.Fatal(err)
			}
			if rows != 2 {
				t.Errorf("durable audit rows=%d, want 2", rows)
			}
			mu.Lock()
			defer mu.Unlock()
			if !slices.Equal(observed, []string{"before", "after", "ordinary"}) {
				t.Errorf("observer entries=%v, want before, after, ordinary", observed)
			}
		})
	}
}

// The database write is the boundary: an API precheck alone races recovery.
func TestRecoveredStopAuditAndRealLifecycleAreMutuallyExclusive(t *testing.T) {
	for _, lifecycle := range []EntryType{EntryRunStarted, EntryRunCompleted, EntryRunFailed, EntryRunCancelled, EntryRunTimeout} {
		for _, auditFirst := range []bool{true, false} {
			name := "start-first"
			if auditFirst {
				name = "audit-first"
			}
			t.Run(string(lifecycle)+"/"+name, func(t *testing.T) {
				db := openTestDB(t)
				defer db.Close()
				w := NewWriter(db, quietLogger(), WriterOptions{FlushInterval: time.Hour})
				defer w.Close()
				first, second := lifecycle, EntryType("run.recovered_stop")
				if auditFirst {
					first, second = second, first
				}
				entry := func(kind EntryType) Entry {
					return Entry{WorkspaceID: "ws1", AgentID: "agent", TraceID: "same-run", Type: kind, ActorType: ActorSystem, Summary: string(kind), Payload: map[string]any{"start_recorded": false}}
				}
				if _, err := w.EmitSync(t.Context(), entry(first)); err != nil {
					t.Fatal(err)
				}
				if _, err := w.EmitSync(t.Context(), entry(second)); err == nil {
					t.Errorf("accepted %s after %s", second, first)
				}
				var n int
				if err := db.QueryRow(`SELECT COUNT(*) FROM journal_entries WHERE workspace_id='ws1' AND trace_id='same-run'`).Scan(&n); err != nil {
					t.Fatal(err)
				}
				if n != 1 {
					t.Errorf("contradictory identity has %d events", n)
				}
				// Same trace in another workspace must not be suppressed.
				foreign := entry(EntryRunStarted)
				foreign.WorkspaceID = "ws2"
				if _, err := w.EmitSync(t.Context(), foreign); err != nil {
					t.Fatalf("blocked independent workspace: %v", err)
				}
			})
		}
	}
}

func TestRecoveredStopAuditConflictDoesNotPoisonOrdinaryBatch(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	w := NewWriter(db, quietLogger(), WriterOptions{QueueSize: 16, FlushSize: 16, FlushInterval: time.Hour})
	defer w.Close()
	var mu sync.Mutex
	var observed []string
	w.SetCommitObserver(func(entries []Entry) {
		mu.Lock()
		defer mu.Unlock()
		for _, e := range entries {
			observed = append(observed, e.Summary)
		}
	})
	// Ordinary records on both sides of the delayed start must survive the
	// rollback and per-entry retry of this single flush.
	for _, kind := range []EntryType{EntryAgentMentioned, "run.recovered_stop", EntryRunStarted, EntryAgentMentioned} {
		if _, err := w.Emit(t.Context(), Entry{WorkspaceID: "ws1", AgentID: "a", TraceID: "closed", Type: kind, ActorType: ActorSystem, Summary: string(kind), Payload: map[string]any{"start_recorded": false}}); err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	if err := w.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	var audits, starts, ordinary int
	if err := db.QueryRow(`SELECT COALESCE(SUM(entry_type='run.recovered_stop'),0),COALESCE(SUM(entry_type='run.started'),0),COALESCE(SUM(entry_type='agent.mentioned'),0) FROM journal_entries`).Scan(&audits, &starts, &ordinary); err != nil {
		t.Fatal(err)
	}
	if audits != 1 || starts != 0 || ordinary != 2 {
		t.Errorf("audit=%d start=%d ordinary=%d", audits, starts, ordinary)
	}
	mu.Lock()
	defer mu.Unlock()
	if !slices.Equal(observed, []string{string(EntryAgentMentioned), string(EntryAgentMentioned)}) {
		t.Errorf("observer entries=%v", observed)
	}
}
