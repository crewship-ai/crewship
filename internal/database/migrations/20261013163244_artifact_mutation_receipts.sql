-- Retry identities survive source retention. No revision FK: a receipt may
-- identify a revision whose unpinned source has expired.
CREATE TABLE page_artifact_mutation_receipts (
 page_id TEXT NOT NULL REFERENCES pages(id) ON DELETE CASCADE,
 mutation_id TEXT NOT NULL,
 revision INTEGER NOT NULL,
 request_digest TEXT NOT NULL,
 author_user_id TEXT,
 author_agent_id TEXT,
 author_run_id TEXT,
 created_at TEXT NOT NULL,
 restored_from INTEGER,
 PRIMARY KEY(page_id,mutation_id)
);
INSERT INTO page_artifact_mutation_receipts SELECT page_id,mutation_id,revision,request_digest,author_user_id,author_agent_id,author_run_id,created_at,restored_from FROM page_artifact_revisions;
CREATE INDEX idx_artifact_mutation_receipt_rate ON page_artifact_mutation_receipts(page_id,created_at);
