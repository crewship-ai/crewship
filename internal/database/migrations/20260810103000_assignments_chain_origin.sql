-- Agent work joins the trace the routine work already has.
--
-- Crewship has two trees of caused work and they never met. A routine run
-- carries pipeline_runs.chain_origin — the run or entry that STARTED the
-- chain — so the causal walk can ask "what set this off" and get one answer
-- for every hop. Agent work has its own parentage (assignments.depth and
-- parent_assignment_id, 20260804181944) and no trace id at all, so the two
-- halves of one causal story are stored in vocabularies that cannot be joined:
-- `crewship chain ENG-6` on an issue whose closure kicked off a whole process
-- answered one node and zero edges, because the process ran as assignments.
--
-- The column is named chain_origin, exactly as on pipeline_runs, and holds the
-- same kind of value. A second name for the same concept — trace_id,
-- origin_run_id — is precisely what keeps the trees apart: the reader would
-- have to know which table it was standing in before it knew which column to
-- read, and every new producer would get one more chance to pick a third name.
--
-- Two hops populate it, and both are inheritance rather than invention:
--
--   * a routine dispatching an agent (the assignment.create crewship verb)
--     resolves the causing run's chain_origin, or names that run when the run
--     IS the root;
--   * an agent delegating to an agent inherits the parent assignment's
--     chain_origin, or names the parent when the parent IS the root.
--
-- Nullable, no default, matching pipeline_runs and pending_runs: an existing
-- row means "did not say", and a reader treats such a row as its own root,
-- which is what it was. A backfill would have to guess a parentage the rows
-- never recorded — and a fabricated origin is worse than an absent one,
-- because it reads as evidence.
ALTER TABLE assignments ADD COLUMN chain_origin TEXT;

-- "Everything in this chain" is the walk's one query against this table, and
-- without an index it is a full scan of a table that grows with every dispatch.
-- Partial, because the rows that say nothing are never a walk's answer.
CREATE INDEX IF NOT EXISTS idx_assignment_chain_origin
    ON assignments (chain_origin)
    WHERE chain_origin IS NOT NULL;
