package api

// Coverage tests for consolidate_handler.go: SetJournal(nil) fallback,
// parseSinceDuration (d/w suffixes + errors), and the runOnce worker
// paths (default memory root, per-crew Run failure tolerance, and the
// crews_run accounting on a non-skipped run).

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/consolidate"
	"github.com/crewship-ai/crewship/internal/journal"
)

func TestConsolidateSetJournal_NilCollapsesToNoop(t *testing.T) {
	db := setupTestDB(t)
	h := NewConsolidateHandler(db, newTestLogger())

	h.SetJournal(&recordingEmitter{})
	if _, ok := h.journal.(*recordingEmitter); !ok {
		t.Fatalf("journal = %T, want *recordingEmitter after SetJournal", h.journal)
	}
	h.SetJournal(nil)
	if _, ok := h.journal.(noopEmitter); !ok {
		t.Fatalf("journal = %T, want noopEmitter after SetJournal(nil)", h.journal)
	}
}

func TestParseSinceDuration(t *testing.T) {
	t.Parallel()
	tests := []struct {
		in      string
		want    time.Duration
		wantErr bool
	}{
		{"", 0, false},
		{"90m", 90 * time.Minute, false},
		{"3d", 72 * time.Hour, false},
		{"2w", 14 * 24 * time.Hour, false},
		{"xd", 0, true},
		{"w", 0, true},
		{"bogus", 0, true},
	}
	for _, tt := range tests {
		t.Run(fmt.Sprintf("%q", tt.in), func(t *testing.T) {
			got, err := parseSinceDuration(tt.in)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("parseSinceDuration(%q) = %v, want error", tt.in, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseSinceDuration(%q) error: %v", tt.in, err)
			}
			if got != tt.want {
				t.Errorf("parseSinceDuration(%q) = %v, want %v", tt.in, got, tt.want)
			}
		})
	}
}

// findCompletedEntry returns the consolidation-completed entry from a
// recording emitter, failing the test when absent.
func findCompletedEntry(t *testing.T, rec *recordingEmitter) journal.Entry {
	t.Helper()
	for _, e := range rec.entries {
		if e.Type == journal.EntrySystemConsolidationCompleted {
			return e
		}
	}
	t.Fatalf("no consolidation-completed entry emitted; got %d entries", len(rec.entries))
	return journal.Entry{}
}

func TestConsolidateRunOnce_DefaultMemoryRoot_PerCrewErrorTolerated(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	seedCrewRow(t, db, "crew-run-a", wsID, "Crew A", "crew-run-a")
	seedCrewRow(t, db, "crew-run-b", wsID, "Crew B", "crew-run-b")

	// The consolidator's own DB handle is closed, so Run fails for
	// every crew — the loop must continue and still emit a completed
	// entry with crews_run=0 instead of aborting.
	brokenDB := setupTestDB(t)
	brokenDB.Close()

	h := NewConsolidateHandler(db, newTestLogger())
	rec := &recordingEmitter{}
	h.SetJournal(rec)
	h.SetConsolidator(&consolidate.Consolidator{
		DB:      brokenDB,
		Journal: noopEmitter{},
		Logger:  newTestLogger(),
	})
	// memoryRoot deliberately left "" to exercise the default path.

	h.runOnce(context.Background(), wsID, "", 24*time.Hour, "wkr-err")

	entry := findCompletedEntry(t, rec)
	if entry.WorkspaceID != wsID {
		t.Errorf("entry workspace = %q, want %q", entry.WorkspaceID, wsID)
	}
	if entry.Payload["status"] != "ok" {
		t.Errorf("status = %v, want ok (per-crew failures don't abort the run)", entry.Payload["status"])
	}
	if entry.Payload["crews_run"] != 0 {
		t.Errorf("crews_run = %v, want 0 (every crew failed)", entry.Payload["crews_run"])
	}
	if entry.Payload["worker_id"] != "wkr-err" {
		t.Errorf("worker_id = %v, want wkr-err", entry.Payload["worker_id"])
	}
}

func TestConsolidateRunOnce_NonSkippedRunCounted(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	crewID := seedCrewRow(t, db, "crew-run-full", wsID, "Crew Full", "crew-run-full")

	// 10 candidate-type journal entries trip the consolidator's
	// MinEntries threshold; with no Summarizer wired the run completes
	// non-skipped with 0 rules (heuristic pin-snapshot path).
	for i := 0; i < 10; i++ {
		if _, err := db.Exec(`INSERT INTO journal_entries (id, workspace_id, crew_id, entry_type, severity, actor_type, summary)
			VALUES (?, ?, ?, 'mission.status_change', 'info', 'system', 'status flip')`,
			fmt.Sprintf("je-cov-%d", i), wsID, crewID); err != nil {
			t.Fatalf("seed journal entry %d: %v", i, err)
		}
	}

	h := NewConsolidateHandler(db, newTestLogger())
	rec := &recordingEmitter{}
	h.SetJournal(rec)
	h.SetConsolidator(&consolidate.Consolidator{
		DB:      db,
		Journal: noopEmitter{},
		Logger:  newTestLogger(),
	})
	h.SetMemoryRoot(t.TempDir())

	h.runOnce(context.Background(), wsID, crewID, 24*time.Hour, "wkr-full")

	entry := findCompletedEntry(t, rec)
	if entry.Payload["status"] != "ok" {
		t.Errorf("status = %v, want ok", entry.Payload["status"])
	}
	if entry.Payload["crews_run"] != 1 {
		t.Errorf("crews_run = %v, want 1 (non-skipped run must be counted)", entry.Payload["crews_run"])
	}
	if entry.Payload["rules_appended"] != 0 {
		t.Errorf("rules_appended = %v, want 0 (no summarizer)", entry.Payload["rules_appended"])
	}
	if entry.CrewID != crewID {
		t.Errorf("entry crew = %q, want %q", entry.CrewID, crewID)
	}
}
