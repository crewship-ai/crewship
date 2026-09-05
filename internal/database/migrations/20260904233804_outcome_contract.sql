-- outcome contract (PRD-ISSUES-AND-ROUTINES-2026 §9.6, work package B6,
-- #2349): a single, shared vocabulary for "what did this run actually
-- accomplish", set by the runner and never inferred by a consumer.
--
-- Distinct from both existing columns on these tables:
--   - status is technical (did the process complete, error, or get
--     cancelled) — assignments.status / pipeline_runs.status stay exactly
--     as they are; this migration touches neither.
--   - runverdict (internal/runverdict, run_summary aux slot) is an
--     ADVISORY llm judgment about run quality, generated best-effort after
--     the fact for a small subset of runs.
--   - outcome is the routing decision: it says which of the §9.6 lanes
--     (history-only, digest, an issue comment, the inbox, a retry) this
--     run's result belongs in, and it is DETERMINISTIC — the same routing
--     table (internal/orchestrator/outcome.go) reads it the same way every
--     time, which is the property the routing table needs and an LLM
--     verdict cannot promise.
--
-- Nullable, no default: NULL means "this run predates the outcome contract,
-- or was dispatched with no session/HANDOFF machinery to report one" — the
-- Go layer (finishAssignment, pipeline's persistRunTerminal) always writes
-- a value on every NEW terminal run, defaulting to 'FAILED' when the
-- agent's structured hand-off did not carry a recognised one (§9.6: "A run
-- ending without one is FAILED ... an absent outcome is a bug, not a
-- silent success"). Old rows are not backfilled — there is no hand-off
-- text to re-derive a routing decision from after the fact, and guessing
-- one from status would be exactly the "inferred by a consumer" this
-- column exists to rule out.
ALTER TABLE assignments ADD COLUMN outcome TEXT
    CHECK (outcome IS NULL OR outcome IN (
        'NO_CHANGE', 'SUCCEEDED', 'WORK_CREATED', 'PARTIAL',
        'NEEDS_HUMAN', 'FAILED', 'CANCELLED'
    ));

ALTER TABLE pipeline_runs ADD COLUMN outcome TEXT
    CHECK (outcome IS NULL OR outcome IN (
        'NO_CHANGE', 'SUCCEEDED', 'WORK_CREATED', 'PARTIAL',
        'NEEDS_HUMAN', 'FAILED', 'CANCELLED'
    ));

-- Read-side support for "show me what needs a human" / "show me what's
-- noise" without a full table scan. Partial on NOT NULL: the column is
-- sparse (old rows, and any dispatch path that predates this migration,
-- stay NULL) and the only queries that filter on outcome always know
-- which workspace they are in.
CREATE INDEX IF NOT EXISTS idx_assignments_workspace_outcome
    ON assignments (workspace_id, outcome)
    WHERE outcome IS NOT NULL;

CREATE INDEX IF NOT EXISTS idx_pipeline_runs_workspace_outcome
    ON pipeline_runs (workspace_id, outcome)
    WHERE outcome IS NOT NULL;
