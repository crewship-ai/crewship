package pipeline

import (
	"context"
	"testing"
	"time"
)

func TestPendingLegacyInterruptionNeverLinksPhantomRun(t *testing.T) {
	for _, started := range []bool{false, true} {
		t.Run(map[bool]string{false: "phantom", true: "actual"}[started], func(t *testing.T) {
			recorder := newEffectRecorder()
			server := recorder.server(t)
			rig := newEffectRig(t, "legacy-interruption", effectDSL(server.URL, 0))
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
			store := NewPendingRunStore(rig.store.db)
			now := time.Now().UTC()
			if _, _, err := store.Enqueue(t.Context(), PendingRun{ID: "legacy-intent", WorkspaceID: "ws_test", PipelineID: rig.pipeline.ID, PipelineSlug: rig.pipeline.Slug, FireAt: now.Add(-time.Minute)}); err != nil {
				t.Fatal(err)
			}
			claim, err := store.ClaimDue(t.Context(), "legacy-intent", now)
			if err != nil || claim == nil {
				t.Fatalf("claim=%+v err=%v", claim, err)
			}
			key := ScheduledFireIdempotencyKey("pending", claim.ID, claim.FireAt.UTC().Format(time.RFC3339Nano))
			// Model shutdown after the executor reserves its generated ID, before
			// any run row exists. Legacy producers do not supply a planned RunID.
			if started {
				result, err := rig.exec.Run(t.Context(), RunInput{RunIDOverride: "actual-start", IdempotencyKey: key, PipelineID: rig.pipeline.ID, WorkspaceID: "ws_test", Mode: ModeRun})
				if err != nil || result == nil || result.Status != "COMPLETED" {
					t.Fatalf("seed actual start: result=%+v err=%v", result, err)
				}
			} else if _, _, err := idem.LookupOrReserve(t.Context(), "ws_test", key, "never-started", rig.pipeline.ID, time.Hour); err != nil {
				t.Fatal(err)
			}
			dispatcher := NewPendingRunDispatcher(store, rig.exec, nil)
			cancelled, cancel := context.WithCancel(t.Context())
			cancel()
			dispatcher.recordDispatchError(cancelled, *claim, context.Canceled)
			dispatcher.now = func() time.Time { return now.Add(time.Minute) }
			dispatcher.fireOne(t.Context(), PendingRun{ID: claim.ID})
			row, err := store.Get(t.Context(), "ws_test", claim.ID)
			if err != nil || row == nil {
				t.Fatalf("receipt=%+v err=%v", row, err)
			}
			if started {
				if row.FiredRunID != "actual-start" || recorder.count("/apply") != 1 {
					t.Fatalf("actual deduplicated run was not linked: receipt=%+v effects=%d", row, recorder.count("/apply"))
				}
				return
			}
			if row.FiredRunID != "" || row.Status != "fired" || row.ReasonCode != "recovery_unknown" || recorder.count("/apply") != 0 {
				t.Fatalf("phantom run was linked or uncertainty hidden: receipt=%+v effects=%d", row, recorder.count("/apply"))
			}
			attempts := row.DispatchAttempts
			dispatcher.fireOne(t.Context(), PendingRun{ID: claim.ID})
			row, err = store.Get(t.Context(), "ws_test", claim.ID)
			if err != nil || row.DispatchAttempts != attempts || row.FiredRunID != "" || row.ReasonCode != "recovery_unknown" || recorder.count("/apply") != 0 {
				t.Fatalf("uncertain work was automatically retried: receipt=%+v err=%v", row, err)
			}
			var owner string
			if err := rig.store.db.QueryRow(`SELECT run_id FROM pipeline_run_idempotency WHERE workspace_id=? AND idempotency_key=?`, "ws_test", key).Scan(&owner); err != nil || owner != "never-started" {
				t.Fatalf("uncertain reservation was released: owner=%q err=%v", owner, err)
			}

		})
	}
}
