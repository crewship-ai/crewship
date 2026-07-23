package database

// migrationScheduleWakeFailClosed (v149) adds an opt-in fail-closed policy to
// the wake gate on pipeline_schedules (#1372).
//
// A wake probe gates an unattended routine run: the main routine fires only
// when the probe's final output is truthy. But a probe that ERRORS (didn't
// load, crashed, timed out) has always failed OPEN — the gated main routine
// fires anyway (runWakeCheck returned proceed=true on error). That is the
// right default for a monitoring probe (occasional token spend beats going
// silently blind), but the wrong default when the probe is a genuine safety
// gate: a broken or tampered probe must be able to SUPPRESS the autonomous
// run, not wave it through.
//
// wake_fail_closed is per-schedule and defaults to 0 (fail OPEN — today's
// behaviour, unchanged for every existing row). When 1, a probe error HOLDS
// the run instead of proceeding, and the tick records the distinct HELD wake
// status so a stuck probe is visible in telemetry rather than silent.
const migrationScheduleWakeFailClosed = `
ALTER TABLE pipeline_schedules ADD COLUMN wake_fail_closed INTEGER NOT NULL DEFAULT 0;
`
