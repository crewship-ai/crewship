package database

// migrationChatUnread (v130) adds the per-session unread / last-activity
// plumbing behind the chat "never miss a reply" surface:
//
//   - chats.last_activity_at: bumped every time a message is appended
//     (the internal message-count endpoint owns the write) so the
//     Sessions sidebar can order by "newest message", not creation
//     time. Nullable because ALTER TABLE can't add a non-constant
//     default; readers COALESCE to started_at. Stored in the
//     millisecond-ISO form (strftime %fZ) so it lexically compares
//     with conversation_messages.ts.
//
//   - chat_read_cursors: one row per (user, chat) recording the last
//     moment that user marked the chat read. unread_count is then
//     "messages not authored by me with ts past my cursor". PK on
//     (user_id, chat_id) makes mark-read an idempotent upsert.
//
// Backfills:
//
//   - last_activity_at from started_at/created_at, normalised through
//     strftime so legacy space-separated datetime('now') rows and ISO
//     rows land in one comparable format.
//
//   - a read cursor "now" for every existing chat's creator and
//     participants, so the feature ships quiet: history the user has
//     plausibly seen doesn't light every session up as unread on
//     upgrade. Only activity AFTER this migration produces badges.
const migrationChatUnread = `
ALTER TABLE chats ADD COLUMN last_activity_at TEXT;

UPDATE chats SET last_activity_at = COALESCE(
    strftime('%Y-%m-%dT%H:%M:%fZ', started_at),
    strftime('%Y-%m-%dT%H:%M:%fZ', created_at),
    strftime('%Y-%m-%dT%H:%M:%fZ', 'now')
) WHERE last_activity_at IS NULL;

CREATE INDEX IF NOT EXISTS idx_chats_agent_activity
    ON chats(agent_id, last_activity_at DESC);

CREATE TABLE IF NOT EXISTS chat_read_cursors (
    user_id      TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    chat_id      TEXT NOT NULL REFERENCES chats(id) ON DELETE CASCADE,
    last_read_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    PRIMARY KEY (user_id, chat_id)
);
CREATE INDEX IF NOT EXISTS idx_chat_read_cursors_chat
    ON chat_read_cursors(chat_id);

INSERT OR IGNORE INTO chat_read_cursors (user_id, chat_id, last_read_at)
SELECT created_by, id, strftime('%Y-%m-%dT%H:%M:%fZ', 'now')
FROM chats
WHERE created_by IS NOT NULL;

INSERT OR IGNORE INTO chat_read_cursors (user_id, chat_id, last_read_at)
SELECT user_id, chat_id, strftime('%Y-%m-%dT%H:%M:%fZ', 'now')
FROM chat_participants;
`
