import { describe, it, expect, vi, beforeEach, afterEach, beforeAll } from "vitest"
import { render, screen, fireEvent, waitFor, cleanup, within } from "@testing-library/react"

import { AuditLogPage } from "../../audit-log/audit-log-page"

const apiFetch = vi.fn()
vi.mock("@/lib/api-fetch", () => ({ apiFetch: (...args: unknown[]) => apiFetch(...args) }))
vi.mock("@/hooks/use-workspace", () => ({ useWorkspace: () => ({ workspaceId: "ws-1", role: "OWNER", loading: false }) }))
vi.mock("@/hooks/use-mobile", () => ({ useIsMobile: () => false }))

const json = (body: unknown, status = 200) => ({ ok: status < 400, status, json: async () => body })
const LOG = { id: "l1", action: "crew.update", entity_type: "CREW", entity_id: "c1", entity_name: "Ops", metadata: null, ip_address: null, user_agent: null, user: { id: "u1", email: "demo@crewship.ai", full_name: "Demo User" }, created_at: "2026-09-28T10:00:00Z" }

function route(url: string) {
  if (url.includes("/members")) return json([{ user: { id: "u1", email: "demo@crewship.ai", full_name: "Demo User" } }])
  return json({ data: [LOG], pagination: { page: 1, limit: 50, total: 1, total_pages: 1 } })
}
const auditCalls = () => apiFetch.mock.calls.map(([u]) => String(u)).filter((u) => u.includes("/api/v1/audit"))

beforeAll(() => { Element.prototype.scrollIntoView = vi.fn() })

// The audit log is searched and filtered on the server, and the filters live
// in the URL.
describe("Audit log filters", () => {
  beforeEach(() => {
    apiFetch.mockReset()
    apiFetch.mockImplementation(async (u: string) => route(u))
    window.history.replaceState(null, "", "/settings/audit")
  })
  afterEach(() => cleanup())

  it("searches the whole record on the server and writes it in the URL", async () => {
    render(<AuditLogPage />)
    await screen.findByText("Ops")
    fireEvent.change(screen.getByRole("textbox", { name: /search the audit log/i }), { target: { value: "role" } })
    await waitFor(() => expect(auditCalls().some((u) => u.includes("search=role"))).toBe(true))
    expect(new URLSearchParams(window.location.search).get("audit_q")).toBe("role")
  })

  it("restores filters from the URL and shows them as removable chips", async () => {
    window.history.replaceState(null, "", "/settings/audit?audit_user=u1&audit_range=custom&audit_from=2026-09-01&audit_to=2026-09-10")
    render(<AuditLogPage />)
    await screen.findByText("Ops")
    const first = auditCalls().find((u) => u.includes("limit=50"))!
    expect(first).toContain("user_id=u1")
    expect(first).toContain("date_to=2026-09-11T00%3A00%3A00.000Z")
    expect(await screen.findByRole("button", { name: /remove filter who: demo user/i })).toBeTruthy()
    fireEvent.click(screen.getByRole("button", { name: /clear filters/i }))
    await waitFor(() => expect(auditCalls().filter((u) => u.includes("limit=50")).at(-1)).not.toContain("user_id"))
    expect(new URLSearchParams(window.location.search).get("audit_user")).toBeNull()
  })

  it("picks a person in the Filter's Who facet, with their count and All N…", async () => {
    apiFetch.mockImplementation(async (u: string) => {
      if (u.includes("/members")) return json(Array.from({ length: 8 }, (_, i) => ({ user: { id: `u${i + 1}`, email: `p${i + 1}@x.io`, full_name: `Person ${i + 1}` } })))
      return json({ data: [{ ...LOG, created_at: new Date().toISOString() }], pagination: { page: 1, limit: 100, total: 1, total_pages: 1 } })
    })
    render(<AuditLogPage />)
    await screen.findByText("Ops")
    fireEvent.click(screen.getByRole("button", { name: /^Filter/ }))
    const block = document.querySelector<HTMLElement>("[data-slot=facet-block]")!
    await waitFor(() => expect(within(block).getByRole("button", { name: /^Who/ })).toBeInTheDocument())
    fireEvent.click(within(block).getByRole("button", { name: /^Who/ }))
    // The busiest person first with their count; the rest behind "All 8…".
    const group = within(block).getByRole("group", { name: "Who" })
    await waitFor(() => expect(within(group).getAllByRole("button", { pressed: false })[0]).toHaveTextContent(/Person 1\s*1/))
    expect(within(group).getByRole("button", { name: "All 8…" })).toBeInTheDocument()
    fireEvent.click(within(group).getAllByRole("button", { pressed: false })[0])
    await waitFor(() => expect(auditCalls().filter((u) => u.includes("limit=50")).at(-1)).toContain("user_id=u1"))
  })

  it("does not offer search or Filter on a record that filters by time only", async () => {
    render(<AuditLogPage />)
    await screen.findByText("Ops")
    fireEvent.click(screen.getByRole("tab", { name: /keeper decisions/i }))
    await waitFor(() => expect(auditCalls().some((u) => u.includes("source=keeper"))).toBe(true))
    expect(screen.getByRole("textbox", { name: /search the audit log/i }).closest("div")).toHaveClass("pointer-events-none")
    expect(screen.queryByRole("button", { name: /^Filter/ })).toBeNull()
    expect(screen.getByText(/filters by time only/i)).toBeTruthy()
  })

  it("shows the event as a verb pill and the actor by name", async () => {
    render(<AuditLogPage />)
    expect((await screen.findAllByText("Demo User")).length).toBeGreaterThan(0)
    expect(screen.getByText("updated").closest("[data-slot='status-pill']")).not.toBeNull()
  })
})
