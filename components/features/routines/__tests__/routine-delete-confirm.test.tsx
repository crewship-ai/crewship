import { describe, it, expect, vi, beforeEach } from "vitest"
import { render, screen, fireEvent, waitFor, within } from "@testing-library/react"

// #3342 — deleting a routine's schedule or webhook asks in the page, and
// Cancel leaves it in place.

const h = vi.hoisted(() => ({
  removeSchedule: vi.fn(),
  removeWebhook: vi.fn(),
}))

vi.mock("sonner", () => ({ toast: { success: vi.fn(), error: vi.fn(), info: vi.fn() } }))
vi.mock("@/hooks/use-abilities", () => ({ useAbilities: () => ({ role: "OWNER", capabilities: [] }) }))
vi.mock("@/hooks/use-realtime", () => ({ useRealtimeEvent: () => {} }))
vi.mock("@/lib/api-fetch", () => ({ apiFetch: vi.fn(async () => ({ ok: true, json: async () => [] })) }))
vi.mock("@/hooks/use-pipeline-schedules", () => ({
  usePipelineSchedules: () => ({
    schedules: [{
      id: "sch-1", workspace_id: "ws-1", name: "Morning digest", target_pipeline_id: "p1", target_pipeline_slug: "digest",
      cron_expr: "0 8 * * *", timezone: "UTC", inputs: {}, enabled: true, consecutive_failures: 0, max_consecutive_failures: 5,
      created_at: "2026-08-01T00:00:00Z", updated_at: "2026-08-01T00:00:00Z",
    }],
    loading: false, error: null, create: vi.fn(), update: vi.fn(), remove: h.removeSchedule,
  }),
}))
vi.mock("@/hooks/use-pipeline-webhooks", () => ({
  usePipelineWebhooks: () => ({
    webhooks: [{
      id: "wh-1", workspace_id: "ws-1", name: "GitHub pushes", target_pipeline_id: "p1", target_pipeline_slug: "digest",
      url: "https://example.test/hooks/wh-1", rate_limit_per_minute: 60, enabled: true, created_at: "2026-08-01T00:00:00Z", updated_at: "2026-08-01T00:00:00Z",
    }],
    loading: false, error: null, refresh: vi.fn(), create: vi.fn(), update: vi.fn(), remove: h.removeWebhook,
  }),
}))

import { RoutineSchedulesTab } from "../routine-schedules-tab"
import { RoutineWebhooksTab } from "../routine-webhooks-tab"

async function answer(title: string, label: string) {
  const dialog = await screen.findByRole("alertdialog", { name: title })
  fireEvent.click(within(dialog).getByRole("button", { name: label }))
  await waitFor(() => expect(screen.queryByRole("alertdialog")).not.toBeInTheDocument())
  return dialog
}

beforeEach(() => {
  h.removeSchedule.mockReset().mockResolvedValue(undefined)
  h.removeWebhook.mockReset().mockResolvedValue(undefined)
})

describe("deleting a schedule", () => {
  it("asks first; Cancel keeps it, confirm deletes it", async () => {
    render(<RoutineSchedulesTab workspaceId="ws-1" pipelineId="p1" slug="digest" />)
    fireEvent.click(screen.getByRole("button", { name: "Delete schedule Morning digest" }))
    const dialog = await answer("Delete schedule Morning digest?", "Cancel")
    expect(dialog).toHaveTextContent("The routine stops starting on this schedule.")
    expect(h.removeSchedule).not.toHaveBeenCalled()

    fireEvent.click(screen.getByRole("button", { name: "Delete schedule Morning digest" }))
    await answer("Delete schedule Morning digest?", "Delete schedule")
    await waitFor(() => expect(h.removeSchedule).toHaveBeenCalledWith("sch-1"))
  })
})

describe("deleting a webhook", () => {
  it("asks first and says senders will fail; Cancel keeps it, confirm deletes it", async () => {
    render(<RoutineWebhooksTab workspaceId="ws-1" pipelineId="p1" slug="digest" />)
    fireEvent.click(screen.getByRole("button", { name: "Delete webhook GitHub pushes" }))
    const dialog = await answer("Delete webhook GitHub pushes?", "Cancel")
    expect(dialog).toHaveTextContent("Existing senders will start failing")
    expect(h.removeWebhook).not.toHaveBeenCalled()

    fireEvent.click(screen.getByRole("button", { name: "Delete webhook GitHub pushes" }))
    await answer("Delete webhook GitHub pushes?", "Delete webhook")
    await waitFor(() => expect(h.removeWebhook).toHaveBeenCalledWith("wh-1"))
  })
})
