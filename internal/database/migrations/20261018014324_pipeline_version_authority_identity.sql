-- Content equality must not deduplicate distinct executable authorities.
-- This migration prepares storage only; all existing writers still create
-- legacy-trusted identities. No Guest publication route is enabled here.
-- pipeline_versions has no incoming foreign keys: rebuilding this child table
-- is atomic inside the normal migration transaction with foreign_keys enabled.
CREATE TABLE pipeline_versions_authority (
    id TEXT PRIMARY KEY,
    pipeline_id TEXT NOT NULL REFERENCES pipelines(id) ON DELETE CASCADE,
    version INTEGER NOT NULL,
    definition_json TEXT NOT NULL,
    definition_hash TEXT NOT NULL,
    author_type TEXT NOT NULL CHECK(author_type IN ('agent','user','system','imported')),
    author_id TEXT NOT NULL,
    parent_version INTEGER,
    change_summary TEXT,
    created_at TEXT NOT NULL DEFAULT (datetime('now','subsec')),
    chat_origin_id TEXT REFERENCES execution_origins(id),
    authority_fingerprint TEXT NOT NULL DEFAULT 'legacy-trusted:v1'
        CHECK(typeof(authority_fingerprint) = 'text' AND
            (authority_fingerprint = 'legacy-trusted:v1' OR
                (length(authority_fingerprint) = 64 AND length(CAST(authority_fingerprint AS BLOB)) = 64
                    AND authority_fingerprint NOT GLOB '*[^0-9a-f]*'))),
    UNIQUE(pipeline_id, version),
    UNIQUE(pipeline_id, definition_hash, authority_fingerprint)
);
INSERT INTO pipeline_versions_authority (
    id,pipeline_id,version,definition_json,definition_hash,author_type,author_id,
    parent_version,change_summary,created_at,chat_origin_id,authority_fingerprint
)
SELECT id,pipeline_id,version,definition_json,definition_hash,author_type,author_id,
    parent_version,change_summary,created_at,chat_origin_id,'legacy-trusted:v1'
FROM pipeline_versions;
DROP TABLE pipeline_versions;
ALTER TABLE pipeline_versions_authority RENAME TO pipeline_versions;
CREATE INDEX idx_pipeline_versions_pipeline ON pipeline_versions(pipeline_id,version DESC);
CREATE INDEX idx_pipeline_versions_chat_origin ON pipeline_versions(chat_origin_id) WHERE chat_origin_id IS NOT NULL;
CREATE TRIGGER pipeline_versions_authority_immutable
BEFORE UPDATE OF authority_fingerprint ON pipeline_versions
WHEN NEW.authority_fingerprint IS NOT OLD.authority_fingerprint
BEGIN SELECT RAISE(ABORT,'pipeline version authority is immutable'); END;
