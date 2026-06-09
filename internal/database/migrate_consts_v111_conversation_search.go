package database

// migrationConversationSearch (v111) adds a queryable mirror of chat
// conversation messages plus an FTS5 shadow so an agent can search its
// own past sessions for keyword recall.
//
// Why a table at all when conversations already persist as JSONL
// (internal/conversation/store.go)? The JSONL files are one-per-session
// append logs — fine for replaying a single chat, useless for the
// cross-session "what did I conclude about X three weeks ago" question
// because there is no index to scan and no per-agent rollup. This table
// is the dual-write target: every Append writes the JSONL line (kept as
// the durable source of truth) and a row here for search. First slice is
// BM25-only (no semantic re-rank) and search-from-now-on (no backfill of
// pre-v111 JSONL), so the table starts empty and fills as new turns land.
//
// conversation_messages — one row per persisted message. agent_id is the
// isolation boundary: Search ALWAYS filters by agent_id so an agent can
// never read another agent's chat history. role/content/tool_summary
// mirror the conversation.Message fields that carry searchable text.
//
// conversation_messages_fts — external-content FTS5 shadow over
// (content, tool_summary). content='conversation_messages' means the FTS
// table stores no duplicate copy of the text; the triggers keep the two
// in sync. tokenize='porter ascii' matches journal_entries_fts so stem
// + ascii-fold behaviour is identical across the two search surfaces.
//
// The DELETE/UPDATE triggers use the contentless INSERT('delete', ...)
// form COPIED VERBATIM from journal_entries_fts (migration v55). An
// earlier journal revision used plain DELETE on the shadow table and
// corrupted the index (SQLite error 267 "database disk image is
// malformed"); the comment there warns that this exact form is
// load-bearing. We reuse it unchanged so this surface inherits the same
// guarantee — do not "simplify" the triggers.
const migrationConversationSearch = `
CREATE TABLE IF NOT EXISTS conversation_messages (
    id TEXT PRIMARY KEY,
    session_id TEXT NOT NULL,
    agent_id TEXT NOT NULL,
    role TEXT NOT NULL,
    content TEXT NOT NULL DEFAULT '',
    tool_summary TEXT NOT NULL DEFAULT '',
    ts TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now'))
);
CREATE INDEX IF NOT EXISTS idx_conversation_messages_agent_ts
    ON conversation_messages(agent_id, ts DESC);
CREATE INDEX IF NOT EXISTS idx_conversation_messages_session
    ON conversation_messages(session_id, ts DESC);

CREATE VIRTUAL TABLE IF NOT EXISTS conversation_messages_fts USING fts5(
    content, tool_summary,
    content='conversation_messages',
    content_rowid='rowid',
    tokenize='porter ascii'
);
CREATE TRIGGER IF NOT EXISTS conversation_messages_ai AFTER INSERT ON conversation_messages BEGIN
    INSERT INTO conversation_messages_fts(rowid, content, tool_summary) VALUES (new.rowid, new.content, new.tool_summary);
END;
-- For external-content FTS5 tables, DELETE/UPDATE triggers use the
-- INSERT(fts, 'delete'/'insert', ...) contentless form. An earlier
-- revision used plain DELETE/INSERT which mutated FTS5's shadow tables
-- directly and corrupted the index (SQLite error 267 "database disk
-- image is malformed"). Keep this form exactly as-is — changes here
-- require a full FTS rebuild.
CREATE TRIGGER IF NOT EXISTS conversation_messages_ad AFTER DELETE ON conversation_messages BEGIN
    INSERT INTO conversation_messages_fts(conversation_messages_fts, rowid, content, tool_summary) VALUES('delete', old.rowid, old.content, old.tool_summary);
END;
CREATE TRIGGER IF NOT EXISTS conversation_messages_au AFTER UPDATE ON conversation_messages BEGIN
    INSERT INTO conversation_messages_fts(conversation_messages_fts, rowid, content, tool_summary) VALUES('delete', old.rowid, old.content, old.tool_summary);
    INSERT INTO conversation_messages_fts(rowid, content, tool_summary) VALUES (new.rowid, new.content, new.tool_summary);
END;
`
