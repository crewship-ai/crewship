-- How deep a run sits in a COMPOSED chain, and what started that chain.
--
-- Self-composition (automation fires routine → routine acts on an issue →
-- that change fires an automation) makes cycles trivially constructible: the
-- first user to close a loop takes their instance down. The budget that stops
-- it has to be carried by the run itself, because every other candidate — a
-- per-process counter, a per-automation counter — resets at exactly the wrong
-- moment (restart, second automation, second workspace).
--
-- chain_depth is the number of COMPOSED hops from whatever a human did: a run
-- a person started is 0, a routine that run called is 1, an automation fired
-- by an event that run emitted is 2. It is deliberately NOT the same number
-- as the executor's `depth` argument (in-process call_pipeline nesting, which
-- resets per top-level run and is not persisted): a chain can leave the
-- process and come back through the journal, and this column is what survives
-- that hop.
--
-- chain_origin is the id of the run or journal entry that started the chain,
-- so an operator asking "what set this off" has one hop to the answer instead
-- of walking triggered_by_id backwards through runs that may have been swept.
--
-- Existing rows keep chain_depth 0 / chain_origin NULL: they predate the
-- concept and every one of them was, in fact, a chain root.

ALTER TABLE pipeline_runs ADD COLUMN chain_depth INTEGER NOT NULL DEFAULT 0;

ALTER TABLE pipeline_runs ADD COLUMN chain_origin TEXT;

-- "Show me the chains" — the operator query when a loop is suspected — is
-- "rows in this workspace with chain_depth > 0", which without an index is a
-- full scan of the busiest table in the schema.
CREATE INDEX IF NOT EXISTS idx_pipeline_runs_chain
    ON pipeline_runs (workspace_id, chain_depth)
    WHERE chain_depth > 0;
