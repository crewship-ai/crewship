package database

// migrationScheduleCircuitBreaker (v153) adds a per-schedule circuit
// breaker to pipeline_schedules (#1405).
//
// Before this migration only an unparsable cron expression could
// auto-disable a schedule (schedules.go's fireOne). A routine whose
// TARGET is broken — deleted agent, expired credential, a bug in the
// routine itself — fires on every cron tick, fails every time, and
// alertFailedScheduledRun raises a MANAGER inbox card per failed run
// forever. That's an unbounded inbox-spam + agent-cost bleed with no
// backstop.
//
//   - consecutive_failures counts back-to-back FAILED fires. Reset to
//     0 on a COMPLETED fire; incremented on a FAILED fire. Left alone
//     on SKIPPED/WAITING/DEDUPED — those are non-terminal or healthy
//     outcomes, not failures.
//   - max_consecutive_failures is the per-schedule trip threshold.
//     Defaults to 5 (DEFAULT 5 below); a schedule can opt into a
//     tighter or looser threshold at create/update time.
//   - disabled_reason records WHY a schedule is disabled so the CLI
//     (`schedules list` / `routine doctor`) can distinguish "an
//     operator disabled this" (NULL) from "the circuit breaker
//     tripped" ("circuit_breaker") without guessing from enabled=0
//     alone. `schedules enable` (Save with enabled transitioning
//     false→true) clears both disabled_reason and consecutive_failures
//     — re-enabling gives the schedule a clean slate.
const migrationScheduleCircuitBreaker = `
ALTER TABLE pipeline_schedules ADD COLUMN consecutive_failures INTEGER NOT NULL DEFAULT 0;
ALTER TABLE pipeline_schedules ADD COLUMN max_consecutive_failures INTEGER NOT NULL DEFAULT 5;
ALTER TABLE pipeline_schedules ADD COLUMN disabled_reason TEXT;
`
