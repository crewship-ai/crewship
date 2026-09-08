-- Opt-in journal projections. Each family starts at the current journal head;
-- enabling a channel never dumps historical workspace activity into it.
CREATE TABLE workspace_conversation_activity (
 conversation_id TEXT PRIMARY KEY REFERENCES workspace_conversations(id) ON DELETE CASCADE,
 issues INTEGER NOT NULL DEFAULT 0 CHECK(issues IN (0,1)),
 routines INTEGER NOT NULL DEFAULT 0 CHECK(routines IN (0,1)),
 issues_cursor INTEGER NOT NULL DEFAULT 0,
 routines_cursor INTEGER NOT NULL DEFAULT 0
);

-- Trusted origin survives author deletion; client_id is caller-controlled and
-- cannot establish that a message was authored by Crewship.
ALTER TABLE workspace_conversation_messages ADD COLUMN source_kind TEXT NOT NULL DEFAULT '' CHECK(source_kind IN ('','activity'));
