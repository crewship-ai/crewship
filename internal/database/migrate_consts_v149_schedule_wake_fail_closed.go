package database

// migrationScheduleWakeFailClosed (v149) adds an opt-in fail-closed
// policy flag to the schedule wake gate (issue #1372).
//
// Background: the wake gate (v115) fails OPEN — a probe that errors,
// returns nil, or finishes non-COMPLETED still lets the gated main
// routine fire. For an unattended schedule that is the wrong default: a
// broken or tampered probe cannot suppress the autonomous run.
//
// wake_fail_closed = 1 flips the failure branch for that schedule so a
// non-affirmative probe HOLDS the run instead of proceeding. Default 0
// preserves the historical fail-open behaviour for every existing
// schedule, so this migration is purely additive.
const migrationScheduleWakeFailClosed = `
ALTER TABLE pipeline_schedules ADD COLUMN wake_fail_closed INTEGER NOT NULL DEFAULT 0;
`
