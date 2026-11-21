import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from "vitest"
import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react"

// v67 parity for Settings › Audit log (PAR-111, 112, 113, 114), as the round-71
// prototype has it: the four records as spaces, 24-hour times, the solid-bar
// histogram inside the Events card, and the card header. jsdom has no layout; the sizes are measured by
// e2e/settings-h2.spec.ts after deploy.

const api = vi.fn()
vi.mock("@/lib/api-fetch", () => ({ apiFetch: (...a: unknown[]) => api(...a) }))
vi.mock("@/hooks/use-workspace", () => ({ useWorkspace: () => ({ workspaceId: "ws", role: "OWNER", loading: false }) }))
vi.mock("@/hooks/use-mobile", () => ({ useIsMobile: () => false }))

import { AuditLogPage } from "../audit-log-page"

const ok = (b: unknown) => ({ ok: true, status: 200, json: async () => b })
// 09:16 in the viewer's own zone, so the assertion does not depend on it.
const WHEN = new Date(2026, 9, 18, 9, 16, 0)
const LOGS = [
  { id: "l1", action: "workspace.update", entity_type: "WORKSPACE", entity_id: "w1", metadata: null, ip_address: null, user_agent: null,
    user: { id: "u1", email: "pavel@example.com", full_name: "Pavel Srba" }, created_at: WHEN.toISOString() },
]

beforeAll(() => {
  Element.prototype.scrollIntoView = vi.fn()
  Element.prototype.hasPointerCapture = vi.fn(() => false)
  Element.prototype.setPointerCapture = vi.fn()
  Element.prototype.releasePointerCapture = vi.fn()
})
beforeEach(() => {
  cleanup()
  api.mockReset()
  api.mockImplementation(async (u: string) => (u.includes("/members") ? ok([]) : ok({ data: LOGS, pagination: { page: 1, limit: 50, total: 1, total_pages: 1 } })))
  window.history.replaceState(null, "", "/settings/audit")
})
afterEach(() => cleanup())

async function renderPage() {
  render(<AuditLogPage />)
  await screen.findByText("Pavel Srba")
}

describe("PAR-111 record switcher", () => {
  it("is the card of four records, named by what they hold, without a description under each", async () => {
    await renderPage()
    const spaces = document.querySelector<HTMLElement>("[data-slot=panel-spaces]")!
    expect(within(spaces).getAllByRole("tab").map((t) => t.textContent?.replace(/\d+$/, "").trim())).toEqual(["All changes", "Between crews", "Secret use", "Keeper decisions"])
    const panel = document.querySelector<HTMLElement>("[data-slot=secondary-panel]")!
    expect(within(panel).queryByText("Every gatekeeper decision, append-only")).toBeNull()
  })

  it("lists what changed under All changes, and moves Who and Run result into the Filter", async () => {
    await renderPage()
    const sections = Array.from(document.querySelectorAll("[data-slot=drill-section]"))
    expect(sections.map((s) => s.firstElementChild!.textContent!.replace(/[\d]+( active)?$/, "").trim())).toEqual(["What changed"])
    for (const s of sections) expect(s.querySelector("button[aria-expanded]")).toBeNull()
    const nav = within(screen.getByRole("complementary", { name: "Audit log navigation" }))
    expect(nav.getByRole("button", { name: /^Everything/ })).toBeInTheDocument()
    expect(nav.getByRole("button", { name: /^Workspace settings/ })).toBeInTheDocument()
    expect(nav.queryByRole("button", { name: /^All events/ })).toBeNull()
    expect(document.querySelector("[data-slot=facet-block]")).toBeNull()
    fireEvent.click(nav.getByRole("button", { name: /^Filter/ }))
    const block = document.querySelector<HTMLElement>("[data-slot=facet-block]")!
    expect(within(block).getByRole("button", { name: /^Run result/ })).toBeInTheDocument()
  })

  it("keeps the record in the URL and the time range through a switch", async () => {
    await renderPage()
    fireEvent.click(screen.getByRole("tab", { name: /Keeper decisions/ }))
    await waitFor(() => expect(new URLSearchParams(window.location.search).get("audit_source")).toBe("keeper"))
  })

  it("on the other records says what is in it and that only time filters", async () => {
    await renderPage()
    fireEvent.click(screen.getByRole("tab", { name: /Between crews/ }))
    expect(await screen.findByText(/Work handed over, messages and shared files\. This record filters by time only\./)).toBeInTheDocument()
    expect(screen.queryByRole("button", { name: /^Filter/ })).toBeNull()
    expect(screen.getByRole("textbox", { name: /search the audit log/i }).closest("div")).toHaveClass("opacity-50")
  })
})

