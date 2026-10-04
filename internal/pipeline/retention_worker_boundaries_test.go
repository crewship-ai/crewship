package pipeline

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/journal"
	"github.com/crewship-ai/crewship/internal/tsformat"
)

type retentionBoundaryEmitter struct {
	events chan journal.Entry
	err    error
}

func (e retentionBoundaryEmitter) Emit(_ context.Context, entry journal.Entry) (string, error) {
	e.events <- entry
	return "audit", e.err
}
func (retentionBoundaryEmitter) Flush(context.Context) error { return nil }

func TestRetentionWorkerSweepsImmediatelyAndOnLaterTicks(t *testing.T) {
	for _, interval := range []time.Duration{0, 5 * time.Millisecond} {
		t.Run(interval.String(), func(t *testing.T) {
			db := openRetentionTestDB(t)
			t.Cleanup(func() { _ = db.Close() })
			ctx, cancel := context.WithCancel(t.Context())
			t.Cleanup(cancel)
			old := tsformat.Format(time.Now().Add(-100 * 24 * time.Hour))
			for i := 0; i < DefaultKeepLastNRunsPerPipeline+2; i++ {
				insertRunForRetention(t, db, fmt.Sprintf("initial-%d", i), "ws_a", "pln_a", "completed", old, "")
			}
			emitter := retentionBoundaryEmitter{events: make(chan journal.Entry, 8)}
			StartRunRetentionSweeper(ctx, db, emitter, interval)
			wait := func(want int) {
				t.Helper()
				select {
				case event := <-emitter.events:
					if event.WorkspaceID != "ws_a" || event.Type != journal.EntryPipelineRunsSwept || event.Payload["deleted_count"] != want {
						t.Fatalf("retention audit = %+v", event)
					}
				case <-time.After(5 * time.Second):
					t.Fatal("retention worker did not sweep")
				}
			}
			wait(2)
			if interval > 0 {
				tx, err := db.BeginTx(ctx, nil)
				if err != nil {
					t.Fatal(err)
				}
				for i := 0; i < 3; i++ {
					if _, err := tx.ExecContext(ctx, `INSERT INTO pipeline_runs(id,workspace_id,pipeline_id,status,started_at) VALUES(?,'ws_a','pln_a','completed',?)`, fmt.Sprintf("later-%d", i), old); err != nil {
						_ = tx.Rollback()
						t.Fatal(err)
					}
				}
				if err := tx.Commit(); err != nil {
					t.Fatal(err)
				}
				wait(3)
			}
			cancel()
		})
	}
}

func TestRetentionCommittedDeletionSurvivesAuditRefusal(t *testing.T) {
	db := openRetentionTestDB(t)
	t.Cleanup(func() { _ = db.Close() })
	insertRunForRetention(t, db, "old", "ws_a", "pln_a", "completed", tsformat.Format(time.Now().Add(-100*24*time.Hour)), "")
	emitter := retentionBoundaryEmitter{events: make(chan journal.Entry, 1), err: errors.New("journal unavailable")}
	n, err := SweepRunRetention(t.Context(), db, emitter, "ws_a", 90, 0)
	if err != nil || n != 1 || runExists(t, db, "old") {
		t.Fatalf("committed sweep reported as failed: %d, %v", n, err)
	}
	if n, err := SweepRunRetention(t.Context(), db, emitter, "ws_a", 90, 0); err != nil || n != 0 {
		t.Fatalf("repeat sweep = %d, %v", n, err)
	}
	if len(emitter.events) != 1 {
		t.Fatal("repeat sweep emitted duplicate deletion")
	}
}

func TestRetentionWorkspaceStorageErrorsReachCaller(t *testing.T) {
	if err := SweepAllWorkspacesRunRetention(t.Context(), nil, nil, 0); err == nil {
		t.Fatal("missing store ignored")
	}
	db := openRetentionTestDB(t)
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.Exec(`UPDATE workspaces SET run_retention_days='malformed' WHERE id='ws_a'`); err != nil {
		t.Fatal(err)
	}
	if err := SweepAllWorkspacesRunRetention(t.Context(), db, nil, 0); err == nil {
		t.Fatal("malformed retention silently applied")
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if err := SweepAllWorkspacesRunRetention(t.Context(), db, nil, 0); err == nil {
		t.Fatal("closed storage ignored")
	}
}
