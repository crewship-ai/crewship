package database

// migrationAddMessageFeedback (v95) introduces a dedicated table for
// per-message user feedback signals: thumbs up/down, "edit", "regenerate",
// "abandon", and free-form "inaccurate"/"unsafe" markers.
//
// Why a new table instead of reusing message_reactions:
//   message_reactions is an open-ended emoji store (UNIQUE per
//   chat+message+emoji+user). Modelling feedback as 👍/👎 emojis would
//   force every consumer that cares about ADLC-phase-7 signal — eval
//   regression triggers, online grader datasets, rolling baseline alerts
//   — to LIKE-match emoji codepoints. A typed `signal` enum with the
//   six known kinds gives those consumers a stable query target and lets
//   the schema's CHECK constraint reject unrecognised values at write
//   time instead of months later when a dashboard query goes dark.
//
// Trace correlation:
//   trace_id is nullable on purpose. Older messages from before the OTel
//   wiring landed in v95's same release won't have a trace, and a chat
//   created during a collector outage also won't. A partial index keeps
//   the lookup-by-trace path cheap without paying for null rows.
//
// signal vocabulary:
//   helpful      — explicit thumb-up
//   not_helpful  — explicit thumb-down (most common feedback kind)
//   inaccurate   — thumb-down + "the answer was wrong" reason chip
//   unsafe       — thumb-down + "this was harmful / leaked secrets" chip
//   edit         — user replaced the assistant text with their own
//                  (the highest-quality training signal — it's not
//                  "this was bad" but "this is what I wanted")
//   regenerate   — user asked for a different answer without editing
//                  (weak negative signal)
//
// reason TEXT carries the free-form explanation when the user typed one;
// "edit" rows put the user's replacement text here so the eval dataset
// builder can recover the (prompt, original, preferred) triple.
//
// UNIQUE(message_id, user_id, signal) lets one user file multiple
// distinct signals against the same message (e.g. "not_helpful" plus an
// "edit") but blocks duplicate identical filings. The chat_id and
// workspace_id columns are denormalized so scope-aware deletion via the
// CASCADE chain works without joining back through chats.
const migrationAddMessageFeedback = `
CREATE TABLE IF NOT EXISTS message_feedback (
    id TEXT PRIMARY KEY,
    workspace_id TEXT NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    chat_id TEXT REFERENCES chats(id) ON DELETE CASCADE,
    message_id TEXT NOT NULL,
    trace_id TEXT,
    signal TEXT NOT NULL CHECK (signal IN ('helpful','not_helpful','inaccurate','unsafe','edit','regenerate')),
    reason TEXT,
    user_id TEXT REFERENCES users(id) ON DELETE SET NULL,
    created_at TEXT NOT NULL DEFAULT (datetime('now')),
    UNIQUE(message_id, user_id, signal)
);
CREATE INDEX IF NOT EXISTS idx_feedback_trace ON message_feedback(trace_id) WHERE trace_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_feedback_ws_created ON message_feedback(workspace_id, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_feedback_message ON message_feedback(message_id);
`
