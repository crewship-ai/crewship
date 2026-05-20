package database

// migrationAutonomy (v98) adds the per-crew autonomy policy surface
// introduced in PRD §6 F2 (PR-B). Two enum columns gate every
// downstream HITL decision (memory writes, skill creation, persona
// suggestions, behavior monitor escalations, ephemeral spawn):
//
//   - autonomy_level: how much agent autonomy this crew has
//     strict    every action needs operator Approve
//     guided    read-only auto, writes need OK (default for new crews)
//     trusted   most actions auto, writes log to inbox
//     full      autonomous; journal-only logging
//
//   - behavior_mode: how F4.2 behavior monitor responds to anti-patterns
//     warn      DENY decisions treated as WARN (non-blocking inbox);
//     the agent's action proceeds. Default — Hermes-aligned.
//     block     DENY decisions throw BlockedError in the hook handler;
//     next tool call interrupted. Opt-in.
//
// Both are ALTER TABLE ADD COLUMN with NOT NULL + DEFAULT + column-
// level CHECK so existing crews land on guided/warn and bad inserts
// fail loudly. SQLite recreate dance is unnecessary here because
// these are net-new columns; CHECK on add-column is supported.
//
// autonomy_set_by_user_id / autonomy_set_at / autonomy_reason are the
// audit triple — who flipped the policy, when, and why. NULLable
// because the seed default doesn't carry a user (no operator flipped
// it; the migration installed it). Subsequent writes via the API
// must populate all three together; the API layer enforces the
// invariant rather than a DB-level NOT NULL (which would break the
// seed insert path).
//
// validation rule landing alongside this migration: when
// behavior_mode='block', autonomy_level cannot be 'full' — combining
// the two creates a contradiction (opt-in trust × opt-in restriction).
// Enforced at the API PATCH handler in PR-B.4 with an inline error.
const migrationAutonomy = `
ALTER TABLE crews ADD COLUMN autonomy_level TEXT NOT NULL DEFAULT 'guided'
    CHECK(autonomy_level IN ('strict','guided','trusted','full'));

ALTER TABLE crews ADD COLUMN behavior_mode TEXT NOT NULL DEFAULT 'warn'
    CHECK(behavior_mode IN ('warn','block'));

ALTER TABLE crews ADD COLUMN autonomy_set_by_user_id TEXT;
ALTER TABLE crews ADD COLUMN autonomy_set_at TEXT;
ALTER TABLE crews ADD COLUMN autonomy_reason TEXT;
`
