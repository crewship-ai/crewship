import { describe, it, expect, vi, beforeAll, beforeEach, afterEach } from "vitest"
import { render, screen, fireEvent, waitFor, cleanup, act, within } from "@testing-library/react"

const api = vi.fn()
vi.mock("@/lib/api-fetch", () => ({ apiFetch: (...a: unknown[]) => api(...a) }))
let role = "OWNER"
vi.mock("@/hooks/use-workspace", () => ({ useWorkspace: () => ({ workspaceId: "ws", role, loading: false }) }))
vi.mock("@/hooks/use-mobile", () => ({ useIsMobile: () => false }))

import { AuditLogPage } from "../audit-log-page"

const ok = (b: unknown) => ({ ok: true, status: 200, json: async () => b })
const auditCalls = () => api.mock.calls.map(([u]) => String(u)).filter((u) => u.startsWith("/api/v1/audit"))

// Radix Select reads pointer capture and scrolls its list into view; happy-dom
// has neither.
beforeAll(() => {
  Element.prototype.scrollIntoView = vi.fn()
  Element.prototype.hasPointerCapture = vi.fn(() => false)
  Element.prototype.setPointerCapture = vi.fn()
  Element.prototype.releasePointerCapture = vi.fn()
})

beforeEach(() => {
  api.mockReset()
  api.mockImplementation(async (u: string) => (u.includes("/members") ? ok([]) : ok({ data: [], pagination: { page: 1, limit: 50, total: 0, total_pages: 1 } })))
  role = "OWNER"
  window.history.replaceState(null, "", "/settings/audit")
})
afterEach(() => { cleanup(); vi.useRealTimers() })

// The Audit log as a nested page: the side panel picks the trail, the time
// range is the Crew Journal's picker on the sub-bar (a calendar behind
// "Custom…"), the table is full width, and all of it lives in the URL.
/** Pick a range on the top bar: a preset segment, or "Custom…" — the
 *  calendar button beside them. */
async function pickRange(label: string) {
  if (label === "Custom…") fireEvent.click(screen.getByRole("button", { name: "Custom range" }))
  else fireEvent.click(within(screen.getByRole("radiogroup", { name: "Time range" })).getByRole("radio", { name: label }))
}

