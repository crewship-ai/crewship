-- #2210 — three journal filters that miss their index.
--
-- All three were verified with EXPLAIN QUERY PLAN against a `.backup` copy of
-- a live workspace (3,594 entries) with sqlite_stat1 populated, before and
-- after. A migration that does not move the plan is worse than none: it looks
-- like the problem is solved.
--
--
-- 1. severity — a single-value IN does not imply a partial index predicate.
--
-- v146 built idx_journal_ws_sev_ts as
--   (workspace_id, severity, ts DESC) WHERE severity IN ('warn','error')
-- for the per-workspace errors/warnings view. But the Timeline's severity
-- select sends exactly ONE severity, and SQLite will only use a partial index
-- when the query's WHERE provably implies the index's. `severity IN ('error')`
-- does not imply `severity IN ('warn','error')` as far as the planner's
-- implication prover is concerned — nor does `severity = 'error'` — so the
-- index built for this feature was dead for the query it was built for:
--
--   severity IN ('warn','error')  ->  SEARCH ... USING INDEX idx_journal_ws_sev_ts
--   severity IN ('error')         ->  SEARCH ... USING INDEX idx_journal_ws_ts   <- scan
--   severity = 'error'            ->  SEARCH ... USING INDEX idx_journal_ws_ts   <- scan
--
-- The fix is a full (non-partial) twin. Widening the partial predicate to
-- cover every severity value IS just dropping the predicate, and the CSV
-- alternative — making the client always send both values — would have made
-- the UI lie about what it filtered.
--
-- v146's partial index is deliberately KEPT rather than replaced: it holds 18
-- of 3,594 rows on the sampled workspace, and with statistics present the
-- planner still prefers it for the two-value errors+warnings shape. Dropping
-- it traded one regression for another (measured: that query fell back to
-- idx_journal_ws_ts). The two coexist, each winning the shape it is for.
CREATE INDEX IF NOT EXISTS idx_journal_ws_severity_ts
    ON journal_entries(workspace_id, severity, ts DESC);

-- 2. priority — the same failure, one migration older.
--
-- v54's idx_journal_entries_priority is
--   (workspace_id, priority) WHERE priority != 'normal'
-- and `priority IN ('high')` does not imply `priority != 'normal'` either.
--
-- Here the partial index IS strictly superseded: the new index has the same
-- leading columns, covers every priority value, and adds ts DESC so the
-- ORDER BY does not need a sort the old one could never help with. Keeping
-- both would only cost write amplification on the hottest INSERT path in the
-- product, so the redundant one goes.
CREATE INDEX IF NOT EXISTS idx_journal_ws_priority_ts
    ON journal_entries(workspace_id, priority, ts DESC);
DROP INDEX IF EXISTS idx_journal_entries_priority;

-- 3. actor_id — the missing leg that cost the whole run_id filter its index.
--
-- journal.Query.RunID emits `(trace_id = ? OR actor_id = ? OR run_id = ?)`,
-- because a run id reaches the journal by three doors depending on the engine
-- that ran it. SQLite's OR-optimization turns such a clause into an index
-- union ONLY when every leg is indexable; one unindexed leg and the entire
-- predicate degrades to a scan of the workspace partition. trace_id had
-- idx_journal_ws_trace (v60) and run_id had idx_journal_ws_run (v120), but
-- actor_id had no index at all — idx_journal_actor_ts is on actor_TYPE, a
-- different column that reads almost the same.
--
-- So the claim v120 makes in its own comment ("the run-logs query then unions
-- two B-tree probes") was never true of the query it describes, and Count(),
-- which emits no LIMIT, scanned every row in the workspace on every
-- /journal/count?run_id= call.
--
-- Partial on actor_id IS NOT NULL: 1,879 of 3,594 rows on the sampled
-- workspace carry one, and `actor_id = ?` implies IS NOT NULL, which the
-- planner does prove (verified: the union picks this index).
CREATE INDEX IF NOT EXISTS idx_journal_ws_actor_ts
    ON journal_entries(workspace_id, actor_id, ts DESC)
    WHERE actor_id IS NOT NULL;

-- No ANALYZE here: Migrate() runs one after the whole registry has applied
-- (see the comment above the ExecContext("ANALYZE") call in migrate.go), and
-- it is load-bearing for this migration in particular. Without sqlite_stat1
-- rows for the new indexes the planner has no row-count evidence and the
-- three-way OR union above is NOT chosen — measured: the plan degrades to a
-- workspace scan on an arbitrary index that happens to lead with
-- workspace_id.
