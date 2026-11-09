-- Producer run records of dashboard datasets: when a refresh of one collection
-- started, how it ended and why it failed. A producer (a script or job using
-- the REST API or CLI) starts a run, writes data batches, then finishes it.
-- Data batches apply immediately; a run does not stage or publish them.
-- start_generation/end_generation are the collection's data generation (sum of
-- document versions; documents are never deleted, so it only grows) when the
-- run started and finished. A success is bound to the generation its producer
-- reports, which must be current. Retention never removes a running run:
-- admission bounds active runs, and only terminal runs are pruned beyond the
-- store's configured default. An idempotency key of a terminal run expires
-- after the store's TTL (idempotency_key becomes NULL). Page/workspace lifetime
-- owns the rows. started_by/finished_by are authenticated host actors, never
-- caller input.
-- Debt: rows cascade from artifact_version_heads. A future migration that
-- rebuilds that table (as 20261028202000 did) must copy artifact_data_runs
-- around the rebuild like artifact_data_documents, or the run history is lost.
-- There is no shared guard list for heads dependents yet.
CREATE TABLE artifact_data_runs (
 workspace_id TEXT NOT NULL,
 page_id TEXT NOT NULL,
 collection TEXT NOT NULL CHECK(length(collection) BETWEEN 1 AND 128),
 id TEXT NOT NULL CHECK(length(id) BETWEEN 1 AND 64),
 idempotency_key TEXT CHECK(idempotency_key IS NULL OR length(idempotency_key) BETWEEN 1 AND 128),
 state TEXT NOT NULL CHECK(state IN ('running','succeeded','failed')),
 started_by TEXT NOT NULL,
 started_at TEXT NOT NULL,
 start_generation INTEGER NOT NULL DEFAULT 0 CHECK(start_generation >= 0),
 finished_at TEXT,
 finished_by TEXT,
 end_generation INTEGER CHECK(end_generation IS NULL OR end_generation >= start_generation),
 failure_reason TEXT CHECK(failure_reason IS NULL OR length(failure_reason) <= 500),
 CHECK((state='running') = (finished_at IS NULL)),
 CHECK((state='running') = (finished_by IS NULL)),
 CHECK((state='running') = (end_generation IS NULL)),
 CHECK(state!='running' OR idempotency_key IS NOT NULL),
 CHECK(failure_reason IS NULL OR state='failed'),
 PRIMARY KEY(workspace_id,page_id,id),
 UNIQUE(workspace_id,page_id,collection,idempotency_key),
 FOREIGN KEY(workspace_id,page_id) REFERENCES artifact_version_heads(workspace_id,page_id) ON DELETE CASCADE
);
CREATE INDEX artifact_data_runs_latest ON artifact_data_runs(workspace_id,page_id,collection,started_at);
