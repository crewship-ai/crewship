-- The cause belongs to the historical skipped occurrence, not the routine's
-- current lifecycle. Empty is unknown for schedules recorded before this field.
ALTER TABLE pipeline_schedules ADD COLUMN last_skip_reason TEXT NOT NULL DEFAULT ''
    CHECK (last_skip_reason IN ('', 'capacity', 'catchup', 'inactive', 'busy'));
