import { describe, it, expect, vi, beforeAll, beforeEach, afterEach } from "vitest"
import { render, screen, fireEvent, waitFor, cleanup, within } from "@testing-library/react"

const api = vi.fn()
vi.mock("@/lib/api-fetch", () => ({ apiFetch: (...a: unknown[]) => api(...a) }))
const ws = vi.hoisted(() => ({
  role: "OWNER" as string,
  workspace: { id: "ws", name: "Acme", audit_log_retention_days: null as number | null, credential_audit_retention_days: null as number | null },
  refresh: vi.fn(async () => {}),
}))
vi.mock("@/hooks/use-workspace", () => ({
  useWorkspace: () => ({ workspaceId: "ws", role: ws.role, loading: false, workspace: ws.workspace }),
  refreshWorkspaceSettings: () => ws.refresh(),
}))
vi.mock("@/hooks/use-mobile", () => ({ useIsMobile: () => false }))
const toastMock = vi.hoisted(() => Object.assign(vi.fn(), { success: vi.fn(), error: vi.fn(), dismiss: vi.fn() }))
vi.mock("@/lib/toast", () => ({ toast: toastMock }))

import { AuditLogPage } from "../audit-log-page"

const ok = (b: unknown) => ({ ok: true, status: 200, json: async () => b })
const page = (rows: unknown[]) => ok({ data: rows, pagination: { page: 1, limit: 50, total: rows.length, total_pages: 1 } })
const event = (id: string, extra: Record<string, unknown> = {}) => ({
  id, action: "crew.update", entity_type: "CREW", entity_id: `crew-${id}`, entity_name: `Crew ${id}`,
  metadata: { field: "name" }, ip_address: "10.0.0.7", user_agent: "Mozilla/5.0 test",
  user: { id: "u1", email: "ada@x.io", full_name: "Ada Admin" }, created_at: new Date().toISOString(), ...extra,
})
const auditCalls = () => api.mock.calls.map(([u]) => String(u)).filter((u) => u.startsWith("/api/v1/audit"))

beforeAll(() => {
  Element.prototype.scrollIntoView = vi.fn()
  Element.prototype.hasPointerCapture = vi.fn(() => false)
  Element.prototype.setPointerCapture = vi.fn()
  Element.prototype.releasePointerCapture = vi.fn()
})
beforeEach(() => {
  api.mockReset()
  api.mockImplementation(async (u: string) => (u.includes("/members") ? ok([]) : page([event("e1")])))
  ws.role = "OWNER"
  ws.workspace = { id: "ws", name: "Acme", audit_log_retention_days: null, credential_audit_retention_days: null }
  toastMock.mockReset()
  window.history.replaceState(null, "", "/settings/audit")
})
afterEach(() => cleanup())

describe("Everything about <entity>", () => {
  it("is offered in an event's detail and filters by entity_id, in the URL and as a chip", async () => {
    render(<AuditLogPage />)
    const row = await screen.findByRole("button", { name: /Crew e1/ })
    fireEvent.click(row)
    const detail = document.getElementById(row.getAttribute("aria-controls")!)!
    fireEvent.click(within(detail).getByRole("button", { name: "Everything about Crew e1" }))
    await waitFor(() => expect(auditCalls().filter((u) => u.includes("limit=50")).at(-1)).toContain("entity_id=crew-e1"))
    expect(new URLSearchParams(window.location.search).get("audit_entity")).toBe("crew-e1")
    const chip = screen.getByRole("button", { name: "Remove filter Everything about Crew e1" })
    fireEvent.click(chip)
    await waitFor(() => expect(auditCalls().filter((u) => u.includes("limit=50")).at(-1)).not.toContain("entity_id"))
    expect(new URLSearchParams(window.location.search).get("audit_entity")).toBeNull()
  })

  it("restores from the URL", async () => {
    window.history.replaceState(null, "", "/settings/audit?audit_entity=crew-e1")
    render(<AuditLogPage />)
    await screen.findByRole("button", { name: /Crew e1/ })
    expect(auditCalls().find((u) => u.includes("limit=50"))).toContain("entity_id=crew-e1")
    expect(screen.getByRole("button", { name: "Remove filter Everything about this item" })).toBeInTheDocument()
  })
})

describe("CSV export", () => {
  it("has metadata and user_agent columns, and names the record", async () => {
    let csv = ""
    const create = vi.fn((blob: Blob) => { void blob.text().then((t) => { csv = t }); return "blob:x" })
    vi.stubGlobal("URL", Object.assign(URL, { createObjectURL: create, revokeObjectURL: vi.fn() }))
    const click = vi.spyOn(HTMLAnchorElement.prototype, "click").mockImplementation(() => {})
    render(<AuditLogPage />)
    await screen.findByRole("button", { name: /Crew e1/ })
    fireEvent.click(screen.getByRole("button", { name: "Export audit log to CSV" }))
    await waitFor(() => expect(click).toHaveBeenCalled())
    await waitFor(() => expect(csv).not.toBe(""))
    const [header, line] = csv.split("\n")
    expect(header).toBe("timestamp,action,entity_type,entity_id,entity_name,user,ip_address,user_agent,metadata")
    expect(line).toContain('"Mozilla/5.0 test"')
    expect(line).toContain('"{""field"":""name""}"')
    click.mockRestore()
  })
})

