package pipeline

import (
	"fmt"
	"testing"
	"time"
)

// This is a storage-level lost-owner reproduction, not a kill -9 end-to-end test.
// No executor or provider is invoked between ClaimDue and opening a fresh store.
func TestReviewPendingPreStartClaimRemainsRecoverable(t *testing.T) {
	db := newPendingDB(t)
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE pipeline_runs(id TEXT PRIMARY KEY,workspace_id TEXT,pipeline_id TEXT);` + idempotencySchemaSQL); err != nil {
		t.Fatal(err)
	}
	before := NewPendingRunStore(db)
	now := time.Now().UTC()
	_, _, err := before.Enqueue(t.Context(), PendingRun{ID: "review-no-start", RunID: "planned-review", WorkspaceID: "review-w", PipelineID: "review-p", PipelineSlug: "review", FireAt: now.Add(-time.Minute), TriggeredVia: TriggeredViaWebhook})
	if err != nil {
		t.Fatal(err)
	}
	claimed, err := before.ClaimDue(t.Context(), "review-no-start", now)
	if err != nil || claimed == nil {
		t.Fatalf("claim: %+v %v", claimed, err)
	}
	// Simulate losing the caller before Executor.Run. SQL bytes survive.
	if err := RecoverPendingStartsAtBoot(t.Context(), db); err != nil {
		t.Fatal(err)
	}
	after := NewPendingRunStore(db)
	due, err := after.DueRuns(t.Context(), now.Add(time.Hour), 25)
	if err != nil {
		t.Fatal(err)
	}
	row, err := after.Get(t.Context(), "review-w", "review-no-start")
	if err != nil || row == nil {
		t.Fatalf("receipt missing: %+v %v", row, err)
	}
	if len(due) == 0 && row.FiredRunID == "" && row.Status == "fired" {
		t.Fatalf("accepted claim is stranded: status=%s fired_run_id=%q due=%d; no executor was invoked", row.Status, row.FiredRunID, len(due))
	}
}

func TestReviewPendingWebhookAttributionRoundTrip(t *testing.T) {
	db := newPendingDB(t)
	defer db.Close()
	s := NewPendingRunStore(db)
	now := time.Now().UTC()
	_, _, err := s.Enqueue(t.Context(), PendingRun{ID: "review-origin", WorkspaceID: "review-w", PipelineID: "review-p", PipelineSlug: "review", FireAt: now.Add(-time.Minute), TriggeredVia: TriggeredViaWebhook, TriggeredByID: "review-webhook"})
	if err != nil {
		t.Fatal(err)
	}
	row, err := s.ClaimDue(t.Context(), "review-origin", now)
	if err != nil || row == nil {
		t.Fatalf("claim: %+v %v", row, err)
	}
	via, by := effectivePendingTrigger(*row)
	if via != TriggeredViaWebhook || by != "review-webhook" {
		t.Fatalf("origin changed: %s/%s", via, by)
	}
}

func TestPendingStartRecoveryPreservesLinkedStartsAndUnknownClaims(t *testing.T) {
	db := newPendingDB(t)
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE pipeline_runs(id TEXT PRIMARY KEY,workspace_id TEXT,pipeline_id TEXT);` + idempotencySchemaSQL); err != nil {
		t.Fatal(err)
	}
	store := NewPendingRunStore(db)
	now := time.Now().UTC()
	for _, id := range []string{"linked-old-binary", "unknown", "unstarted", "started"} {
		if _, _, err := store.Enqueue(t.Context(), PendingRun{ID: id, WorkspaceID: "w", PipelineID: "p", PipelineSlug: "s", FireAt: now.Add(-time.Minute)}); err != nil {
			t.Fatal(err)
		}
		if _, err := store.ClaimDue(t.Context(), id, now); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(`UPDATE pending_runs SET fired_run_id='old-binary-run' WHERE id='linked-old-binary';
 UPDATE pending_runs SET run_id='planned-unstarted',idempotency_key='own-key' WHERE id='unstarted';
 UPDATE pending_runs SET run_id='planned-started',idempotency_key='started-key' WHERE id='started';
 INSERT INTO pipeline_runs VALUES('planned-started','w','p');`); err != nil {
		t.Fatal(err)
	}
	idem := NewIdempotencyStore(db)
	for key, run := range map[string]string{"own-key": "planned-unstarted", "started-key": "planned-started"} {
		if _, _, err := idem.LookupOrReserve(t.Context(), "w", key, run, "p", time.Hour); err != nil {
			t.Fatal(err)
		}
	}
	for range 2 {
		if err := RecoverPendingStartsAtBoot(t.Context(), db); err != nil {
			t.Fatal(err)
		}
	}
	for id, want := range map[string]string{"linked-old-binary": "fired", "unknown": "fired", "unstarted": "pending", "started": "fired"} {
		row, err := store.Get(t.Context(), "w", id)
		if err != nil || row.Status != want {
			t.Fatalf("%s: %+v %v", id, row, err)
		}
	}
	var reason string
	if err := db.QueryRow(`SELECT reason_code FROM pending_runs WHERE id='unknown'`).Scan(&reason); err != nil || reason != "recovery_unknown" {
		t.Fatalf("unknown=%q %v", reason, err)
	}
	var linked string
	if err := db.QueryRow(`SELECT fired_run_id FROM pending_runs WHERE id='started'`).Scan(&linked); err != nil || linked != "planned-started" {
		t.Fatalf("started link=%q %v", linked, err)
	}
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM pipeline_run_idempotency WHERE idempotency_key='own-key'`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("own reservation was not released: %d %v", count, err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM pipeline_run_idempotency WHERE idempotency_key='started-key'`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("started reservation released: %d %v", count, err)
	}
}

