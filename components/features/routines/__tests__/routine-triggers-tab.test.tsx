import * as React from "react"
import { describe, it, expect, vi, beforeEach } from "vitest"
import { render, screen, fireEvent, waitFor, within } from "@testing-library/react"
import { RoutineTriggersTab } from "../routine-triggers-tab"
import { routineTriggerRows } from "@/lib/routine-page"
import type { RoutineDetail } from "../routines-detail-panel"

const h = vi.hoisted(() => ({
  fetcher: vi.fn(),
  create: vi.fn(async () => ({})),
  remove: vi.fn(async () => {}),
  preview: vi.fn(async () => ({ cron_expr: "0 9 * * *", timezone: "Europe/Prague", occurrences: ["2026-10-18T07:00:00Z", "2026-10-19T07:00:00Z", "2026-10-20T07:00:00Z", "2026-10-21T07:00:00Z", "2026-10-22T07:00:00Z"] })),
  schedules: [] as Record<string, unknown>[],
  webhooks: [] as Record<string, unknown>[],
  role: "ADMIN",
}))
vi.mock("@/lib/api-fetch", () => ({ apiFetch: h.fetcher, broadcastSessionExpired: vi.fn() }))
vi.mock("sonner", () => ({ toast: { success: vi.fn(), error: vi.fn() } }))
vi.mock("@/hooks/use-realtime", () => ({ useRealtimeEvent: () => {}, useRealtimeEventSafe: () => {} }))
vi.mock("@/hooks/use-abilities", () => ({ useAbilities: () => ({ role: h.role, capabilities: [] }) }))
vi.mock("@/hooks/use-pipeline-schedules", () => ({
  usePipelineSchedules: () => ({ schedules: h.schedules, loading: false, error: null, refresh: vi.fn(), create: h.create, update: vi.fn(), remove: h.remove, preview: h.preview }),
}))
vi.mock("@/hooks/use-pipeline-webhooks", () => ({
  usePipelineWebhooks: () => ({ webhooks: h.webhooks, loading: false, error: null, create: vi.fn(), update: vi.fn(), remove: vi.fn() }),
}))
vi.mock("@/hooks/use-automations", () => ({ useAutomations: () => ({ automations: [], loading: false, error: null }) }))
// The preset form reads the routine's inputs; this routine has none.
vi.mock("../routine-preset-form", () => ({
  RoutinePresetForm: ({ formId, onSave, onReadyChange }: { formId?: string; onSave: (i: Record<string, unknown>) => void; onReadyChange?: (r: boolean) => void }) => {
    React.useEffect(() => onReadyChange?.(true), [onReadyChange])
    return <form id={formId} onSubmit={(e) => { e.preventDefault(); onSave({}) }} />
  },
}))
vi.mock("../routine-once-schedule", () => ({ RoutineOnceSchedule: () => <section aria-label="One-time starts" /> }))

const routine = { id: "pipe-1", slug: "weekly", name: "Weekly", definition: { steps: [] }, head_version: 2 } as unknown as RoutineDetail
const json = (body: unknown) => ({ ok: true, status: 200, json: async () => body })

function renderTab() {
  const rows = routineTriggerRows({
    slug: "weekly",
    lifecycle: "active",
    schedules: h.schedules as never[],
    webhooks: h.webhooks as never[],
    automations: [],
  })
  return render(<RoutineTriggersTab workspaceId="ws" routine={routine} lifecycle="active" depth="simple" rows={rows} onOpenRun={vi.fn()} onSettings={vi.fn()} />)
}

beforeEach(() => {
  h.fetcher.mockReset()
  h.create.mockClear()
  h.remove.mockClear()
  h.schedules = []
  h.webhooks = []
  h.role = "ADMIN"
})

describe("<RoutineTriggersTab>", () => {
  it("Add trigger › Schedule previews the next 5 starts and saves cron and time zone", async () => {
    renderTab()
    fireEvent.click(screen.getByRole("button", { name: "Add trigger" }))
    const dialog = screen.getByRole("dialog")
    fireEvent.change(within(dialog).getByLabelText("Timezone"), { target: { value: "Europe/Prague" } })
    await waitFor(() => expect(within(dialog).getByTestId("add-trigger-next").children).toHaveLength(5))
    expect(h.preview).toHaveBeenLastCalledWith("0 9 * * *", "Europe/Prague", 5)
    fireEvent.click(within(dialog).getByRole("button", { name: "Add schedule" }))
    await waitFor(() => expect(h.create).toHaveBeenCalled())
    expect(h.create).toHaveBeenCalledWith({ name: "Every day at 09:00", target_pipeline_slug: "weekly", cron_expr: "0 9 * * *", timezone: "Europe/Prague", inputs: {}, enabled: true })
  })

  it("delete schedule asks the shared ConfirmDialog (no window.confirm)", async () => {
    const confirm = vi.fn(() => true)
    window.confirm = confirm
    h.schedules = [{ id: "s1", name: "Mornings", target_pipeline_slug: "weekly", target_pipeline_id: "pipe-1", cron_expr: "0 9 * * *", timezone: "UTC", enabled: true, inputs: {}, consecutive_failures: 0, max_consecutive_failures: 5 }]
    h.fetcher.mockResolvedValue(json([]))
    renderTab()
    fireEvent.click(screen.getByRole("button", { name: "Delete schedule Mornings" }))
    const dialog = await screen.findByTestId("confirm-dialog")
    expect(confirm).not.toHaveBeenCalled()
    fireEvent.click(within(dialog).getByRole("button", { name: "Delete schedule" }))
    await waitFor(() => expect(h.remove).toHaveBeenCalledWith("s1"))
  })

  it("webhook deliveries table reads routine-webhook-receipts", async () => {
    h.webhooks = [{ id: "w1", name: "competitor-change", target_pipeline_id: "pipe-1", target_pipeline_slug: "weekly", enabled: true, rate_limit_per_min: 60, fire_count: 1, token: "t", signing_secret_set: true, inputs_template: {} }]
    h.fetcher.mockImplementation(async (url: string) =>
      url.includes("/routine-webhook-receipts?webhook_id=w1")
        ? json({ items: [{ id: "r1", webhook_id: "w1", run_id: "run_abcdef12", run_status: "completed", received_at: "2026-10-15T08:00:00Z" }], next_cursor: null })
        : json([]),
    )
    renderTab()
    const table = await screen.findByTestId("routine-webhook-deliveries")
    expect(table).toHaveTextContent("competitor-change")
    expect(table).toHaveTextContent("run_abcd")
    expect(table.querySelector('[data-slot="status-pill"]')).not.toBeNull()
  })

  it("a draft-only routine has nothing to start yet", () => {
    render(<RoutineTriggersTab workspaceId="ws" routine={{ ...routine, head_version: 0, draft_only: true }} lifecycle="active" depth="simple" rows={[]} onOpenRun={vi.fn()} onSettings={vi.fn()} />)
    expect(screen.getByText(/Not live yet/)).toBeInTheDocument()
    expect(screen.queryByRole("button", { name: "Add trigger" })).toBeNull()
  })
})
