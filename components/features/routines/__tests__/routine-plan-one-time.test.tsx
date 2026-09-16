import { fireEvent, render, screen, within } from "@testing-library/react"
import { describe, expect, it, vi } from "vitest"
import { RoutineSchedulesTab, defaultScheduleName, scheduleVersionLine } from "../routine-schedules-tab"
const h = vi.hoisted(() => ({
  mode: { recurring: false, message: "Daily summary" },
  update: vi.fn(async () => null),
  create: vi.fn(async () => null),
  extra: [] as Record<string, unknown>[],
}))
vi.mock("@/hooks/use-pipeline-schedules", () => ({
  usePipelineSchedules: () => ({
    schedules: [
      ...(h.mode.recurring
        ? [
            {
              id: "daily",
              name: "Daily",
              target_pipeline_slug: "incident-timeline",
              cron_expr: "0 9 * * *",
              timezone: "UTC",
              enabled: true,
              next_run_at: "2026-09-10T09:00:00Z",
              inputs: { message: h.mode.message },
              consecutive_failures: 0,
              max_consecutive_failures: 5,
            },
          ]
        : []),
      ...h.extra,
    ],
    loading: false,
    error: null,
    create: h.create,
    update: h.update,
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
        pinned_version: 2,
        inputs: { message: "Once summary" },
      },
    ],
  })),
}))

const renderPlan = (props: Partial<React.ComponentProps<typeof RoutineSchedulesTab>> = {}) =>
  render(
    <RoutineSchedulesTab workspaceId="ws" pipelineId="pipeline" slug="incident-timeline" {...props} />,
  )

describe("routine Plan", () => {
  it("hides credential-like values under neutral keys in schedule rows", async () => {
    h.mode.recurring = true
    h.mode.message = "ghp_" + "x".repeat(36)
    h.extra = []
    renderPlan()
    expect(await screen.findByTestId("schedule-uses-daily")).toHaveTextContent("with: message: Hidden")
    expect(screen.queryByText(h.mode.message, { exact: false })).not.toBeInTheDocument()
  })

  it.each([false, true])("shows one-time starts beside repeating plans: %s", async (recurring) => {
    h.mode.recurring = recurring
    h.mode.message = "Daily summary"
    h.extra = []
    renderPlan({ headVersion: 3 })
    const once = within(screen.getByRole("region", { name: "One-time starts" }))
    expect(await once.findByText(/9 Sept 2026, 08:00 · UTC/)).toBeInTheDocument()
    expect(once.getByTestId("pending-uses-from-calendar")).toHaveTextContent(
      "Pinned to v2 — publishing a newer version does not change this start · with: message: Once summary",
    )
    expect(once.getByRole("button", { name: "Remove" })).toBeInTheDocument()
    if (recurring)
      expect(screen.getByTestId("schedule-uses-daily")).toHaveTextContent("with: message: Daily summary")
    else expect(screen.getByText(/No repeating schedule/)).toBeInTheDocument()
    expect(screen.getByRole("button", { name: "Add" })).toBeEnabled()
    expect(screen.getByRole("button", { name: "Schedule a start" })).toBeEnabled()
  })

  it("says which version each repeating schedule uses: latest published or pinned", async () => {
    h.mode.recurring = true
    h.mode.message = "Daily summary"
    h.extra = [
      {
        id: "pinned",
        name: "Pinned one",
        target_pipeline_slug: "incident-timeline",
        cron_expr: "0 8 * * 1-5",
        timezone: "Europe/Prague",
        enabled: false,
        target_pipeline_version: 2,
        effective_version: 2,
        version_pinned: true,
        inputs: {},
        consecutive_failures: 0,
        max_consecutive_failures: 5,
      },
    ]
    renderPlan({ headVersion: 3 })
    const latest = await screen.findByTestId("schedule-uses-daily")
    expect(latest).toHaveTextContent(
      "Uses the latest published version (now v3) · next 10 Sept 2026, 09:00 · UTC · with: message: Daily summary",
    )
    expect(screen.getByText("Every day at 09:00")).toBeInTheDocument()
    const pinned = screen.getByTestId("schedule-uses-pinned")
    expect(pinned).toHaveTextContent(
      "Pinned to v2 — publishing a newer version does not change this start · off · with: No inputs",
    )
    expect(screen.getByText("Every weekday at 08:00")).toBeInTheDocument()
  })

  it("turns a schedule off and on with the switch, through the existing update", async () => {
    h.mode.recurring = true
    h.mode.message = "Daily summary"
    h.extra = []
    renderPlan()
    const toggle = await screen.findByRole("switch", { name: "Disable schedule Daily" })
    expect(toggle).toBeChecked()
    fireEvent.click(toggle)
    expect(h.update).toHaveBeenCalledWith("daily", { cron_expr: "0 9 * * *", enabled: false })
  })

  it("names a new schedule after its days and time, not the slug", async () => {
    expect(defaultScheduleName("0 8 * * 1-5")).toBe("Weekdays at 08:00")
    expect(defaultScheduleName("0 9 * * *")).toBe("Every day at 09:00")
    expect(defaultScheduleName("*/5 * * * *")).toBe("Every 5 minutes")
    expect(defaultScheduleName("1 2 3 4 5")).toBe("Repeating schedule")
    h.mode.recurring = false
    h.extra = []
    renderPlan()
    fireEvent.click(await screen.findByRole("button", { name: "Add" }))
    expect(screen.getByPlaceholderText("Every day at 09:00")).toBeInTheDocument()
    expect(screen.queryByPlaceholderText(/incident-timeline schedule/)).toBeNull()
  })

  it("folds webhooks, automations and the overlap rule under one closed disclosure", async () => {
    h.mode.recurring = false
    h.extra = []
    renderPlan({ concurrencyKey: "nightly", maxConcurrent: 1, otherWays: <div>Webhooks here</div> })
    const summary = await screen.findByText(/Other ways this routine starts/)
    const details = summary.closest("details") as HTMLDetailsElement
    expect(details.open).toBe(false)
    expect(details.textContent).toContain("Webhooks here")
    expect(details.textContent).toContain("Serialized by nightly")
    expect(screen.queryByText("Advanced execution settings")).toBeNull()
  })
})

describe("scheduleVersionLine", () => {
  it("reads the contract fields and falls back to the pin alone on older servers", () => {
    expect(scheduleVersionLine({ effective_version: 3, version_pinned: false }, 2)).toEqual({ pinned: false, version: 3 })
    expect(scheduleVersionLine({ target_pipeline_version: 2, effective_version: 2, version_pinned: true }, 3)).toEqual({ pinned: true, version: 2 })
    expect(scheduleVersionLine({ target_pipeline_version: 2 }, 3)).toEqual({ pinned: true, version: 2 })
    expect(scheduleVersionLine({}, 3)).toEqual({ pinned: false, version: 3 })
    expect(scheduleVersionLine({}, undefined)).toEqual({ pinned: false, version: null })
  })
})
