package database

// migrationEvalRunsOnline (v97) extends eval_runs to support the online
// sampling kind. The sampler watches completed pipeline runs and grades
// a configurable percentage of them through the existing rubric grader
// so production traffic continuously feeds the ADLC phase-7 drift
// detector — not just on-demand replays or scheduled regression suites.
//
// SQLite CHECK constraints are immutable in place; widening the
// (replay|regression) set to include 'online' requires the standard
// recreate dance:
//
//  1. Rename the old table aside.
//  2. CREATE a fresh table with the widened CHECK + the two new
//     columns (routine_slug, pipeline_run_id) that link an online
//     eval back to the routine that triggered the sample.
//  3. INSERT … SELECT to copy every row, padding the new columns
//     with NULL.
//  4. DROP the renamed old table.
//  5. Recreate every index. We do this explicitly rather than relying
//     on SQLite to carry indexes over a rename (it doesn't).
//
// trace_id ties an eval row back to the OTel trace the sample came
// from so an operator who clicks "why was this graded poorly?" in the
// eval UI lands on the actual trace view. Indexed for the
// /api/v1/feedback?trace_id query path which already exists from v96.
//
// Backwards-compat invariant: every existing row keeps its kind and
// every column value. The recreate is byte-equivalent for replay /
// regression rows; only the CHECK relaxes and the four new columns
// (which default NULL) are added.
const migrationEvalRunsOnline = `
ALTER TABLE eval_runs RENAME TO eval_runs_pre_v97;

CREATE TABLE eval_runs (
    id TEXT PRIMARY KEY,
    workspace_id TEXT NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    kind TEXT NOT NULL CHECK(kind IN ('replay','regression','online')),
    mission_id TEXT REFERENCES missions(id) ON DELETE SET NULL,
    baseline_mission_id TEXT REFERENCES missions(id) ON DELETE SET NULL,
    candidate_mission_id TEXT REFERENCES missions(id) ON DELETE SET NULL,
    status TEXT NOT NULL DEFAULT 'queued' CHECK(status IN ('queued','running','completed','failed')),
    result TEXT,
    seed INTEGER NOT NULL DEFAULT 0,
    signature TEXT,
    total_tokens INTEGER NOT NULL DEFAULT 0,
    total_cost_usd REAL NOT NULL DEFAULT 0,
    regressed INTEGER NOT NULL DEFAULT 0,
    created_by TEXT,
    created_at TEXT NOT NULL DEFAULT (datetime('now')),
    completed_at TEXT,
    -- v96 additions: link an online sample back to the routine + run.
    routine_slug TEXT,
    pipeline_run_id TEXT,
    trace_id TEXT,
    sample_rate REAL
);

INSERT INTO eval_runs (
    id, workspace_id, kind, mission_id, baseline_mission_id,
    candidate_mission_id, status, result, seed, signature,
    total_tokens, total_cost_usd, regressed, created_by,
    created_at, completed_at
)
SELECT id, workspace_id, kind, mission_id, baseline_mission_id,
       candidate_mission_id, status, result, seed, signature,
       total_tokens, total_cost_usd, regressed, created_by,
       created_at, completed_at
FROM eval_runs_pre_v97;

DROP TABLE eval_runs_pre_v97;

CREATE INDEX IF NOT EXISTS idx_eval_runs_ws_created ON eval_runs(workspace_id, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_eval_runs_kind ON eval_runs(kind, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_eval_runs_mission ON eval_runs(mission_id, created_at DESC) WHERE mission_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_eval_runs_routine ON eval_runs(routine_slug, created_at DESC) WHERE routine_slug IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_eval_runs_trace ON eval_runs(trace_id) WHERE trace_id IS NOT NULL;
-- Idempotency guard for the online sampler: enforce one online eval row
-- per pipeline_run_id at the schema layer. Without this, a duplicate
-- sampler instance (HA + accidental double-start) or a crash-recovery
-- watermark replay can enqueue the same run twice and the grader runs
-- twice for it. The partial WHERE limits the constraint to online rows
-- so replay/regression runs that legitimately reference the same
-- mission across many eval_runs are unaffected.
CREATE UNIQUE INDEX IF NOT EXISTS uq_eval_runs_online_pipeline_run
    ON eval_runs(pipeline_run_id) WHERE kind = 'online' AND pipeline_run_id IS NOT NULL;
`
