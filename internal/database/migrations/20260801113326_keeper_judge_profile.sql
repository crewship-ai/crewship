-- The judge profile: which capabilities the credential-access judge may use.
--
-- Keeper 1.0 adds four things to the judge — a computed evidence block, a
-- deterministic hard gate on unbound credentials, few-shot precedent, and
-- self-consistency sampling — and every one of them makes the prompt bigger or
-- the decision more expensive. A small model drowned in context decides WORSE,
-- not better. That is a hypothesis, not a fact: the measurement that would
-- settle it (PRD P4) has not run. Wiring any of them on or off as a constant
-- would bake today's guess into a security control that an operator cannot
-- re-aim at the model they actually have — and the model is the whole point,
-- since the target is a judge that fits on a laptop.
--
-- So each capability is a toggle, and three presets (lean / standard /
-- thorough) set them together so it is not seven knobs.
--
-- Nullable, and not merely for tidiness: NULL means "follow the profile", which
-- must stay distinguishable from an explicit 0 meaning "off". Collapsing the two
-- would mean the first operator to switch one capability off silently pinned the
-- other six to whatever they happened to be that afternoon — frozen out of every
-- later default change, which is exactly the change the P4 measurement exists to
-- produce.
--
-- judge_evidence_facts is a comma-separated list of fact keys, '' meaning all of
-- them. Comma-separated rather than JSON because the keys are lower_snake_case
-- by validation, so the split is unambiguous and the stored value, the CLI flag
-- and the audit stamp are one string rather than three encodings.
--
-- The bounds are restated as CHECKs rather than left to keepercfg's validation
-- because the store is not the only writer a database ever sees: a hand-edited
-- row saying "sample the judge 400 times" would exhaust the 20s decision budget
-- and fail closed on every credential request. 512 tokens is the floor at which
-- the incompressible prompt sections (watch policy, tier, facts, the request)
-- still fit — below it the budget would force the truncation of a security
-- instruction, which is the silent degradation PRD P7 exists to prevent.
--
-- These columns ride the existing instance-global singleton (already in
-- NonBackedUpTables), so no backup-intent change: a restored workspace bundle
-- must not carry one instance's judge tuning onto another's.

ALTER TABLE keeper_runtime_settings ADD COLUMN judge_profile TEXT NOT NULL DEFAULT '';
ALTER TABLE keeper_runtime_settings ADD COLUMN judge_evidence INTEGER
    CHECK (judge_evidence IN (0, 1));
ALTER TABLE keeper_runtime_settings ADD COLUMN judge_evidence_facts TEXT NOT NULL DEFAULT '';
ALTER TABLE keeper_runtime_settings ADD COLUMN judge_hard_gate INTEGER
    CHECK (judge_hard_gate IN (0, 1));
ALTER TABLE keeper_runtime_settings ADD COLUMN judge_precedent INTEGER
    CHECK (judge_precedent IN (0, 1));
ALTER TABLE keeper_runtime_settings ADD COLUMN judge_precedent_n INTEGER
    CHECK (judge_precedent_n IS NULL OR (judge_precedent_n >= 1 AND judge_precedent_n <= 10));
ALTER TABLE keeper_runtime_settings ADD COLUMN judge_consistency_samples INTEGER
    CHECK (judge_consistency_samples IS NULL OR (judge_consistency_samples >= 1 AND judge_consistency_samples <= 9));
ALTER TABLE keeper_runtime_settings ADD COLUMN judge_prompt_budget_tokens INTEGER
    CHECK (judge_prompt_budget_tokens IS NULL OR (judge_prompt_budget_tokens >= 512 AND judge_prompt_budget_tokens <= 131072));

-- Which profile decided THIS request.
--
-- Without it two decisions are not comparable. The eval harness replays this
-- table (internal/keeper/eval), and a corpus that mixes verdicts taken with the
-- evidence block against verdicts taken without it measures nothing — it would
-- credit or blame a model for a prompt it never saw. The name alone is not
-- enough either, because a 'standard' with precedent switched off by hand is a
-- different judge from 'standard', so the column holds the resolved stamp
-- (keepercfg.EffectiveProfile.Stamp): the profile name plus every toggle in
-- force.
--
-- Nullable with no default: rows written before this migration were judged by a
-- build that had no profile, and backfilling them with today's would be a claim
-- about the past that is simply false. NULL reads as "unknown", which is what it
-- is.
ALTER TABLE keeper_requests ADD COLUMN judge_profile TEXT;