func TestPendingStartRecoveryDoesNotReleaseAnotherOwner(t *testing.T) {
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
	if _, err := store.ClaimDue(t.Context(), "claim", now); err != nil {
		t.Fatal(err)
	}
	if _, _, err := NewIdempotencyStore(db).LookupOrReserve(t.Context(), "w", "key", "different-owner", "p", time.Hour); err != nil {
		t.Fatal(err)
	}
	if err := RecoverPendingStartsAtBoot(t.Context(), db); err != nil {
		t.Fatal(err)
	}
	var owner, reason string
	if err := db.QueryRow(`SELECT run_id FROM pipeline_run_idempotency WHERE workspace_id='w' AND pipeline_id='p' AND idempotency_key='key'`).Scan(&owner); err != nil || owner != "different-owner" {
		t.Fatalf("changed another owner: %q %v", owner, err)
	}
	if err := db.QueryRow(`SELECT reason_code FROM pending_runs WHERE id='claim' AND status='fired'`).Scan(&reason); err != nil || reason != "recovery_unknown" {
		t.Fatalf("unsafe claim retried: %q %v", reason, err)
	}
}

// Rebuild stores at the two crash boundaries, then dispatch through the real
// executor. This is a persisted-state crash simulation, not a kill -9 test.
func TestPendingStartRecoveryDispatchesStableIdentityAfterCrash(t *testing.T) {
	for _, claimed := range []bool{false, true} {
		t.Run(fmt.Sprintf("claimed_%t", claimed), func(t *testing.T) {
			recorder := newEffectRecorder()
			server := recorder.server(t)
			rig := newEffectRig(t, "crash-recovery", effectDSL(server.URL, 0))
			schemaDB := newPendingDB(t)
			defer schemaDB.Close()
			var schema string
			if err := schemaDB.QueryRow(`SELECT sql FROM sqlite_schema WHERE type='table' AND name='pending_runs'`).Scan(&schema); err != nil {
				t.Fatal(err)
			}
			if _, err := rig.store.db.Exec(schema + `;` + idempotencySchemaSQL); err != nil {
				t.Fatal(err)
			}
			idem := NewIdempotencyStore(rig.store.db)
			rig.exec.WithIdempotencyStore(idem)
			before := NewPendingRunStore(rig.store.db)
			now := time.Now().UTC()
			_, _, err := before.Enqueue(t.Context(), PendingRun{ID: "crash-intent", WorkspaceID: "ws_test", PipelineID: rig.pipeline.ID, PipelineSlug: rig.pipeline.Slug, RunID: "planned-crash-run", IdempotencyKey: "own-crash-key", FireAt: now.Add(-time.Minute)})
			if err != nil {
				t.Fatal(err)
			}
			if claimed {
				if _, err := before.ClaimDue(t.Context(), "crash-intent", now); err != nil {
					t.Fatal(err)
				}
				if _, _, err := idem.LookupOrReserve(t.Context(), "ws_test", "own-crash-key", "planned-crash-run", rig.pipeline.ID, time.Hour); err != nil {
					t.Fatal(err)
				}
			}
			if err := RecoverPendingStartsAtBoot(t.Context(), rig.store.db); err != nil {
				t.Fatal(err)
			}
			after := NewPendingRunStore(rig.store.db)
			dispatcher := NewPendingRunDispatcher(after, rig.exec, nil)
			dispatcher.fireOne(t.Context(), PendingRun{ID: "crash-intent"})
			actual, err := rig.runStore.Get(t.Context(), "planned-crash-run")
			if err != nil || actual.Status != RunStatusCompleted {
				t.Fatalf("accepted work did not execute: %+v %v", actual, err)
			}
			row, err := after.Get(t.Context(), "ws_test", "crash-intent")
			if err != nil || row.FiredRunID != "planned-crash-run" {
				t.Fatalf("lost identity: %+v %v", row, err)
			}
			if err := RecoverPendingStartsAtBoot(t.Context(), rig.store.db); err != nil {
				t.Fatal(err)
			}
			dispatcher.fireOne(t.Context(), PendingRun{ID: "crash-intent"})
			if recorder.count("/apply") != 1 {
				t.Fatal("recovery repeated an external effect")
			}
		})
	}
}
