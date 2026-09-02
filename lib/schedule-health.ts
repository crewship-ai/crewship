// Schedule health — derives a read-only status summary from the reliability
// fields `PipelineSchedule` already carries (internal/api/pipeline_schedules.go)
// but the schedules tab never rendered (F18, A6:
// docs/prd/PRD-ISSUES-AND-ROUTINES-2026.md). Pulled into lib/ rather than kept
// inline in the component so it is unit-testable without a DOM — see
// lib/__tests__/schedule-health.test.ts, which is also the test that proves a
// circuit-breaker-disabled schedule shows *why*.
//
// Deliberately read-only: this module only describes state, it never writes
// it. Making these fields settable (the reliability editor) is Track B (B9)
// — out of scope here.

export type ScheduleHealthTone = "default" | "success" | "destructive" | "warn" | "blue" | "purple"

/** The subset of the scheduleResponse wire shape health derives from. */
export interface ScheduleHealthInput {
  enabled: boolean
  /** "" for an operator-initiated disable, "circuit_breaker" once the breaker trips it. */
  disabled_reason?: string
  consecutive_failures: number
  max_consecutive_failures: number
}

export interface ScheduleHealth {
  tone: ScheduleHealthTone
  label: string
  /** null when there is nothing more to say than the label. */
  reason: string | null
}

/**
 * scheduleHealth turns the four raw reliability fields into one status a
 * list row can render as a pill + optional reason line.
 *
 * Priority, highest first:
 *   1. Auto-disabled by the circuit breaker — this is the case F18 named:
 *      "a schedule disabled by the circuit breaker shows the user no
 *      reason." Always surfaces the failure count against the threshold.
 *   2. Manually paused by an operator (enabled=false, no disabled_reason).
 *   3. Enabled but has failed at least once since the last success —
 *      "at risk", so a slow slide toward the breaker is visible before it
 *      trips, not just after.
 *   4. Enabled with a clean streak — healthy.
 */
export function scheduleHealth(s: ScheduleHealthInput): ScheduleHealth {
  const max = s.max_consecutive_failures > 0 ? s.max_consecutive_failures : null

  if (!s.enabled && s.disabled_reason === "circuit_breaker") {
    return {
      tone: "destructive",
      label: "disabled — circuit breaker",
      reason:
        max != null
          ? `Auto-disabled after ${s.consecutive_failures} consecutive failures (limit ${max}). Fix the cause, then re-enable.`
          : `Auto-disabled after ${s.consecutive_failures} consecutive failures. Fix the cause, then re-enable.`,
    }
  }

  if (!s.enabled) {
    return { tone: "default", label: "paused", reason: null }
  }

  if (s.consecutive_failures > 0) {
    return {
      tone: "warn",
      label: "at risk",
      reason:
        max != null
          ? `${s.consecutive_failures} of ${max} consecutive failures before this schedule auto-disables.`
          : `${s.consecutive_failures} consecutive failure${s.consecutive_failures === 1 ? "" : "s"}.`,
    }
  }

  return { tone: "success", label: "healthy", reason: null }
}
