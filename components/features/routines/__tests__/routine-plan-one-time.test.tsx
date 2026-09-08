import { render, screen, within } from "@testing-library/react"
import { describe, expect, it, vi } from "vitest"
import { RoutineSchedulesTab } from "../routine-schedules-tab"
vi.mock("@/hooks/use-pipeline-schedules", () => ({ usePipelineSchedules: () => ({ schedules: [], loading: false, error: null, create: vi.fn(), update: vi.fn(), remove: vi.fn(), preview: vi.fn() }) }))
vi.mock("@/lib/api-fetch", () => ({ apiFetch: vi.fn(async () => ({ ok: true, json: async () => [{ id: "from-calendar", pipeline_slug: "incident-timeline", fire_at: "2026-09-09T08:00:00Z" }] })) }))

describe("routine Plan", () => {
  it("includes a Calendar one-time start in Schedules even without recurring triggers", async () => {
    render(<RoutineSchedulesTab workspaceId="ws" pipelineId="pipeline" slug="incident-timeline" />)
    const schedules = within(screen.getByRole("region", { name: "Schedules" }))
    expect(await schedules.findByText(/Wed,? 9 Sept 2026/)).toBeInTheDocument()
    expect(schedules.getByText("Scheduled · One-time start")).toBeInTheDocument()
    expect(schedules.getByRole("button", { name: "Cancel scheduled start" })).toBeInTheDocument()
    expect(screen.queryByText("No schedules yet")).not.toBeInTheDocument()
    expect(schedules.getByRole("button", { name: "Add schedule" })).toBeEnabled()
  })
})
