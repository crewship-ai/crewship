-- The trace could group agent work but not place it.
--
-- 20260810103000 gave assignments a chain_origin, so a routine's dispatch and
-- the agent work it causes finally share one trace id. That collapses a
-- workflow into one row in a list — and it is not enough to DRAW the workflow,
-- because every hop in a trace carries the SAME origin. A graph built from it
-- alone is a bag of nodes with no edges.
--
-- Agent → agent already had its edge (parent_assignment_id, 20260804181944).
-- Routine → agent had none: assignment.create passes author_run_id, the handler
-- resolved it to an origin and dropped the run id itself, so nothing recorded
-- which run dispatched which agent. The causal walk could say "this work
-- belongs to that workflow" and never "this run called this agent" — which is
-- the one sentence the picture exists to say.
--
-- This is the symmetric column: parent_run_id is to a routine dispatch what
-- parent_assignment_id is to a delegation. Exactly one of the two is set;
-- filling both would give a row two parents and let a walk reach the same work
-- by two paths and draw it twice.
--
-- Nullable, no default, matching both siblings: an existing row means "did not
-- say", and an invented edge is worse than a missing one — the missing edge is
-- visible as a gap the walk declares, the invented one is indistinguishable
-- from a real one and points at nothing.
ALTER TABLE assignments ADD COLUMN parent_run_id TEXT;

-- "Which agents did this run dispatch" is the walk's query against this column,
-- and without an index it is a full scan of a table that grows with every
-- dispatch. Partial for the same reason the chain_origin index is: rows that
-- name no run are never this query's answer.
CREATE INDEX IF NOT EXISTS idx_assignment_parent_run
    ON assignments (parent_run_id)
    WHERE parent_run_id IS NOT NULL;
