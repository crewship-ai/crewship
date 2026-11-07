-- Optional fire-time spread for a schedule, so many schedules on the same cron
-- expression do not all fire in the same second. spread_seconds is the window
-- the operator chose (0 = off, today's behaviour). spread_offset_seconds is the
-- stable offset within [0, spread_seconds) derived from the schedule id when
-- the row is written; it only delays WHEN an occurrence is picked up. The
-- occurrence itself (next_run_at, the idempotency bucket, catch-up) stays the
-- nominal cron time.
ALTER TABLE pipeline_schedules ADD COLUMN spread_seconds INTEGER NOT NULL DEFAULT 0;
ALTER TABLE pipeline_schedules ADD COLUMN spread_offset_seconds INTEGER NOT NULL DEFAULT 0;
