-- Where each operator-model fact came from (#1693, split out of #1669).
--
-- `crewship privacy user-model list` shows a person WHAT is stored about them
-- and could not say WHERE any of it came from. It is not a hole in the
-- extraction: usermodel.Verify admits a fact only when its evidence span is
-- found byte-for-byte in a turn the person authored, so every stored fact
-- provably has a quote. The quote was thrown away after the check, because the
-- model file is capped at 1.5 KB and read into every agent prompt — carrying
-- provenance inline would halve how much a person can be known by.
--
-- So provenance lives BESIDE the file, in this table, never inside it.
--
-- The shape is Personis's (Kay & Kummerfeld, 2002): evidence rows, resolved at
-- read time; correction is APPEND, never mutate. A sync that changes a key, or
-- finds it said again in a later message, writes a new row and the readers
-- take the newest one per (workspace, slug, key) — which is why there is no
-- UNIQUE on that triple and why nothing here is ever UPDATEd. (The writer
-- skips an entry identical to the newest row, so the daily re-read of the same
-- fortnight does not stack fourteen copies of one sentence.) The row records
-- the value the evidence supported at the time, so a later row that changes
-- the value is a visible correction rather than a silent rewrite of history.
--
-- user_slug is the file's own name (sha256(user_id||workspace_id)[:16], the
-- key user_models is UNIQUE on) so a read from the file side needs no join.
-- user_id is here too, with the same FK user_models carries, so the Art. 17
-- cascade's schema sweep (admin_gdpr_pages_identity_test.go, which matches on
-- user-id columns) can SEE this table — a table keyed on the slug alone is
-- invisible to it, and invisible to the sweep is how #1669 found user_models
-- missing from the cascade in the first place.
--
-- message_id is conversation_messages.id, the turn Verify matched the quote in.
-- Deliberately NOT a foreign key: conversation_messages is the searchable
-- mirror of the JSONL session log, instance-local and never in a workspace
-- backup bundle, so a restored bundle would have every FK dangling; and a
-- quote does not stop being what the person said because the mirror row it
-- was found in has since been pruned. It is a pointer, kept so a later
-- "show me that conversation" can follow it.
--
-- source_type is usermodel.SourceType's vocabulary. Only 'stated' is written
-- by any shipped profile; the other two are here so admitting one later is a
-- profile change and not a migration.
CREATE TABLE user_model_provenance (
    id            TEXT PRIMARY KEY,
    workspace_id  TEXT NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    user_id       TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    user_slug     TEXT NOT NULL,
    key           TEXT NOT NULL,
    value         TEXT NOT NULL,
    quote         TEXT NOT NULL,
    message_id    TEXT NOT NULL DEFAULT '',
    source_type   TEXT NOT NULL CHECK (source_type IN ('stated', 'observed', 'inferred')),
    recorded_at   TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now'))
);

-- The read: newest row per fact. Every reader orders by recorded_at DESC
-- within one (workspace, slug, key), and every purge is by (workspace, slug)
-- or (workspace, user).
CREATE INDEX idx_user_model_provenance_fact
    ON user_model_provenance (workspace_id, user_slug, key, recorded_at DESC);
CREATE INDEX idx_user_model_provenance_user
    ON user_model_provenance (workspace_id, user_id);
