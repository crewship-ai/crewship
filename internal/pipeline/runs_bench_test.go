package pipeline

import (
	"context"
	"database/sql"
	"fmt"
	"testing"
)

// openStepOutputsBenchDB is a minimal single-connection schema covering
// exactly what UpsertStepOutput/FlushStepOutputs/GetStepOutputs touch —
// pipeline_runs (for the cost/duration update) + pipeline_run_step_outputs
// (migration v156).
func openStepOutputsBenchDB(b *testing.B) *RunStore {
	b.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		b.Fatalf("open: %v", err)
	}
	b.Cleanup(func() { db.Close() })
	if _, err := db.ExecContext(context.Background(), `
CREATE TABLE pipeline_runs (
    id                  TEXT PRIMARY KEY,
    workspace_id        TEXT NOT NULL,
    pipeline_id         TEXT NOT NULL,
    pipeline_slug       TEXT NOT NULL,
    pipeline_version    INTEGER,
    definition_hash     TEXT,
    status              TEXT NOT NULL,
    mode                TEXT NOT NULL DEFAULT 'run',
    started_at          TEXT NOT NULL,
    ended_at            TEXT,
    current_step_id     TEXT,
    step_outputs_json   TEXT NOT NULL DEFAULT '{}',
    output              TEXT,
    cost_usd            REAL NOT NULL DEFAULT 0,
    duration_ms         INTEGER NOT NULL DEFAULT 0,
    error_message       TEXT,
    failed_at_step      TEXT,
    error_fingerprint   TEXT,
    invoking_crew_id    TEXT,
    invoking_agent_id   TEXT,
    invoking_user_id    TEXT,
    triggered_via       TEXT NOT NULL DEFAULT 'manual',
    triggered_by_id     TEXT,
    idempotency_key     TEXT,
    inputs_json         TEXT NOT NULL DEFAULT '{}',
    concurrency_key     TEXT,
    metadata_json       TEXT NOT NULL DEFAULT '{}',
    is_replay           INTEGER NOT NULL DEFAULT 0,
    replay_of           TEXT,
    warnings_json       TEXT NOT NULL DEFAULT '[]',
    created_at          TEXT NOT NULL DEFAULT (datetime('now','subsec')),
    updated_at          TEXT NOT NULL DEFAULT (datetime('now','subsec'))
);
CREATE TABLE pipeline_run_step_outputs (
    run_id     TEXT NOT NULL,
    step_id    TEXT NOT NULL,
    output     TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    PRIMARY KEY (run_id, step_id)
);`); err != nil {
		b.Fatalf("schema: %v", err)
	}
	return NewRunStore(db)
}

// seedStepOutputs pre-populates n distinct step outputs for runID, outside
// any timer — the fixture for BenchmarkUpsertStepOutput_ConstantCost's claim
// that a single upsert's cost doesn't depend on how many other steps a run
// already has.
func seedStepOutputs(b *testing.B, store *RunStore, runID string, n int) {
	b.Helper()
	ctx := context.Background()
	if err := store.Insert(ctx, &RunRecord{
		ID: runID, WorkspaceID: "ws_bench", PipelineID: "pln_bench", PipelineSlug: "bench",
		Status: RunStatusRunning, Mode: ModeRun,
	}); err != nil {
		b.Fatalf("seed run: %v", err)
	}
	for i := 0; i < n; i++ {
		stepID := fmt.Sprintf("seed_step_%d", i)
		if err := store.UpsertStepOutput(ctx, runID, stepID, "seed-output", 0, 0); err != nil {
			b.Fatalf("seed step output %d: %v", i, err)
		}
	}
}

// BenchmarkUpsertStepOutput_ConstantCost is the #1411 item 4 fix's proof:
// UpsertStepOutput replaced AppendStepOutput's whole-map JSON rewrite (cost
// scaling with the run's TOTAL step count, O(N) bytes per call / O(N²) over
// a run) with a single-row upsert. Sub-benchmarks pre-seed a run with a
// growing number of ALREADY-PERSISTED steps, then time repeatedly
// overwriting ONE specific step's output — ns/op should stay flat across
// sub-benchmarks regardless of pre-seeded size; the old AppendStepOutput
// would have shown ns/op growing with pre-seeded size, since every call
// re-serialized the entire map.
func BenchmarkUpsertStepOutput_ConstantCost(b *testing.B) {
	for _, preSeeded := range []int{10, 100, 1000, 10000} {
		b.Run(fmt.Sprintf("preseeded_%d_steps", preSeeded), func(b *testing.B) {
			store := openStepOutputsBenchDB(b)
			runID := "run_bench"
			seedStepOutputs(b, store, runID, preSeeded)
			ctx := context.Background()

			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if err := store.UpsertStepOutput(ctx, runID, "repeat_step", "output", 0.01, 100); err != nil {
					b.Fatalf("upsert: %v", err)
				}
			}
		})
	}
}
