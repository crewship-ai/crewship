-- Keeper judge profile: the escalation floor (PRD P8).
--
-- The step from L3 to L4 is the largest single trust jump in the product and
-- there was no dial between them. L3 is "administrative access to real
-- infrastructure (SSH, database admin, cloud account)" and the model grants it
-- alone; L4 is the first tier a person always confirms. An operator running a
-- 9B judge who wanted a human on SSH-to-production had exactly one move:
-- relabel the credential L4 — which also imposes the four-eyes rule and L4's
-- 35-character intent minimum, whether they wanted those or not.
--
-- judge_escalate_from raises ONLY the human-approval floor, to a tier the
-- operator names. NULL (the default) leaves the tier table alone, so an
-- instance that does not set it behaves exactly as it did.
--
-- The CHECK is 1-4 rather than an open integer because it is a credential tier,
-- and a hand-edited 7 would otherwise resolve to "escalate nothing" — an
-- out-of-range floor that silently disables the control the row was added for.
--
-- Separate migration rather than an edit to 20260801113326: that one has already
-- been applied on a dev instance, and rewriting an applied migration desyncs the
-- ledger hash for the sake of saving one ALTER.

ALTER TABLE keeper_runtime_settings ADD COLUMN judge_escalate_from INTEGER
    CHECK (judge_escalate_from IS NULL OR (judge_escalate_from >= 1 AND judge_escalate_from <= 4));
