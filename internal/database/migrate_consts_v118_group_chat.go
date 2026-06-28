package database

// migrationGroupChat (v118) lays the data-model groundwork for multi-user
// "group" chats — several humans plus an agent in one conversation.
//
//   - chat_participants: who is in a chat and in what role. A chat with only
//     its creator behaves exactly like today's private 1:1 chat; adding rows
//     makes it a shared thread. PK (chat_id, user_id) so a join is idempotent.
//   - chats.visibility: 'private' (default — current behaviour, only the
//     creator) or 'group' (multiple participants; the agent runs only when
//     @mentioned). Additive, defaulted, so every existing chat stays private.
//   - conversation_messages.author_user_id: which human authored a message, so
//     a shared transcript can attribute each turn. NULL for agent/system turns
//     and for legacy rows.
//
// All changes are additive and backward-compatible: existing chats keep working
// as private 1:1 conversations with no participant rows required.
const migrationGroupChat = `
CREATE TABLE IF NOT EXISTS chat_participants (
    chat_id    TEXT NOT NULL REFERENCES chats(id) ON DELETE CASCADE,
    user_id    TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    role       TEXT NOT NULL DEFAULT 'member',  -- owner | member
    joined_at  TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    PRIMARY KEY (chat_id, user_id)
);
CREATE INDEX IF NOT EXISTS idx_chat_participants_user ON chat_participants(user_id);

ALTER TABLE chats ADD COLUMN visibility TEXT NOT NULL DEFAULT 'private';

ALTER TABLE conversation_messages ADD COLUMN author_user_id TEXT;
`
