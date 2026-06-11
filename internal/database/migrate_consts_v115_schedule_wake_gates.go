package database

// migrationScheduleWakeGates (v115) adds the wake-gate columns to
// pipeline_schedules (PRD WAKE-GATES-AGENTLESS-ROUTINES).
//
// A wake gate lets a schedule run a cheap AGENTLESS probe routine on
// each cron tick and fire the main routine only when the probe's
// final output is truthy — the cost ladder between "plain cron that
// burns an LLM run every tick" and "never check at all".
//
// Schema notes:
//   - wake_pipeline_id NULL = no gate (default; existing schedules
//     keep today's fire-every-tick behaviour). References a pipeline
//     in the same workspace; the API save handler enforces that the
//     target parses as `agentless: true` and isn't the schedule's
//     own routine. No FK clause — ALTER TABLE ADD COLUMN in SQLite
//     can't add one, and the schedule must survive probe deletion
//     anyway (scheduler fails OPEN on a missing probe).
//   - wake_inputs_json carries static inputs for the probe, mirroring
//     inputs_json for the main routine.
//   - wake_check_count / wake_fire_count power the "checked 96×,
//     woke 3×" telemetry without scanning pipeline_runs.
//   - last_wake_at / last_wake_status (WOKE | SKIPPED | ERROR) are
//     wake-only health fields; last_run_* stays strictly about main
//     runs so a long streak of skips doesn't masquerade as activity.
const migrationScheduleWakeGates = `
ALTER TABLE pipeline_schedules ADD COLUMN wake_pipeline_id TEXT;
ALTER TABLE pipeline_schedules ADD COLUMN wake_inputs_json TEXT NOT NULL DEFAULT '{}';
ALTER TABLE pipeline_schedules ADD COLUMN wake_check_count INTEGER NOT NULL DEFAULT 0;
ALTER TABLE pipeline_schedules ADD COLUMN wake_fire_count INTEGER NOT NULL DEFAULT 0;
ALTER TABLE pipeline_schedules ADD COLUMN last_wake_at TEXT;
ALTER TABLE pipeline_schedules ADD COLUMN last_wake_status TEXT;
`
