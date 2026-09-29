import { describe, it, expect, vi, beforeEach, afterEach } from "vitest"
import { render, screen, fireEvent, waitFor, cleanup } from "@testing-library/react"

import { CrewAuditSection } from "../crew-audit-section"

const apiFetch = vi.fn()
vi.mock("@/lib/api-fetch", () => ({ apiFetch: (...args: unknown[]) => apiFetch(...args) }))

const json = (body: unknown, status = 200) => ({ ok: status < 400, status, json: async () => body })
const LOG = { id: "l1", action: "crew.update", entity_type: "CREW", entity_id: "c1", entity_name: "Ops", metadata: null, ip_address: null, user_agent: null, user: { id: "u1", email: "demo@crewship.ai", full_name: "Demo User" }, created_at: "2026-09-28T10:00:00Z" }

function route(url: string) {
  if (url.includes("/members")) return json([{ user: { id: "u1", email: "demo@crewship.ai", full_name: "Demo User" } }])
  return json({ data: [LOG], pagination: { page: 1, limit: 50, total: 1, total_pages: 1 } })
}
const auditCalls = () => apiFetch.mock.calls.map(([u]) => String(u)).filter((u) => u.includes("/api/v1/audit"))

// The audit log is searched and filtered on the server, and the filters live
// in the URL — "Filter this page…" used to search only the 50 loaded rows.
describe("Audit log filters", () => {
  beforeEach(() => {
    apiFetch.mockReset()
    apiFetch.mockImplementation(async (u: string) => route(u))
    window.history.replaceState(null, "", "/settings?tab=audit")
  })
  afterEach(() => cleanup())

  it("searches the whole trail on the server and records it in the URL", async () => {
    render(<CrewAuditSection workspaceId="ws-1" />)
    await screen.findByText("Ops")
    fireEvent.change(screen.getByRole("searchbox", { name: /search the audit log/i }), { target: { value: "role" } })
    await waitFor(() => expect(auditCalls().some((u) => u.includes("search=role"))).toBe(true))
    expect(new URLSearchParams(window.location.search).get("audit_q")).toBe("role")
    expect(new URLSearchParams(window.location.search).get("tab")).toBe("audit")
  })

  it("restores filters from the URL and shows them as removable chips", async () => {
    window.history.replaceState(null, "", "/settings?tab=audit&audit_user=u1&audit_range=custom&audit_from=2026-09-01&audit_to=2026-09-10")
    render(<CrewAuditSection workspaceId="ws-1" />)
    await screen.findByText("Ops")
    const first = auditCalls()[0]
    expect(first).toContain("user_id=u1")
    expect(first).toContain("date_to=2026-09-11T00%3A00%3A00.000Z")
    expect(await screen.findByRole("button", { name: /remove filter person: demo user/i })).toBeTruthy()
    fireEvent.click(screen.getByRole("button", { name: /clear all/i }))
    await waitFor(() => expect(auditCalls().at(-1)).not.toContain("user_id"))
    expect(new URLSearchParams(window.location.search).get("audit_user")).toBeNull()
  })

  it("does not offer search or person on a trail that filters by time only", async () => {
    render(<CrewAuditSection workspaceId="ws-1" />)
    await screen.findByText("Ops")
    fireEvent.click(screen.getByRole("button", { name: /keeper/i }))
    await waitFor(() => expect(auditCalls().some((u) => u.includes("source=keeper"))).toBe(true))
    expect(screen.queryByRole("searchbox", { name: /search the audit log/i })).toBeNull()
    expect(screen.getByText(/narrowed by time only/i)).toBeTruthy()
  })

  it("shows the event as a verb pill and the actor by name", async () => {
    render(<CrewAuditSection workspaceId="ws-1" />)
    expect(await screen.findByText("Demo User")).toBeTruthy()
    expect(screen.getByText("updated").closest("[data-slot='status-pill']")).not.toBeNull()
  })
})