describe("Audit log page", () => {
  it("picks the range on the sub-bar, then a trail that keeps it", async () => {
    render(<AuditLogPage />)
    await waitFor(() => expect(auditCalls().length).toBeGreaterThan(0))
    await pickRange("Last 30 days")
    await waitFor(() => expect(new URLSearchParams(window.location.search).get("audit_range")).toBe("30d"))
    fireEvent.click(screen.getByRole("button", { name: /^Keeper/ }))
    await waitFor(() => expect(auditCalls().at(-1)).toContain("source=keeper"))
    expect(auditCalls().at(-1)).toContain("date_from=")
    const q = new URLSearchParams(window.location.search)
    expect(q.get("audit_source")).toBe("keeper")
    expect(q.get("audit_range")).toBe("30d")
  })

  it("filters failed runs on the server and shows the chip", async () => {
    render(<AuditLogPage />)
    fireEvent.click(screen.getByRole("button", { name: /^Failed/ }))
    await waitFor(() => expect(auditCalls().at(-1)).toContain("action=agent.run.failed"))
    expect(screen.getByRole("button", { name: "Remove filter Runs: failed" })).toBeInTheDocument()
    fireEvent.click(screen.getByRole("button", { name: "Clear filters" }))
    await waitFor(() => expect(auditCalls().at(-1)).not.toContain("action="))
  })

  // A day in the calendar is a one-day custom range, sent as whole UTC days.
  it("picks a day from the Custom… calendar and stretches it with shift", async () => {
    // Mid-month, so the current month has past days to pick: on the 1st only
    // today is enabled. Only Date is faked; React's timers stay real.
    vi.useFakeTimers({ toFake: ["Date"] })
    vi.setSystemTime(new Date("2026-09-15T12:00:00Z"))
    render(<AuditLogPage />)
    await pickRange("Custom…")
    await waitFor(() => expect(document.querySelector("[data-slot=audit-calendar]")).not.toBeNull())
    const days = document.querySelectorAll<HTMLButtonElement>("[data-slot=audit-calendar] [data-day]")
    const enabled = [...days].filter((d) => !d.disabled)
    const a = enabled[0].dataset.day!, b = enabled[2].dataset.day!
    fireEvent.click(enabled[0])
    await waitFor(() => expect(new URLSearchParams(window.location.search).get("audit_from")).toBe(a))
    fireEvent.click(enabled[2], { shiftKey: true })
    await waitFor(() => expect(new URLSearchParams(window.location.search).get("audit_to")).toBe(b))
    expect(new URLSearchParams(window.location.search).get("audit_range")).toBe("custom")
    expect(auditCalls().at(-1)).toContain(`date_from=${a}T00%3A00%3A00.000Z`)
  })

  // The Crew Journal's chart, as is: a bar picked on it narrows the table to
  // that slice of the range, the range itself stays, and the same bar again
  // clears it.
  it("narrows the table to a bar picked on the Journal's chart", async () => {
    vi.useFakeTimers({ toFake: ["Date"] })
    vi.setSystemTime(new Date("2026-10-14T12:00:00Z"))
    let finishActivity!: (response: ReturnType<typeof ok>) => void
    const activity = new Promise<ReturnType<typeof ok>>(resolve => { finishActivity = resolve })
    api.mockImplementation(async (url: string) => url.includes("/members") ? ok([])
      : url.includes("limit=100") ? activity
      : ok({ data: [], pagination: { page: 1, limit: 50, total: 0, total_pages: 1 } }))
    render(<AuditLogPage />)
    const chart = await screen.findByRole("application", { name: /Event volume histogram/ })
    await waitFor(() => expect(auditCalls().length).toBeGreaterThan(0))
    fireEvent.keyDown(chart, { key: "End" })
    fireEvent.keyDown(chart, { key: "Enter" })
    await waitFor(() => expect(new URLSearchParams(window.location.search).get("audit_at")).toMatch(/^\d{4}-\d{2}-\d{2}T/))
    const q = new URLSearchParams(window.location.search)
    expect(q.get("audit_range")).toBeNull()
    expect(auditCalls().at(-1)).toContain(`date_from=${encodeURIComponent(q.get("audit_at")!)}`)
    expect(screen.getByRole("button", { name: /^Remove filter .* – / })).toBeInTheDocument()
    // The activity sample arrives after selection, at a later wall-clock time.
    // New entries must not move the selected relative-window bucket.
    vi.setSystemTime(new Date("2026-10-14T12:00:01Z"))
    await act(async () => { finishActivity(ok({ data: [], pagination: { page: 1, limit: 100, total: 0, total_pages: 1 } })) })
    fireEvent.keyDown(chart, { key: "Enter" })
    await waitFor(() => expect(new URLSearchParams(window.location.search).get("audit_at")).toBeNull())
  })

  it("lays the chart over the picked range", async () => {
    window.history.replaceState(null, "", "/settings/audit?audit_range=24h")
    render(<AuditLogPage />)
    expect(await screen.findByText(/stacked by severity/i)).toBeInTheDocument()
  })

  // The numbers next to each value are what picking it would show, read from
  // the recent activity the calendar already loaded.
  it("counts each facet value with the other filters applied", async () => {
    const at = new Date().toISOString()
    const rows = [
      { id: "1", action: "agent.run.failed", entity_type: "agent_run", user_id: null, created_at: at },
      { id: "2", action: "agent.run.failed", entity_type: "agent_run", user_id: null, created_at: at },
      { id: "3", action: "agent.run.completed", entity_type: "agent_run", user_id: null, created_at: at },
      { id: "4", action: "crew.update", entity_type: "CREW", user_id: "u1", created_at: at },
    ]
    api.mockImplementation(async (u: string) => (u.includes("/members") ? ok([]) : ok({ data: rows, pagination: { page: 1, limit: 100, total: 4, total_pages: 1 } })))
    render(<AuditLogPage />)
    await waitFor(() => expect(screen.getByRole("button", { name: /^Failed/ }).textContent).toContain("2"))
    expect(screen.getByRole("button", { name: /^Runs/ }).textContent).toContain("3")
    // "Crews" is also a trail; the category row is the one with a count.
    const crews = screen.getByRole("button", { name: /^Crews\s*1$/ })
    // Picking Crews leaves no failed runs to show.
    fireEvent.click(crews)
    await waitFor(() => expect(screen.getByRole("button", { name: /^Failed/ }).textContent).toContain("0"))
  })

  it("has a back link to Settings", () => {
    render(<AuditLogPage />)
    expect(screen.getByRole("link", { name: /settings/i }).getAttribute("href")).toBe("/settings")
  })

  it("says who can read it instead of loading a 403", () => {
    role = "MANAGER"
    render(<AuditLogPage />)
    // The shared no-access state: which role is needed, and the way back.
    expect(document.querySelector("[data-state=no-access]")).not.toBeNull()
    expect(screen.getByText(/You can’t open Audit log/)).toBeInTheDocument()
    expect(screen.getByText(/Audit log needs Admin or above/)).toBeInTheDocument()
    expect(auditCalls()).toHaveLength(0)
  })

  it("opens with the section head and offers to verify the journal chain", () => {
    render(<AuditLogPage />)
    expect(screen.getByRole("heading", { level: 2, name: "Audit log" })).toBeInTheDocument()
    expect(document.querySelector("[data-slot=journal-verify]")).not.toBeNull()
    expect(screen.getByRole("button", { name: "Verify now" })).toBeInTheDocument()
  })
})

