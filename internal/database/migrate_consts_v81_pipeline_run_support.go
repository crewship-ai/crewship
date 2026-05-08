package database

// migrationAddPipelineRunSupport (v81) introduces the production-
// readiness pieces that turn pipelines from "demo works on happy
// path" into "safe to expose to webhook deliveries":
//
//   - pipeline_run_idempotency: dedupe re-runs when the same
//     idempotency key arrives twice (e.g. webhook redelivered
//     because we returned 502 before our response landed).
//
// The cancel + concurrency state is intentionally NOT in the DB:
// both live in the in-memory run registry because they couple to a
// Go context that doesn't survive a process restart anyway. A
// future multi-replica deployment will need a leader-elected
// shared registry; for single-instance the in-memory map is correct
// and fast.
//
// Schema notes:
//   - Composite primary key on (workspace_id, idempotency_key) so
//     two workspaces can independently use the same key value.
//   - expires_at indexed for the cleanup sweep (rows older than the
//     TTL are deleted lazily before each new INSERT OR IGNORE).
//   - run_id captured so a duplicate request can return the run id
//     of the original invocation.
const migrationAddPipelineRunSupport = `
CREATE TABLE IF NOT EXISTS pipeline_run_idempotency (
    workspace_id     TEXT NOT NULL,
    idempotency_key  TEXT NOT NULL,
    run_id           TEXT NOT NULL,
    pipeline_id      TEXT NOT NULL,
    created_at       TEXT NOT NULL DEFAULT (datetime('now','subsec')),
    expires_at       TEXT NOT NULL,
    PRIMARY KEY (workspace_id, idempotency_key)
);
CREATE INDEX IF NOT EXISTS idx_pipeline_run_idempotency_expires
    ON pipeline_run_idempotency (expires_at);
`
