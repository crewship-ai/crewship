package database

// migrationPendingRuns (v122) adds deferred run dispatch — the storage
// behind delay / ttl / debounce / priority on a run trigger
// (trigger.dev parity). A trigger that carries a delay or a debounce key
// is parked here instead of executing immediately; an in-process
// dispatcher fires due rows (fire_at <= now), highest priority first,
// and expires rows past their ttl.
//
//   - fire_at:        when to dispatch (RFC3339Nano). delay_seconds → now+delay.
//   - expires_at:     ttl — if still pending past this, mark expired, never fire.
//   - debounce_key:   coalesces a burst: a repeat trigger with the same
//     (pipeline_id, debounce_key) extends fire_at + replaces inputs
//     instead of creating a new row.
//   - debounce_max_at: ceiling on debounce extension (maxDelay) so a
//     continuously-retriggered key still fires eventually.
//   - priority:       dispatch order among due rows (higher first).
//
// All additive. Partial unique index makes the debounce upsert a cheap
// lookup. The plain (status, fire_at) index serves the due-sweep.
const migrationPendingRuns = `
CREATE TABLE IF NOT EXISTS pending_runs (
    id              TEXT PRIMARY KEY,
    workspace_id    TEXT NOT NULL,
    pipeline_id     TEXT NOT NULL REFERENCES pipelines(id) ON DELETE CASCADE,
    pipeline_slug   TEXT NOT NULL,
    inputs_json     TEXT NOT NULL DEFAULT '{}',
    tags_json       TEXT NOT NULL DEFAULT '[]',
    metadata_json   TEXT NOT NULL DEFAULT '{}',
    tier_override   TEXT,
    priority        INTEGER NOT NULL DEFAULT 0,
    debounce_key    TEXT,
    fire_at         TEXT NOT NULL,
    expires_at      TEXT,
    debounce_max_at TEXT,
    status          TEXT NOT NULL DEFAULT 'pending',   -- pending | fired | expired | cancelled
    fired_run_id    TEXT,
    created_at      TEXT NOT NULL DEFAULT (datetime('now','subsec')),
    updated_at      TEXT NOT NULL DEFAULT (datetime('now','subsec'))
);
CREATE INDEX IF NOT EXISTS idx_pending_runs_due ON pending_runs (status, fire_at);
CREATE UNIQUE INDEX IF NOT EXISTS idx_pending_runs_debounce
    ON pending_runs (pipeline_id, debounce_key)
    WHERE status = 'pending' AND debounce_key IS NOT NULL;
`
