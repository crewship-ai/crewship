package database

// migrationIdempotencyPipelineScope (v153) namespaces the
// pipeline_run_idempotency primary key by pipeline_id (issue #1415).
//
// Before this migration the PK was (workspace_id, idempotency_key)
// only — pipeline_id was carried as plain provenance. Idempotency-Key
// is a human-readable, client-supplied string (e.g. "order-123"), so
// any `create`-role member of a workspace could:
//
//   - Pre-poison: submit a trivial run with a predictable key; a
//     later legitimate run in a DIFFERENT pipeline using the same key
//     dedupes onto the attacker's run and never executes — a silent
//     cross-pipeline DoS.
//   - Disclose: the dedupe response returns the original run's
//     run_id, leaking it to a workspace member who has no business
//     knowing about that other pipeline's run.
//
// Widening the PK to (workspace_id, pipeline_id, idempotency_key)
// lets two different pipelines reuse the same key value without
// colliding, while same-pipeline dedup (the actual point of the
// feature — e.g. webhook redelivery) is unaffected.
//
// SQLite has no ALTER TABLE ... DROP/ADD CONSTRAINT for primary keys,
// so this rebuilds the table: create the new shape, copy every row
// across (the OLD PK already guaranteed at most one row per
// (workspace_id, idempotency_key), so the copy can't produce a
// duplicate against the wider new PK), drop the old table, and rename.
// Plain SQL (not a Go fn) — no row-level Go logic is needed, unlike
// v152's hash-chain backfill.
//
// This table only ever holds live rows for DefaultIdempotencyTTL
// (24h) plus whatever a sweep-failure straggler adds on top, so a
// full-table copy during migration is cheap even on an
// established instance.
const migrationIdempotencyPipelineScope = `
CREATE TABLE pipeline_run_idempotency_v153 (
    workspace_id     TEXT NOT NULL,
    pipeline_id      TEXT NOT NULL,
    idempotency_key  TEXT NOT NULL,
    run_id           TEXT NOT NULL,
    created_at       TEXT NOT NULL DEFAULT (datetime('now','subsec')),
    expires_at       TEXT NOT NULL,
    PRIMARY KEY (workspace_id, pipeline_id, idempotency_key)
);
INSERT INTO pipeline_run_idempotency_v153
    (workspace_id, pipeline_id, idempotency_key, run_id, created_at, expires_at)
SELECT workspace_id, pipeline_id, idempotency_key, run_id, created_at, expires_at
FROM pipeline_run_idempotency;
DROP TABLE pipeline_run_idempotency;
ALTER TABLE pipeline_run_idempotency_v153 RENAME TO pipeline_run_idempotency;
CREATE INDEX IF NOT EXISTS idx_pipeline_run_idempotency_expires
    ON pipeline_run_idempotency (expires_at);
`
