package database

// migrationSelfLearning (v106) adds the per-agent self-improving
// switch introduced by PR-G F4.1 UX. The flag governs whether the
// agent may have skill / persona / memory proposals auto-promoted to
// active state without an explicit operator approve in the inbox.
//
// Semantics by autonomy + self_learning combination:
//
//	self_learning=0 (default)
//	  The agent can still surface proposals through the keeper
//	  evaluators (F4.1 skill_review, F4.4 negative_learning), but
//	  each proposal lands as a BLOCKING inbox item the operator must
//	  approve. Governance-first posture; safe default for any new
//	  agent.
//
//	self_learning=1
//	  Proposals run through the same evaluators, but ALLOW decisions
//	  auto-apply: a recommended skill flips lifecycle_state=active,
//	  a negative lesson lands in lessons.md without prompting. DENY +
//	  ESCALATE still gate via inbox. Per-action gating still respects
//	  the crew's autonomy_level — a strict crew cannot self-learn
//	  because policy.DecideAction returns InboxApprove for every
//	  proposal kind regardless of this flag.
//
// Why a per-agent flag rather than a per-crew policy: the autonomy_
// level dial (PR-B F2) controls the crew-wide approval cadence for
// operator-initiated work. Self-learning is an agent-level posture —
// some agents in a crew may be trusted to evolve on their own
// (long-running maintainer bot), others should stay strict (newly
// onboarded contractor agent). Bundling into autonomy_level would
// force the operator to flip the entire crew to grant autonomy to
// one agent.
//
// Audit triple mirrors v101 autonomy: who flipped, when, why. The
// API layer enforces NOT NULL on writes; the migration leaves them
// NULL for legacy rows so the DEFAULT-installed zero doesn't carry
// a forged user-id.
//
// SQLite recreate dance is unnecessary — net-new column with default;
// CHECK on add-column supported.
const migrationSelfLearning = `
ALTER TABLE agents ADD COLUMN self_learning_enabled INTEGER NOT NULL DEFAULT 0
    CHECK(self_learning_enabled IN (0, 1));

ALTER TABLE agents ADD COLUMN self_learning_set_by_user_id TEXT;
ALTER TABLE agents ADD COLUMN self_learning_set_at TEXT;
ALTER TABLE agents ADD COLUMN self_learning_reason TEXT;
`