describe("PAR-112 times and histogram", () => {
  it("writes times on the 24-hour clock", async () => {
    await renderPage()
    const row = document.querySelector<HTMLElement>("[data-audit-weight]")!
    // (A second copy sits in the phone layout, hidden from sm up.)
    expect(within(row).getAllByText("09:16").length).toBeGreaterThan(0)
    expect(row.textContent).not.toMatch(/\b(AM|PM)\b/i)
  })

  it("draws the histogram as solid bars in one colour, captioned on the axis", async () => {
    await renderPage()
    const chart = document.querySelector<HTMLElement>("[data-slot=audit-histogram]")!
    expect(chart.textContent).not.toMatch(/stacked by severity/i)
    expect(chart.querySelector(".uppercase")).toBeNull()
    expect(chart.querySelector("[class*='font-mono']")).toBeNull()
    const bars = Array.from(chart.querySelectorAll<HTMLElement>("[data-bar]"))
    expect(bars.length).toBeGreaterThan(10)
    // Every bar is the accent (a tint of it, the latest in full): no severity hues.
    for (const b of bars) expect(b.className).toMatch(/bg-primary/)
    expect(within(chart).getByText("Drag to pick a slice of time")).toBeInTheDocument()
    expect(within(chart).getByText("Today")).toBeInTheDocument()
    expect(within(chart).getByRole("application", { name: /Event volume histogram/ })).toBeInTheDocument()
  })
})

describe("PAR-113 integrity line", () => {
  it("is one quiet line in the head with Check now, not a banner", async () => {
    await renderPage()
    const line = document.querySelector<HTMLElement>("[data-slot=journal-integrity]")!
    expect(within(line).getByRole("button", { name: "Check now" })).toBeInTheDocument()
    expect(document.querySelector(".page-banner-info")).toBeNull()
  })

  it("says Record intact · checked HH:MM once it has been checked", async () => {
    await renderPage()
    api.mockResolvedValue(ok({ ok: true, count: 12 }))
    fireEvent.click(screen.getByRole("button", { name: "Check now" }))
    expect(await screen.findByText(/Record intact · checked \d{2}:\d{2}/)).toBeInTheDocument()
  })
})

describe("PAR-114 Events card header", () => {
  it("has the record's name, the count and range, the chips, a refresh and Export CSV, with the chart inside the card", async () => {
    await renderPage()
    const card = screen.getByRole("region", { name: "All changes" })
    const head = card.querySelector<HTMLElement>("[data-slot=audit-events-head]")!
    expect(head).toHaveTextContent("All changes")
    expect(within(head).getByTestId("audit-count")).toHaveTextContent("1 event · last 7 d")
    const refresh = within(head).getByRole("button", { name: "Refresh audit log" })
    expect(refresh.textContent?.trim()).toBe("")
    expect(within(head).getByRole("button", { name: "Export audit log to CSV" })).toHaveTextContent("Export CSV")
    // One card: the chart is inside it, above the table.
    expect(card.querySelector("[data-slot=audit-histogram]")).not.toBeNull()
    expect(document.querySelectorAll("[data-slot=audit-histogram]")).toHaveLength(1)
  })

  it("names the table's columns Time, Who, What happened", async () => {
    await renderPage()
    const card = screen.getByRole("region", { name: "All changes" })
    for (const c of ["Time", "Who", "What happened"]) expect(within(card).getByText(c)).toBeInTheDocument()
    expect(within(card).queryByText("Person")).toBeNull()
  })
})
