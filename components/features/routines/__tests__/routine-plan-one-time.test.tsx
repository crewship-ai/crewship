import { render, screen, within } from "@testing-library/react"
import { describe, expect, it, vi } from "vitest"
import { RoutineSchedulesTab } from "../routine-schedules-tab"
const { mode } = vi.hoisted(() => ({
  mode: { recurring: false, message: "Daily summary" },
}))
vi.mock("@/hooks/use-pipeline-schedules", () => ({
  usePipelineSchedules: () => ({
    schedules: mode.recurring
      ? [
          {
            id: "daily",
            name: "Daily",
            target_pipeline_slug: "incident-timeline",
            cron_expr: "0 9 * * *",
            timezone: "UTC",
            enabled: true,
            inputs: { message: mode.message },
          },
        ]
      : [],
    loading: false,
    error: null,
    create: vi.fn(),
    update: vi.fn(),
    remove: vi.fn(),
    preview: vi.fn(),
  }),
}))
vi.mock("@/lib/api-fetch", () => ({
  apiFetch: vi.fn(async () => ({
    ok: true,
    json: async () => [
      {
        id: "from-calendar",
        pipeline_slug: "incident-timeline",
        fire_at: "2026-09-09T08:00:00Z",
        inputs: { message: "Once summary" },
      },
    ],
  })),
}))

describe("routine Plan", () => {
  it("hides credential-like values under neutral keys in schedule rows", async () => {
    mode.recurring = true
    mode.message = "ghp_" + "x".repeat(36)
    render(
      <RoutineSchedulesTab
        workspaceId="ws"
        pipelineId="pipeline"
        slug="incident-timeline"
      />,
    )
    expect(await screen.findByText("Inputs: message: Hidden")).toBeInTheDocument()
    expect(screen.queryByText(mode.message, { exact: false })).not.toBeInTheDocument()
  })
  it.each([false, true])(
    "shows one-time presets with recurring plans: %s",
    async (recurring) => {
      mode.recurring = recurring
      mode.message = "Daily summary"
      render(
        <RoutineSchedulesTab
          workspaceId="ws"
          pipelineId="pipeline"
          slug="incident-timeline"
        />,
      )
      const schedules = within(screen.getByRole("region", { name: "Schedules" }))
      expect(await schedules.findByText(/9 Sept 2026, 08:00 · UTC/)).toBeInTheDocument()
      expect(schedules.getByText("Inputs: message: Once summary")).toBeInTheDocument()
      if (recurring)
        expect(schedules.getByText("Inputs: message: Daily summary")).toBeInTheDocument()
      expect(schedules.getByText("Scheduled · One-time start")).toBeInTheDocument()
      expect(
        schedules.getByRole("button", { name: "Cancel scheduled start" }),
      ).toBeInTheDocument()
      expect(screen.queryByText("No schedules yet")).not.toBeInTheDocument()
      expect(schedules.getByRole("button", { name: "Add schedule" })).toBeEnabled()
    },
  )
})
