package database

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/crewship-ai/crewship/internal/tsformat"
)

// migrationRunStepOutputs (v159) replaces the O(N²) step_outputs_json
// rewrite pattern (#1411 item 4) with a normalized per-step table.
//
// Before this migration, RunStore.AppendStepOutput re-serialized and
// rewrote the ENTIRE step_outputs_json blob on every step boundary — a
// 100-step run wrote ~100 copies of steps 1..N, 1..N-1, ... 1..1 worth of
// bytes over its lifetime. pipeline_run_step_outputs makes each step
// boundary a single-row upsert instead: O(1) bytes written per step,
// O(N) total over a run, not O(N²).
//
// pipeline_runs.step_outputs_json is NOT dropped here — dropping a
// NOT NULL column requires a SQLite table rebuild, and the column may
// still be read by external tooling that queries it directly. It simply
// stops being written on the hot per-step path after this migration; new
// rows get '{}' at insert (RunStore.Insert's existing default) and never
// grow past that. Existing rows' history is preserved by the backfill
// below so both new read paths (resume.go, api/pipeline_runs.go GetRun)
// find pre-migration runs' outputs in the new table too.
func migrationRunStepOutputs(ctx context.Context, tx *sql.Tx, logger *slog.Logger) error {
	if _, err := tx.ExecContext(ctx, `
CREATE TABLE IF NOT EXISTS pipeline_run_step_outputs (
    run_id     TEXT NOT NULL REFERENCES pipeline_runs(id) ON DELETE CASCADE,
    step_id    TEXT NOT NULL,
    output     TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    PRIMARY KEY (run_id, step_id)
)`); err != nil {
		return fmt.Errorf("run step outputs: create table: %w", err)
	}

	if err := backfillRunStepOutputs(ctx, tx, logger); err != nil {
		return err
	}
	return nil
}

// backfillRunStepOutputs seeds pipeline_run_step_outputs from every
// existing pipeline_runs.step_outputs_json blob so pre-migration runs
// (including ones still in-flight at deploy time — a run parked
// 'waiting' on an approval may be resumed weeks later) keep their
// per-step history under the new read path. Rows with an empty/'{}'
// blob contribute nothing, which is the common case (most runs are
// short-lived and this migration runs promptly after deploy).
func backfillRunStepOutputs(ctx context.Context, tx *sql.Tx, logger *slog.Logger) error {
	rows, err := tx.QueryContext(ctx,
		`SELECT id, step_outputs_json, updated_at FROM pipeline_runs WHERE step_outputs_json IS NOT NULL AND step_outputs_json != '' AND step_outputs_json != '{}'`)
	if err != nil {
		return fmt.Errorf("run step outputs backfill: query: %w", err)
	}
	type row struct {
		runID, blob, updatedAt string
	}
	var toBackfill []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.runID, &r.blob, &r.updatedAt); err != nil {
			rows.Close()
			return fmt.Errorf("run step outputs backfill: scan: %w", err)
		}
		toBackfill = append(toBackfill, r)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return fmt.Errorf("run step outputs backfill: iterate: %w", err)
	}
	rows.Close()

	stmt, err := tx.PrepareContext(ctx, `
INSERT INTO pipeline_run_step_outputs (run_id, step_id, output, updated_at)
VALUES (?, ?, ?, ?)
ON CONFLICT (run_id, step_id) DO UPDATE SET output = excluded.output, updated_at = excluded.updated_at`)
	if err != nil {
		return fmt.Errorf("run step outputs backfill: prepare: %w", err)
	}
	defer stmt.Close()

	backfilled := 0
	for _, r := range toBackfill {
		var outputs map[string]string
		if err := json.Unmarshal([]byte(r.blob), &outputs); err != nil {
			// A malformed historical blob shouldn't fail the whole
			// migration — log and skip that run's backfill, matching
			// the "honesty over heroics" contract the rest of the
			// resume path follows (see resume.go).
			logger.Warn("run step outputs backfill: unmarshal failed, skipping run",
				"run_id", r.runID, "error", err)
			continue
		}
		updatedAt := r.updatedAt
		if updatedAt == "" {
			updatedAt = tsformat.Format(time.Now().UTC())
		}
		for stepID, output := range outputs {
			if _, err := stmt.ExecContext(ctx, r.runID, stepID, output, updatedAt); err != nil {
				return fmt.Errorf("run step outputs backfill: insert run %s step %s: %w", r.runID, stepID, err)
			}
		}
		backfilled++
	}
	logger.Info("run step outputs backfill complete", "runs_backfilled", backfilled, "runs_scanned", len(toBackfill))
	return nil
}
