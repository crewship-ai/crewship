-- Receipt storage only; no capture or runtime permission is enabled here.
-- Capture requires a separately registered future Go cutover.
CREATE TABLE pipeline_legacy_head_receipts (
    id TEXT PRIMARY KEY,
    pipeline_id TEXT NOT NULL UNIQUE REFERENCES pipelines(id) ON DELETE CASCADE,
    head_version INTEGER NOT NULL CHECK(head_version=0),
    definition_json TEXT NOT NULL,
    definition_hash TEXT NOT NULL,
    author_crew_id TEXT REFERENCES crews(id) ON DELETE SET NULL,
    author_agent_id TEXT REFERENCES agents(id) ON DELETE SET NULL,
    chat_origin_id TEXT REFERENCES execution_origins(id),
    execution_tier_json TEXT,
    created_at TEXT NOT NULL DEFAULT (datetime('now','subsec'))
);

CREATE INDEX idx_pipeline_legacy_head_receipts_author_crew ON pipeline_legacy_head_receipts(author_crew_id) WHERE author_crew_id IS NOT NULL;
CREATE INDEX idx_pipeline_legacy_head_receipts_author_agent ON pipeline_legacy_head_receipts(author_agent_id) WHERE author_agent_id IS NOT NULL;
CREATE INDEX idx_pipeline_legacy_head_receipts_chat_origin ON pipeline_legacy_head_receipts(chat_origin_id) WHERE chat_origin_id IS NOT NULL;