// Harbor 2 (#3487 SET-03): v67 S-AUDIT — the range is ONE control on the top
// bar (1 h · 24 h · 7 d · 30 d · 90 d · All, and a calendar for a custom
// range), the display switches sit above the table, each row opens its
// detail, and the events page.
describe("Audit log — range, display, detail and paging", () => {
  const page = (rows: unknown[], p = 1, total = rows.length, pages = 1) =>
    ok({ data: rows, pagination: { page: p, limit: 50, total, total_pages: pages } })
  const event = (id: string, extra: Record<string, unknown> = {}) => ({
    id, action: "crew.update", entity_type: "CREW", entity_id: `crew-${id}`, entity_name: `Crew ${id}`,
    metadata: { field: "name" }, ip_address: "10.0.0.7", user_agent: "Mozilla/5.0 test",
    user: { id: "u1", email: "ada@x.io", full_name: "Ada Admin" }, created_at: new Date().toISOString(), ...extra,
  })

  it("is one segmented range on the top bar, with the calendar beside it", async () => {
    render(<AuditLogPage />)
    const group = screen.getByRole("radiogroup", { name: "Time range" })
    const radios = within(group).getAllByRole("radio")
    expect(radios.map((r) => r.textContent)).toEqual(["1 h", "24 h", "7 d", "30 d", "90 d", "All"])
    expect(within(group).getByRole("radio", { name: "Last 7 days" })).toHaveAttribute("aria-checked", "true")
    expect(screen.queryByRole("combobox", { name: "Time range" })).toBeNull()
    fireEvent.click(within(group).getByRole("radio", { name: "Last 24 hours" }))
    await waitFor(() => expect(new URLSearchParams(window.location.search).get("audit_range")).toBe("24h"))
    expect(within(group).getByRole("radio", { name: "Last 24 hours" })).toHaveAttribute("aria-checked", "true")
    fireEvent.click(screen.getByRole("button", { name: "Custom range" }))
    await waitFor(() => expect(document.querySelector("[data-slot=audit-calendar]")).not.toBeNull())
  })

  it("keeps Group repeated events and Mark access changes above the table, not in the panel", async () => {
    render(<AuditLogPage />)
    const strip = document.querySelector<HTMLElement>("[data-slot=audit-display]")!
    expect(strip).not.toBeNull()
    const group = within(strip).getByRole("switch", { name: "Group repeated events" })
    expect(group).toHaveAttribute("aria-checked", "true")
    expect(within(strip).getByRole("switch", { name: "Mark access changes" })).toHaveAttribute("aria-checked", "true")
    expect(within(screen.getByRole("complementary", { name: "Audit log navigation" })).queryByText("Group repeated events")).toBeNull()
    fireEvent.click(group)
    expect(group).toHaveAttribute("aria-checked", "false")
  })

  it("opens a row's detail: action, entity, IP, user agent, details, immutable", async () => {
    api.mockImplementation(async (u: string) => (u.includes("/members") ? ok([]) : page([event("e1")])))
    render(<AuditLogPage />)
    const row = await screen.findByRole("button", { name: /Crew e1/ })
    expect(row).toHaveAttribute("aria-expanded", "false")
    fireEvent.click(row)
    expect(row).toHaveAttribute("aria-expanded", "true")
    const detail = document.getElementById(row.getAttribute("aria-controls")!)!
    expect(detail).toHaveTextContent("crew.update")
    expect(detail).toHaveTextContent("CREW · crew-e1")
    expect(detail).toHaveTextContent("10.0.0.7")
    expect(detail).toHaveTextContent("Mozilla/5.0 test")
    expect(detail).toHaveTextContent(/"field": "name"/)
    expect(detail).toHaveTextContent("This record is immutable.")
  })

  it("pages through the events and starts again at page one when a panel filter changes", async () => {
    api.mockImplementation(async (u: string) => {
      if (u.includes("/members")) return ok([])
      if (u.includes("limit=100")) return page([], 1, 0)
      const p = Number(new URLSearchParams(u.split("?")[1]).get("page"))
      return page([event(`p${p}`)], p, 120, 3)
    })
    render(<AuditLogPage />)
    expect(await screen.findByText(/1–50 of 120 events/)).toBeInTheDocument()
    fireEvent.click(screen.getByRole("button", { name: /Next/ }))
    await waitFor(() => expect(auditCalls().filter((u) => u.includes("limit=50")).at(-1)).toContain("page=2"))
    expect(await screen.findByText(/51–100 of 120 events/)).toBeInTheDocument()
    fireEvent.click(screen.getByRole("button", { name: /^Keeper/ }))
    await waitFor(() => expect(auditCalls().filter((u) => u.includes("limit=50")).at(-1)).toContain("source=keeper"))
    expect(auditCalls().filter((u) => u.includes("limit=50")).at(-1)).toContain("page=1")
  })

  // SET-07: one section head, drawn by the Settings drill frame.
  it("says what the page is once, in the section head", () => {
    render(<AuditLogPage />)
    expect(screen.getAllByText(/Every state-changing action, immutably recorded/)).toHaveLength(1)
    const head = document.querySelector<HTMLElement>("[data-slot=drill-head]")!
    expect(head).toHaveTextContent("Admins and the owner")
  })
})
