-- Runtime overrides for the auxiliary evaluator slots.
--
-- The six aux slots (curator, keeper, behavior, memory_health, negative,
-- run_summary) plus the fallback are the models behind the behavioural watchdog
-- and the four Keeper Reviews sweeps. Their provider/model/timeout came from
-- llm.DefaultAuxiliaryModels layered with YAML (`auxiliary.*`) and
-- CREWSHIP_AUX_<SLOT>_* env — all boot-time. An operator could SEE on the admin
-- Judge models card that five evaluators were pinned to anthropic/claude-haiku-4-5
-- and could not change one without editing env and restarting.
--
-- That matters more than convenience: those five are the PAID models in the
-- Keeper stack. The credential-access judge is local Ollama and costs nothing per
-- decision; the evaluators bill per token. "Which model, and do I want to pay for
-- it" is exactly the decision an operator should be able to make from the console,
-- including pointing a slot at their own local judge.
--
-- One row per overridden slot. A slot with no row inherits the YAML/env value,
-- so an instance nobody has touched behaves exactly as before this table existed
-- — and clearing an override returns the slot to that value rather than to a
-- hardcoded guess. NULL (not 0) is "inherit" for the timeout, because 0 is a
-- meaningful-looking value that would mean "no deadline".
--
-- `slot` is TEXT with no CHECK: the vocabulary lives in llm.Slot and has already
-- gained a member (run_summary, #1403). Pinning it here would mean a table
-- rebuild every time it moves, and the write path validates against the Go list,
-- which is the one that decides resolution anyway. Unknown rows are ignored on
-- read, so a slot removed from the code cannot resurrect itself.
--
-- Instance-global (no workspace_id), like the env vars it supersedes and like
-- keeper_runtime_settings. Registered in NonBackedUpTables so restoring a
-- workspace bundle cannot repoint one instance's evaluators from another's.

CREATE TABLE IF NOT EXISTS keeper_aux_settings (
    slot       TEXT PRIMARY KEY,
    provider   TEXT NOT NULL DEFAULT '',
    model      TEXT NOT NULL DEFAULT '',
    timeout_ms INTEGER CHECK (timeout_ms IS NULL OR timeout_ms > 0),
    updated_by TEXT REFERENCES users(id) ON DELETE SET NULL,
    created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
    updated_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now'))
);
