-- Rebuild keeper_runtime_settings to widen the judge_escalate_from range.
--
-- SQLite cannot alter a CHECK constraint in place; the table has to be recreated.
-- The range changed from 1-4 to 1-5 the same day it was added, when the dial
-- gained its "never" value (full autonomy, no tier escalated on the model's
-- behalf), and an instance that had already applied the first version was left
-- rejecting the new value with a constraint failure rather than a message.
--
-- The escalate CHECK is DROPPED rather than widened, and that is the point of
-- doing this as a rebuild rather than a one-off repair.
--
-- A CHECK is a poor home for a policy range. This one encodes "which credential
-- tiers exist, plus a sentinel" — a product decision that has already moved once
-- in a day and can move again, and every move costs a table rebuild on every
-- instance. Meanwhile keepercfg.validateProfile already rejects the same values
-- and answers with "escalate-from must be a credential tier 1-4, 5 for never, or
-- 0 to leave the tier table alone" instead of "constraint failed". Two
-- validators, one of which is unhelpful and expensive to change, is one too many.
--
-- The other CHECKs are preserved verbatim. They constrain shapes rather than
-- policy (a boolean is 0 or 1; a timeout is milliseconds within a sane band) and
-- have no reason to move.
--
-- Column order and defaults are carried across exactly, because
-- BackupTableIntent and the scan/upsert in internal/keepercfg name these columns
-- positionally.
--
-- judge_escalate_from is NOT copied, and is the one deliberate data loss here.
-- A rebuild that read the old column would fail outright on a database where
-- 20260801141432 never ran — which the repair-ledger path constructs, and which
-- is exactly the situation a rebuild has to survive. So the column is recreated
-- empty and an instance that had set a floor re-sets it. That is one CLI call,
-- the setting is a day old and has never merged, and the alternative is a
-- migration that cannot run on a renumbered database.

PRAGMA foreign_keys = OFF;

CREATE TABLE keeper_runtime_settings_new (
    id                 TEXT PRIMARY KEY CHECK (id = 'singleton'),
    enabled            INTEGER CHECK (enabled IN (0, 1)),
    judge_provider     TEXT NOT NULL DEFAULT '',
    judge_endpoint_url TEXT NOT NULL DEFAULT '',
    judge_wire         TEXT NOT NULL DEFAULT '',
    judge_model        TEXT NOT NULL DEFAULT '',
    updated_by         TEXT REFERENCES users(id) ON DELETE SET NULL,
    created_at         TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
    updated_at         TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
    judge_timeout_ms   INTEGER
        CHECK (judge_timeout_ms IS NULL OR (judge_timeout_ms >= 1000 AND judge_timeout_ms <= 120000)),
    judge_profile      TEXT NOT NULL DEFAULT '',
    judge_evidence     INTEGER CHECK (judge_evidence IN (0, 1)),
    judge_evidence_facts TEXT NOT NULL DEFAULT '',
    judge_hard_gate    INTEGER CHECK (judge_hard_gate IN (0, 1)),
    judge_precedent    INTEGER CHECK (judge_precedent IN (0, 1)),
    judge_precedent_n  INTEGER
        CHECK (judge_precedent_n IS NULL OR (judge_precedent_n >= 1 AND judge_precedent_n <= 10)),
    judge_consistency_samples INTEGER
        CHECK (judge_consistency_samples IS NULL OR (judge_consistency_samples >= 1 AND judge_consistency_samples <= 9)),
    judge_prompt_budget_tokens INTEGER
        CHECK (judge_prompt_budget_tokens IS NULL OR (judge_prompt_budget_tokens >= 512 AND judge_prompt_budget_tokens <= 131072)),
    judge_escalate_from INTEGER
);

INSERT INTO keeper_runtime_settings_new
    (id, enabled, judge_provider, judge_endpoint_url, judge_wire, judge_model,
     updated_by, created_at, updated_at, judge_timeout_ms, judge_profile,
     judge_evidence, judge_evidence_facts, judge_hard_gate, judge_precedent,
     judge_precedent_n, judge_consistency_samples, judge_prompt_budget_tokens)
SELECT id, enabled, judge_provider, judge_endpoint_url, judge_wire, judge_model,
       updated_by, created_at, updated_at, judge_timeout_ms, judge_profile,
       judge_evidence, judge_evidence_facts, judge_hard_gate, judge_precedent,
       judge_precedent_n, judge_consistency_samples, judge_prompt_budget_tokens
  FROM keeper_runtime_settings;

DROP TABLE keeper_runtime_settings;
ALTER TABLE keeper_runtime_settings_new RENAME TO keeper_runtime_settings;

PRAGMA foreign_keys = ON;
