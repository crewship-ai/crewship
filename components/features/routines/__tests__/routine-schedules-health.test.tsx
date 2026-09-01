import { describe, it, expect, vi, beforeEach } from "vitest"
import { render, screen } from "@testing-library/react"

// F18 / A6 (docs/prd/PRD-ISSUES-AND-ROUTINES-2026.md): PipelineSchedule
// carries disabled_reason, consecutive_failures, max_consecutive_failures,
// last_missed_count, catchup_policy and the wake_* telemetry fields — the
// schedules tab rendered none of them. A schedule the circuit breaker had
// auto-disabled showed no reason at all. These tests render the real tab
// against a mocked usePipelineSchedules and check the reason text appears
// (and, for a plain paused schedule, that nothing crashes or invents a
// reason that was never given).

const h = vi.hoisted(() => ({
  schedules: [] as unknown[],
}))

vi.mock("@/hooks/use-pipeline-schedules", () => ({
  usePipelineSchedules: () => ({
    schedules: h.schedules,
    loading: false,
    error: null,
    create: vi.fn(),
    update: vi.fn(),
    remove: vi.fn(),
  }),
}))

import { RoutineSchedulesTab } from "@/components/features/routines/routine-schedules-tab"

function baseSchedule(overrides: Record<string, unknown> = {}) {
  return {
    id: "sch-1",
    workspace_id: "ws-1",
    name: "Morning digest",
    target_pipeline_id: "p1",
    target_pipeline_slug: "digest",
    cron_expr: "0 8 * * *",
    timezone: "UTC",
    inputs: {},
    enabled: true,
    consecutive_failures: 0,
    max_consecutive_failures: 5,
    created_at: "2026-08-01T00:00:00Z",
    updated_at: "2026-08-01T00:00:00Z",
    ...overrides,
  }
}

beforeEach(() => {
  h.schedules = []
})

describe("RoutineSchedulesTab — reliability display (read-only)", () => {
  it("renders disabled_reason as the visible reason a circuit-breaker-disabled schedule stopped", () => {
    h.schedules = [
      baseSchedule({
        id: "sch-cb",
        enabled: false,
        disabled_reason: "circuit_breaker",
        consecutive_failures: 5,
        max_consecutive_failures: 5,
      }),
    ]
    render(<RoutineSchedulesTab workspaceId="ws-1" pipelineId="p1" slug="digest" />)

    expect(screen.getByTestId("schedule-health-sch-cb")).toHaveTextContent(/circuit breaker/i)
    const reason = screen.getByTestId("schedule-health-reason-sch-cb")
    expect(reason).toHaveTextContent(/5/)
    expect(reason).toHaveTextContent(/consecutive failures/i)
  })

  it("degrades gracefully for a plain operator-paused schedule: no reason node, no crash, no invented text", () => {
    h.schedules = [
      baseSchedule({
        id: "sch-paused",
        enabled: false,
        disabled_reason: undefined,
        consecutive_failures: 0,
      }),
    ]
    render(<RoutineSchedulesTab workspaceId="ws-1" pipelineId="p1" slug="digest" />)

    expect(screen.getByText("paused")).toBeInTheDocument()
    expect(screen.queryByTestId("schedule-health-reason-sch-paused")).not.toBeInTheDocument()
    expect(screen.queryByTestId("schedule-health-sch-paused")).not.toBeInTheDocument()
  })

  it("shows the consecutive-failure count for a healthy, enabled schedule without alarming pills", () => {
    h.schedules = [baseSchedule({ id: "sch-ok" })]
    render(<RoutineSchedulesTab workspaceId="ws-1" pipelineId="p1" slug="digest" />)

    expect(screen.queryByTestId("schedule-health-sch-ok")).not.toBeInTheDocument()
    expect(screen.getByText(/Failures:/)).toBeInTheDocument()
  })

  it("surfaces last_missed_count and catchup_policy when present", () => {
    h.schedules = [
      baseSchedule({ id: "sch-missed", catchup_policy: "all", last_missed_count: 3 }),
    ]
    render(<RoutineSchedulesTab workspaceId="ws-1" pipelineId="p1" slug="digest" />)

    expect(screen.getByText(/Catch-up:/)).toBeInTheDocument()
    expect(screen.getByText(/Missed last tick: 3/)).toBeInTheDocument()
  })
})
