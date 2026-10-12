import { describe, it, expect, vi, beforeAll, beforeEach, afterEach } from "vitest"
import { render, screen, fireEvent, waitFor, cleanup } from "@testing-library/react"

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
/** Open the sub-bar's range menu and choose an entry. */
async function pickRange(label: string) {
  fireEvent.keyDown(screen.getByRole("combobox", { name: "Time range" }), { key: "ArrowDown" })
  fireEvent.click(await screen.findByRole("option", { name: label }))
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

  // Like the Journal's chart: the bars cover the range the table lists, and
  // dragging across days picks them as a custom range.
  it("charts exactly the chosen range and picks days by dragging", async () => {
    vi.useFakeTimers({ toFake: ["Date"] })
    vi.setSystemTime(new Date("2026-09-15T12:00:00Z"))
    render(<AuditLogPage />)
    const chart = await waitFor(() => {
      const el = document.querySelector<HTMLElement>("[data-slot=audit-histogram]")
      if (!el) throw new Error("no chart")
      return el
    })
    expect(chart.getAttribute("data-unit")).toBe("day")
    const bars = chart.querySelectorAll<HTMLButtonElement>("[data-bucket]")
    expect(bars).toHaveLength(8)
    expect(bars[0].dataset.bucket).toBe("2026-09-08")
    fireEvent.pointerDown(bars[1])
    fireEvent.pointerEnter(bars[3])
    fireEvent.pointerUp(bars[3])
    await waitFor(() => expect(new URLSearchParams(window.location.search).get("audit_from")).toBe("2026-09-09"))
    expect(new URLSearchParams(window.location.search).get("audit_to")).toBe("2026-09-11")
  })

  it("draws hours, not days, for the last 24 hours", async () => {
    window.history.replaceState(null, "", "/settings/audit?audit_range=24h")
    render(<AuditLogPage />)
    await waitFor(() => expect(document.querySelector("[data-slot=audit-histogram]")?.getAttribute("data-unit")).toBe("hour"))
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
    expect(screen.getByText(/readable by workspace Admins/)).toBeInTheDocument()
    expect(auditCalls()).toHaveLength(0)
  })
})
