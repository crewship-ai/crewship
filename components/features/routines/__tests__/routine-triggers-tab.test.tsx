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
  update: vi.fn(async () => ({})),
  createWebhook: vi.fn(async (body: Record<string, unknown>) => ({ id: "w9", token: "tok_9", signing_secret: "sec_9", ...body })),
  starts: [] as Record<string, unknown>[],
  refreshStarts: vi.fn(async () => {}),
  schedules: [] as Record<string, unknown>[],
  webhooks: [] as Record<string, unknown>[],
  role: "ADMIN",
}))
vi.mock("@/hooks/use-pending-starts", () => ({ usePendingStarts: () => ({ starts: h.starts, loading: false, error: null, refresh: h.refreshStarts }) }))
vi.mock("@/lib/api-fetch", () => ({ apiFetch: h.fetcher, broadcastSessionExpired: vi.fn() }))
vi.mock("sonner", () => ({ toast: { success: vi.fn(), error: vi.fn() } }))
vi.mock("@/hooks/use-realtime", () => ({ useRealtimeEvent: () => {}, useRealtimeEventSafe: () => {} }))
vi.mock("@/hooks/use-abilities", () => ({ useAbilities: () => ({ role: h.role, capabilities: [] }) }))
vi.mock("@/hooks/use-pipeline-schedules", () => ({
  usePipelineSchedules: () => ({ schedules: h.schedules, loading: false, error: null, refresh: vi.fn(), create: h.create, update: h.update, remove: h.remove, preview: h.preview }),
}))
vi.mock("@/hooks/use-pipeline-webhooks", () => ({
  usePipelineWebhooks: () => ({ webhooks: h.webhooks, loading: false, error: null, create: h.createWebhook, update: vi.fn(), remove: vi.fn() }),
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
const schedule = { id: "s1", name: "Mornings", target_pipeline_slug: "weekly", target_pipeline_id: "pipe-1", cron_expr: "0 9 * * *", timezone: "UTC", enabled: true, inputs: {}, consecutive_failures: 0, max_consecutive_failures: 5 }
const webhook = { id: "w1", name: "competitor-change", target_pipeline_id: "pipe-1", target_pipeline_slug: "weekly", enabled: true, rate_limit_per_min: 60, fire_count: 1, token: "t", signing_secret_set: true, inputs_template: {} }
function openRowMenu(label: string) {
  fireEvent.pointerDown(screen.getByRole("button", { name: `Options for ${label}` }), { button: 0, ctrlKey: false })
}

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
  h.starts = []
  h.update.mockClear()
  h.createWebhook.mockClear()
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
    h.schedules = [schedule]
    h.fetcher.mockResolvedValue(json([]))
    renderTab()
    openRowMenu("Schedule Every day at 09:00")
    fireEvent.click(await screen.findByRole("menuitem", { name: "Delete…" }))
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

  it("one list and nothing below it repeats the triggers (RT-08)", () => {
    h.schedules = [schedule]
    h.webhooks = [webhook]
    h.fetcher.mockResolvedValue(json({ items: [], next_cursor: null }))
    renderTab()
    const list = screen.getByTestId("routine-trigger-list")
    expect(within(list).getByText("Every day at 09:00")).toBeInTheDocument()
    expect(within(list).getByText("competitor-change")).toBeInTheDocument()
    // Each trigger once: no managing cards for schedules, webhooks or one-time starts.
    expect(screen.getAllByText("Every day at 09:00")).toHaveLength(1)
    expect(screen.getAllByText("competitor-change")).toHaveLength(1)
    expect(screen.queryByRole("region", { name: "Schedules" })).not.toBeInTheDocument()
    expect(screen.queryByRole("region", { name: "One-time starts" })).not.toBeInTheDocument()
    expect(screen.queryByLabelText("Manage triggers")).not.toBeInTheDocument()
  })

  it("Edit on a schedule row opens the schedule editor; Turn off sits in the row menu", async () => {
    h.schedules = [schedule]
    h.fetcher.mockResolvedValue(json([]))
    renderTab()
    fireEvent.click(screen.getByRole("button", { name: "Edit schedule Every day at 09:00" }))
    expect(await screen.findByRole("dialog", { name: /Repeating schedule/ })).toBeInTheDocument()
    fireEvent.keyDown(document.activeElement ?? document.body, { key: "Escape" })
    openRowMenu("Schedule Every day at 09:00")
    fireEvent.click(await screen.findByRole("menuitem", { name: "Turn off" }))
    await waitFor(() => expect(h.update).toHaveBeenCalledWith("s1", { cron_expr: "0 9 * * *", enabled: false }))
  })

  it("Edit on a webhook row opens the webhook editor", async () => {
    h.webhooks = [webhook]
    h.fetcher.mockResolvedValue(json({ items: [], next_cursor: null }))
    renderTab()
    fireEvent.click(screen.getByRole("button", { name: "Edit webhook competitor-change" }))
    expect(await screen.findByRole("dialog", { name: /Edit webhook/ })).toBeInTheDocument()
  })

  it("a planned one-time start is a row of the list and can be cancelled there", async () => {
    h.starts = [{ id: "ps1", pipeline_slug: "weekly", fire_at: "2030-01-02T09:00:00Z", status: "pending", can_cancel: true }]
    h.fetcher.mockResolvedValue(json({}))
    renderTab()
    const row = within(screen.getByTestId("routine-trigger-list")).getByText("One-time").closest("li")!
    expect(row).toHaveAttribute("data-kind", "once")
    fireEvent.click(within(row as HTMLElement).getByRole("button", { name: /Cancel this start/ }))
    await waitFor(() => expect(h.fetcher).toHaveBeenCalledWith("/api/v1/workspaces/ws/pipelines/pending/ps1/cancel", { method: "POST" }))
  })

  it("Add trigger › Webhook creates it in the dialog and shows the URL and secret once", async () => {
    h.fetcher.mockResolvedValue(json({ items: [], next_cursor: null }))
    renderTab()
    fireEvent.click(screen.getByRole("button", { name: "Add trigger" }))
    const dialog = screen.getByRole("dialog")
    fireEvent.click(within(dialog).getByRole("radio", { name: "Webhook" }))
    fireEvent.change(within(dialog).getByLabelText("Webhook name"), { target: { value: "github-push" } })
    fireEvent.click(within(dialog).getByRole("button", { name: "Create webhook" }))
    await waitFor(() => expect(h.createWebhook).toHaveBeenCalledWith(expect.objectContaining({ name: "github-push", target_pipeline_slug: "weekly", ingress_profile: "crewship", enabled: true })))
    expect(await screen.findByText(/copy values now/i)).toBeInTheDocument()
  })

  it("each schedule and webhook row carries its #schedule-/#webhook- anchor, and the URL hash focuses it", async () => {
    // ⌘K schedule hits and the calendar link to /routines?slug=…&view=plan#schedule-<id>;
    // the row is the deep-link target, as it was on the Plan tab.
    h.schedules = [schedule]
    h.webhooks = [webhook]
    h.fetcher.mockResolvedValue(json({ items: [], next_cursor: null }))
    const scrollIntoView = vi.fn()
    Element.prototype.scrollIntoView = scrollIntoView
    window.history.replaceState(null, "", "/routines?slug=weekly&view=plan#schedule-s1")
    try {
      renderTab()
      const list = screen.getByTestId("routine-trigger-list")
      const row = list.querySelector("li#schedule-s1")
      expect(row).not.toBeNull()
      expect(row).toHaveAttribute("data-kind", "schedule")
      expect(list.querySelector("li#webhook-w1")).toHaveAttribute("data-kind", "webhook")
      await waitFor(() => expect(document.activeElement).toBe(row))
      expect(scrollIntoView).toHaveBeenCalledWith({ block: "center" })
    } finally {
      window.history.replaceState(null, "", "/")
    }
  })

  it("an edited schedule shows its new recurrence without a reload", async () => {
    // The page computes the rows once from its own schedules list; the tab
    // saves through its own. After Save the row must follow the tab's list.
    h.schedules = [schedule]
    h.fetcher.mockResolvedValue(json({ items: [], next_cursor: null }))
    const staleRows = routineTriggerRows({ slug: "weekly", lifecycle: "active", schedules: [schedule] as never[], webhooks: [], automations: [] })
    const view = render(<RoutineTriggersTab workspaceId="ws" routine={routine} lifecycle="active" depth="simple" rows={staleRows} onOpenRun={vi.fn()} onSettings={vi.fn()} />)
    expect(within(screen.getByTestId("routine-trigger-list")).getByText("Every day at 09:00")).toBeInTheDocument()
    h.schedules = [{ ...schedule, cron_expr: "15 3 * * *", timezone: "Europe/Prague" }]
    view.rerender(<RoutineTriggersTab workspaceId="ws" routine={routine} lifecycle="active" depth="simple" rows={staleRows} onOpenRun={vi.fn()} onSettings={vi.fn()} />)
    const list = screen.getByTestId("routine-trigger-list")
    expect(within(list).getByText("Every day at 03:15")).toBeInTheDocument()
    expect(within(list).queryByText("Every day at 09:00")).toBeNull()
    expect(list.querySelector("li#schedule-s1")).toHaveTextContent(/Europe\/Prague/)
  })

  it("a draft-only routine has nothing to start yet", () => {
    render(<RoutineTriggersTab workspaceId="ws" routine={{ ...routine, head_version: 0, draft_only: true }} lifecycle="active" depth="simple" rows={[]} onOpenRun={vi.fn()} onSettings={vi.fn()} />)
    expect(screen.getByText(/Not live yet/)).toBeInTheDocument()
    expect(screen.queryByRole("button", { name: "Add trigger" })).toBeNull()
  })
})
