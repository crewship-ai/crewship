import { describe, it, expect, vi, beforeEach, afterEach } from "vitest"
import { render, screen, fireEvent, waitFor, cleanup } from "@testing-library/react"

const api = vi.fn()
vi.mock("@/lib/api-fetch", () => ({ apiFetch: (...a: unknown[]) => api(...a) }))
let role = "OWNER"
vi.mock("@/hooks/use-workspace", () => ({ useWorkspace: () => ({ workspaceId: "ws", role, loading: false }) }))
vi.mock("@/hooks/use-mobile", () => ({ useIsMobile: () => false }))

import { AuditLogPage } from "../audit-log-page"

const ok = (b: unknown) => ({ ok: true, status: 200, json: async () => b })
const auditCalls = () => api.mock.calls.map(([u]) => String(u)).filter((u) => u.startsWith("/api/v1/audit"))

beforeEach(() => {
  api.mockReset()
  api.mockImplementation(async (u: string) => (u.includes("/members") ? ok([]) : ok({ data: [], pagination: { page: 1, limit: 50, total: 0, total_pages: 1 } })))
  role = "OWNER"
  window.history.replaceState(null, "", "/settings/audit")
})
afterEach(() => cleanup())

// The Audit log as a nested page: the side panel picks the trail and the
// time range, the table is full width, and both live in the URL.
describe("Audit log page", () => {
  it("picks the trail from the side panel and keeps the time range", async () => {
    render(<AuditLogPage />)
    await waitFor(() => expect(auditCalls().length).toBeGreaterThan(0))
    fireEvent.click(screen.getByRole("button", { name: "Last 30 days" }))
    fireEvent.click(screen.getByRole("button", { name: "Keeper" }))
    await waitFor(() => expect(auditCalls().at(-1)).toContain("source=keeper"))
    expect(auditCalls().at(-1)).toContain("date_from=")
    const q = new URLSearchParams(window.location.search)
    expect(q.get("audit_source")).toBe("keeper")
    expect(q.get("audit_range")).toBe("30d")
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
