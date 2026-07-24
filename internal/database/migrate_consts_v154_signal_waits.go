package database

// migrationSignalWaits (v154) introduces pipeline_signal_waits — durable
// state for `wait: event` steps (#1409).
//
// Before this migration a wait:event step blocked the run's goroutine
// on an in-memory-only SignalRegistry channel: the payload delivered by
// POST .../signal was lost the moment the process restarted (or if it
// arrived a hair before the step registered), and the run itself never
// even parked (status stayed 'running', current_step_id wasn't
// advertised as waiting) — the exact hazard `wait: approval` closed with
// pipeline_waitpoints (v79).
//
// pipeline_signal_waits mirrors that pattern for events: the step ARMS
// a row (status=pending) before parking (MarkWaiting, same as
// approval), the signal endpoint DELIVERs into that row durably before
// attempting any in-memory wake, and a resume (in-process after
// delivery, or the generic boot-time resume scan after a restart) reads
// the delivered payload straight out of the DB rather than needing a
// live goroutine to have caught it.
//
//   - status transitions pending -> delivered -> consumed. UNIQUE
//     (run_id, step_id) — one wait per (run, step); a re-arm on resume
//     is a no-op via INSERT ... ON CONFLICT DO NOTHING.
//   - payload is nullable until delivered.
//   - the (run_id, event_type, status) index is delivery's lookup path:
//     "find the oldest pending wait for this run+event and deliver into
//     it" — the same shape the in-memory registry used, now durable.
const migrationSignalWaits = `
CREATE TABLE IF NOT EXISTS pipeline_signal_waits (
    id           TEXT PRIMARY KEY,
    workspace_id TEXT NOT NULL,
    run_id       TEXT NOT NULL,
    step_id      TEXT NOT NULL,
    event_type   TEXT NOT NULL,
    status       TEXT NOT NULL DEFAULT 'pending',
    payload      TEXT,
    created_at   TEXT NOT NULL,
    delivered_at TEXT,
    consumed_at  TEXT,
    UNIQUE (run_id, step_id)
);
CREATE INDEX IF NOT EXISTS idx_pipeline_signal_waits_delivery
    ON pipeline_signal_waits (run_id, event_type, status);
`
