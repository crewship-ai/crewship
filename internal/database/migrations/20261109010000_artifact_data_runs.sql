-- Producer run records of dashboard datasets: when a refresh of one collection
-- started, how it ended and why it failed. A producer (a script or job using
-- the REST API or CLI) starts a run, writes data batches, then finishes it. The
-- store keeps the newest 20 runs per collection plus the newest success, so
-- the table never grows without bound. Page/workspace lifetime owns them.
-- started_by is the authenticated host actor, never caller input.
-- Debt: rows cascade from artifact_version_heads. A future migration that
-- rebuilds that table (as 20261028202000 did) must copy artifact_data_runs
-- around the rebuild like artifact_data_documents, or the run history is lost.
-- There is no shared guard list for heads dependents yet.
CREATE TABLE artifact_data_runs (
 workspace_id TEXT NOT NULL,
 page_id TEXT NOT NULL,
 collection TEXT NOT NULL CHECK(length(collection) BETWEEN 1 AND 128),
 id TEXT NOT NULL CHECK(length(id) BETWEEN 1 AND 64),
 idempotency_key TEXT NOT NULL CHECK(length(idempotency_key) BETWEEN 1 AND 128),
 state TEXT NOT NULL CHECK(state IN ('running','succeeded','failed')),
 started_by TEXT NOT NULL,
 started_at TEXT NOT NULL,
 finished_at TEXT,
 failure_reason TEXT CHECK(failure_reason IS NULL OR length(failure_reason) <= 500),
 CHECK((state='running') = (finished_at IS NULL)),
 CHECK(failure_reason IS NULL OR state='failed'),
 PRIMARY KEY(workspace_id,page_id,id),
 UNIQUE(workspace_id,page_id,collection,idempotency_key),
 FOREIGN KEY(workspace_id,page_id) REFERENCES artifact_version_heads(workspace_id,page_id) ON DELETE CASCADE
);
CREATE INDEX artifact_data_runs_latest ON artifact_data_runs(workspace_id,page_id,collection,started_at);
