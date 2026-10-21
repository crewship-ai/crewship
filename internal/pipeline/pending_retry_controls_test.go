package pipeline

import (
	"testing"
	"time"
)

func TestPendingRetryPreservesStartedForeignAndNewerClaims(t *testing.T) {
	for _, state := range []string{"started", "foreign_owner", "newer_claim"} {
		t.Run(state, func(t *testing.T) {
			db := newPendingDB(t)
			defer db.Close()
			if _, err := db.Exec(`CREATE TABLE pipeline_runs(id TEXT PRIMARY KEY,workspace_id TEXT,pipeline_id TEXT);` + idempotencySchemaSQL); err != nil {
				t.Fatal(err)
			}
			store := NewPendingRunStore(db)
			now := time.Now().UTC()
			if _, _, err := store.Enqueue(t.Context(), PendingRun{ID: "claim", WorkspaceID: "w", PipelineID: "p", PipelineSlug: "s", RunID: "planned", IdempotencyKey: "key", FireAt: now.Add(-time.Minute)}); err != nil {
				t.Fatal(err)
			}
			claim, err := store.ClaimDue(t.Context(), "claim", now)
			if err != nil || claim == nil {
				t.Fatalf("claim=%+v err=%v", claim, err)
			}
			owner := "planned"
			if state == "foreign_owner" {
				owner = "another-owner"
			}
			if _, _, err := NewIdempotencyStore(db).LookupOrReserve(t.Context(), "w", "key", owner, "p", time.Hour); err != nil {
				t.Fatal(err)
			}
			switch state {
			case "started":
				if _, err := db.Exec(`INSERT INTO pipeline_runs VALUES('planned','w','p')`); err != nil {
					t.Fatal(err)
				}
			case "newer_claim":
				if _, err := db.Exec(`UPDATE pending_runs SET dispatch_attempts=dispatch_attempts+1 WHERE id='claim'`); err != nil {
					t.Fatal(err)
				}
			}
			if err := store.requeueUnstartedClaim(t.Context(), *claim, "retry", nil); err != nil {
				t.Fatal(err)
			}
			row, err := store.Get(t.Context(), "w", "claim")
			if err != nil || row == nil || row.Status != "fired" {
				t.Fatalf("unsafe retry: %+v err=%v", row, err)
			}
			wantLink := ""
			if state == "started" {
				wantLink = "planned"
			}
			if row.FiredRunID != wantLink {
				t.Fatalf("link=%q want=%q", row.FiredRunID, wantLink)
			}
			var retained string
			if err := db.QueryRow(`SELECT run_id FROM pipeline_run_idempotency WHERE workspace_id='w' AND pipeline_id='p' AND idempotency_key='key'`).Scan(&retained); err != nil || retained != owner {
				t.Fatalf("reservation=%q want=%q err=%v", retained, owner, err)
			}
		})
	}
}
