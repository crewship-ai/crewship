package pipeline

import (
	"context"
	"testing"
	"time"
)

func TestPendingDurableRetryPreservesReasonAndIdentity(t *testing.T) {
	for _, tc := range []struct {
		name   string
		err    error
		reason string
	}{
		{"capacity", ErrConcurrencyLimitReached, "routine_capacity"},
		{"shutdown", context.Canceled, "retry_backoff"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := newPendingDB(t)
			defer db.Close()
			if _, err := db.Exec(`CREATE TABLE pipeline_runs(id TEXT PRIMARY KEY,workspace_id TEXT,pipeline_id TEXT);` + idempotencySchemaSQL); err != nil {
				t.Fatal(err)
			}
			store := NewPendingRunStore(db)
			now := time.Now().UTC()
			due, expiry := now.Add(-time.Minute), now.Add(time.Hour)
			if _, _, err := store.Enqueue(t.Context(), PendingRun{ID: "claim", WorkspaceID: "w", PipelineID: "p", PipelineSlug: "s", RunID: "planned", IdempotencyKey: "key", DueAt: &due, FireAt: due, ExpiresAt: &expiry}); err != nil {
				t.Fatal(err)
			}
			claim, err := store.ClaimDue(t.Context(), "claim", now)
			if err != nil || claim == nil {
				t.Fatalf("claim=%+v err=%v", claim, err)
			}
			if _, _, err := NewIdempotencyStore(db).LookupOrReserve(t.Context(), "w", "key", "planned", "p", time.Hour); err != nil {
				t.Fatal(err)
			}
			dispatcher := NewPendingRunDispatcher(store, rejectedPendingExecutor{tc.err}, nil)
			dispatcher.recordDispatchError(t.Context(), *claim, tc.err)
			row, err := store.Get(t.Context(), "w", "claim")
			if err != nil || row == nil {
				t.Fatalf("receipt=%+v err=%v", row, err)
			}
			if row.Status != "pending" || row.ReasonCode != tc.reason || row.RunID != "planned" || row.FiredRunID != "" || row.IdempotencyKey != "key" || row.DueAt == nil || !row.DueAt.Equal(due) || row.DispatchAttempts != 1 {
				t.Fatalf("retry changed accepted identity or reason: %+v", row)
			}
			var reserved int
			if err := db.QueryRow(`SELECT count(*) FROM pipeline_run_idempotency WHERE workspace_id='w' AND pipeline_id='p' AND idempotency_key='key'`).Scan(&reserved); err != nil || reserved != 0 {
				t.Fatalf("unstarted reservation retained: count=%d err=%v", reserved, err)
			}
		})
	}
}
