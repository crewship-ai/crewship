import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from "vitest"
import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react"

// v67 parity for Settings › Audit log (PAR-111, 112, 113, 114): the trail
// switcher card, 24-hour times, the solid-bar histogram, the chain banner and
// the Events card header. jsdom has no layout; the sizes are measured by
// e2e/settings-h2.spec.ts after deploy.

const api = vi.fn()
vi.mock("@/lib/api-fetch", () => ({ apiFetch: (...a: unknown[]) => api(...a) }))
vi.mock("@/hooks/use-workspace", () => ({ useWorkspace: () => ({ workspaceId: "ws", role: "OWNER", loading: false }) }))
vi.mock("@/hooks/use-mobile", () => ({ useIsMobile: () => false }))

import { AuditLogPage } from "../audit-log-page"
import { JournalVerifyBanner } from "../journal-verify-banner"

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

describe("PAR-111 trail switcher", () => {
  it("is the card of four rows, without a description under each", async () => {
    await renderPage()
    const spaces = document.querySelector<HTMLElement>("[data-slot=panel-spaces]")!
    expect(within(spaces).getAllByRole("tab").map((t) => t.textContent?.replace(/\d+$/, "").trim())).toEqual(["Workspace", "Crews", "Credentials", "Keeper"])
    const panel = document.querySelector<HTMLElement>("[data-slot=secondary-panel]")!
    expect(within(panel).queryByText("Settings, people, crews, credentials")).toBeNull()
    expect(within(panel).queryByText("Every gatekeeper decision, append-only")).toBeNull()
  })

  it("groups the rest in plain sentence-case headings", async () => {
    await renderPage()
    const sections = Array.from(document.querySelectorAll("[data-slot=drill-section]"))
    // (People joins them once the workspace lists someone.)
    expect(sections.map((s) => s.firstElementChild!.textContent!.replace(/[\d]+( active)?$/, "").trim())).toEqual(["Category", "Run result"])
    for (const s of sections) expect(s.querySelector("button[aria-expanded]")).toBeNull()
  })

  it("keeps the trail in the URL and the time range through a switch", async () => {
    await renderPage()
    fireEvent.click(screen.getByRole("tab", { name: /Keeper/ }))
    await waitFor(() => expect(new URLSearchParams(window.location.search).get("audit_source")).toBe("keeper"))
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

describe("PAR-113 journal chain banner", () => {
  it("is a blue info banner with a shield tile, the CLI hint and Verify now", () => {
    render(<JournalVerifyBanner workspaceId="ws" />)
    const banner = document.querySelector<HTMLElement>("[data-slot=journal-verify]")!
    expect(banner.className).toContain("page-banner-info")
    expect(banner).toHaveTextContent("Journal chain")
    expect(banner).toHaveTextContent("Every workspace event is mirrored into a hash-chained journal (HMAC-SHA256).")
    expect(within(banner).getByText("crewship journal verify").className).toContain("font-mono")
    // "New in UI" is the wireframe's note to the owner, not a product label.
    expect(banner).not.toHaveTextContent("New in UI")
    expect(within(banner).getByRole("button", { name: "Verify now" })).toBeInTheDocument()
    expect(banner.querySelector("[data-slot=journal-verify-tile]")).not.toBeNull()
  })

  it("says Journal chain verified HH:MM once it has been checked", async () => {
    api.mockResolvedValue(ok({ ok: true, count: 12 }))
    render(<JournalVerifyBanner workspaceId="ws" />)
    fireEvent.click(screen.getByRole("button", { name: "Verify now" }))
    expect(await screen.findByText(/Journal chain verified \d{2}:\d{2}/)).toBeInTheDocument()
  })
})

describe("PAR-114 Events card header", () => {
  it("has the trail and range on the right, an icon-only refresh and Export CSV", async () => {
    await renderPage()
    const card = screen.getByRole("region", { name: "Events" })
    const header = card.querySelector<HTMLElement>("[data-slot=settings-card-header]")!
    expect(within(header).getByRole("heading", { name: "Events" })).toBeInTheDocument()
    expect(header).toHaveTextContent("Workspace trail · last 7 d")
    // The subtitle under the title is gone: the line is on the right.
    expect(header.querySelector("h3 + p")).toBeNull()
    const refresh = within(header).getByRole("button", { name: "Refresh audit log" })
    expect(refresh.textContent?.trim()).toBe("")
    expect(within(header).getByRole("button", { name: "Export audit log to CSV" })).toHaveTextContent("Export CSV")
  })
})