describe("Change how long", () => {
  const link = () => screen.getByRole("button", { name: "Change how long" })

  it("says what is kept today, with the server's defaults for a window that is not set", async () => {
    render(<AuditLogPage />)
    await screen.findByRole("button", { name: /Crew e1/ })
    expect(screen.getByText(/Kept forever; secret use 90 days\./)).toBeInTheDocument()
  })

  it("saves a longer window with one PATCH of just what changed", async () => {
    api.mockImplementation(async (u: string, init?: { method?: string }) => (init?.method === "PATCH" ? ok({}) : u.includes("/members") ? ok([]) : page([event("e1")])))
    render(<AuditLogPage />)
    await screen.findByRole("button", { name: /Crew e1/ })
    fireEvent.click(link())
    const dialog = await screen.findByRole("dialog")
    // Secret use: 90 d (default) -> Forever is longer, so no confirm.
    const secret = within(dialog).getByRole("radiogroup", { name: "How long Secret use is kept" })
    fireEvent.click(within(secret).getByRole("radio", { name: "Forever" }))
    fireEvent.click(within(dialog).getByRole("button", { name: "Save" }))
    await waitFor(() => expect(api.mock.calls.some(([, i]) => i?.method === "PATCH")).toBe(true))
    const [url, init] = api.mock.calls.find(([, i]) => i?.method === "PATCH")!
    expect(url).toContain("/api/v1/workspaces/ws")
    expect(JSON.parse(init.body)).toEqual({ credential_audit_retention_days: 0 })
    await waitFor(() => expect(ws.refresh).toHaveBeenCalled())
  })

  it("asks before shortening, because older entries are deleted", async () => {
    api.mockImplementation(async (u: string, init?: { method?: string }) => (init?.method === "PATCH" ? ok({}) : u.includes("/members") ? ok([]) : page([event("e1")])))
    render(<AuditLogPage />)
    await screen.findByRole("button", { name: /Crew e1/ })
    fireEvent.click(link())
    const dialog = await screen.findByRole("dialog")
    const all = within(dialog).getByRole("radiogroup", { name: "How long All changes are kept" })
    fireEvent.click(within(all).getByRole("radio", { name: "90 d" }))
    fireEvent.click(within(dialog).getByRole("button", { name: "Save" }))
    const confirm = await screen.findByTestId("confirm-dialog")
    expect(confirm).toHaveTextContent("deleted at the next daily sweep")
    expect(api.mock.calls.some(([, i]) => i?.method === "PATCH")).toBe(false)
    fireEvent.click(within(confirm).getByRole("button", { name: "Shorten and save" }))
    await waitFor(() => expect(api.mock.calls.some(([, i]) => i?.method === "PATCH")).toBe(true))
    expect(JSON.parse(api.mock.calls.find(([, i]) => i?.method === "PATCH")![1].body)).toEqual({ audit_log_retention_days: 90 })
  })

  it("keeps a value set on the command line as its own option", async () => {
    ws.workspace = { ...ws.workspace, audit_log_retention_days: 30 }
    render(<AuditLogPage />)
    await screen.findByRole("button", { name: /Crew e1/ })
    expect(screen.getByText(/Kept 30 days;/)).toBeInTheDocument()
    fireEvent.click(link())
    const dialog = await screen.findByRole("dialog")
    const all = within(dialog).getByRole("radiogroup", { name: "How long All changes are kept" })
    expect(within(all).getByRole("radio", { name: "30 d" })).toHaveAttribute("aria-checked", "true")
  })

  it("Save is off until something changes", async () => {
    render(<AuditLogPage />)
    await screen.findByRole("button", { name: /Crew e1/ })
    fireEvent.click(link())
    const dialog = await screen.findByRole("dialog")
    expect(within(dialog).getByRole("button", { name: "Save" })).toBeDisabled()
  })
})

// The page is ADMIN and up, like GET /api/v1/audit, the verify call and the
// workspace PATCH it uses.
describe("role gate", () => {
  it.each(["OWNER", "ADMIN"])("%s sees the record, Check now, Export and Change how long", async (role) => {
    ws.role = role
    render(<AuditLogPage />)
    await screen.findByRole("button", { name: /Crew e1/ })
    expect(screen.getByRole("button", { name: "Check now" })).toBeInTheDocument()
    expect(screen.getByRole("button", { name: "Export audit log to CSV" })).toBeInTheDocument()
    expect(screen.getByRole("button", { name: "Change how long" })).toBeInTheDocument()
    expect(screen.getByRole("textbox", { name: /search the audit log/i })).toBeInTheDocument()
  })

  it.each(["MANAGER", "MEMBER", "VIEWER"])("%s gets the no-access state and no request", (role) => {
    ws.role = role
    render(<AuditLogPage />)
    expect(document.querySelector("[data-state=no-access]")).not.toBeNull()
    expect(screen.queryByRole("button", { name: "Check now" })).toBeNull()
    expect(screen.queryByRole("button", { name: "Export audit log to CSV" })).toBeNull()
    expect(screen.queryByRole("button", { name: "Change how long" })).toBeNull()
    expect(api).not.toHaveBeenCalledWith(expect.stringContaining("/api/v1/audit"))
    expect(api).not.toHaveBeenCalledWith(expect.stringContaining("/journal/verify"))
  })
})

describe("Check now from the page", () => {
  it("runs the verify call for this workspace and opens the dialog with the answer", async () => {
    render(<AuditLogPage />)
    await screen.findByRole("button", { name: /Crew e1/ })
    api.mockImplementation(async (u: string) => (u.includes("/journal/verify") ? ok({ ok: true, count: 7, checkpoints: 2, continuation_verified: true }) : ok([])))
    fireEvent.click(screen.getByRole("button", { name: "Check now" }))
    const dialog = await screen.findByRole("dialog")
    expect(api).toHaveBeenCalledWith("/api/v1/admin/journal/verify?workspace_id=ws")
    await waitFor(() => expect(dialog).toHaveTextContent("Entries checked"))
    expect(dialog).toHaveTextContent("7")
  })
})
