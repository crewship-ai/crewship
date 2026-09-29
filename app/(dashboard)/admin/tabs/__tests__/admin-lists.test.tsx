// Admin › Workspaces and Users as the admin reads them: figures first, then a
// list that filters and sorts, and a drawer per row with what can be done.

import { describe, it, expect, vi, beforeEach } from "vitest"
import { render, screen, fireEvent, waitFor, cleanup, within } from "@testing-library/react"

import { WorkspacesTab } from "../workspaces-tab"
import { UsersTab, describeAgent } from "../users-tab"
import { ago } from "../admin-kit"

const h = vi.hoisted(() => ({ apiFetch: vi.fn() }))
vi.mock("sonner", () => ({ toast: { success: vi.fn(), error: vi.fn(), info: vi.fn() } }))
vi.mock("@/lib/api-fetch", () => ({ apiFetch: (...args: unknown[]) => h.apiFetch(...args) }))

const now = Date.now()
const iso = (msAgo: number) => new Date(now - msAgo).toISOString()
const ok = (body: unknown) => ({ ok: true, status: 200, json: async () => body, text: async () => "" })

const ORGS = [
  { id: "ws-1", name: "Dess", slug: "dess", created_at: iso(30 * 864e5), updated_at: iso(0), _count_members: 2, _count_agents: 17, _count_crews: 11,
    runs_7d: 11, runs_by_day: [1, 5, 5, 0, 0, 0, 0], cost_30d_usd: 0.4, last_activity_at: iso(120e3), pending_invitations: 1, current: true },
  { id: "ws-2", name: "Engineering Platform", slug: "eng", created_at: iso(90 * 864e5), updated_at: iso(0), _count_members: 5, _count_agents: 22, _count_crews: 9,
    runs_7d: 259, runs_by_day: [40, 52, 61, 38, 12, 9, 47], cost_30d_usd: 182.3, last_activity_at: iso(1e3), allow_privileged_credentials: true },
]
const USERS = [
  { id: "u-1", email: "demo@crewship.ai", full_name: "Demo User", role: "OWNER", created_at: iso(30 * 864e5), workspace: { id: "ws-1", name: "Dess" },
    memberships: [{ member_id: "m-1", workspace_id: "ws-1", name: "Dess", slug: "dess", role: "OWNER", joined_at: iso(30 * 864e5) }],
    last_active_at: iso(60e3), active_sessions: 2, cli_tokens: 1, failed_login_count: 0, email_verified: true },
  { id: "u-2", email: "ondrej@example.com", full_name: "Ondřej Veselý", role: "MEMBER", created_at: iso(10 * 864e5), workspace: { id: "ws-1", name: "Dess" },
    memberships: [{ member_id: "m-2", workspace_id: "ws-1", name: "Dess", slug: "dess", role: "MEMBER", joined_at: iso(10 * 864e5) }],
    last_active_at: iso(40 * 864e5), active_sessions: 1, cli_tokens: 0, failed_login_count: 5, locked_until: new Date(now + 15 * 60e3).toISOString(), email_verified: false },
]

beforeEach(() => {
  cleanup()
  h.apiFetch.mockReset()
  h.apiFetch.mockResolvedValue(ok([]))
})

describe("Admin › Workspaces", () => {
  it("sums the instance in the figures row and says whose list it is", () => {
    render(<WorkspacesTab orgs={ORGS} users={USERS} scope="workspace" license={{ edition: "community", max_crews: 15, max_agents_per_crew: 10, max_members: 5, features: [] }} onRefresh={vi.fn()} />)
    expect(screen.getByText("270")).toBeInTheDocument() // runs 7d across both
    expect(screen.getByText("$182.70")).toBeInTheDocument()
    expect(document.querySelector("[data-slot=admin-scope-note]")).not.toBeNull()
  })

  it("orders by runs by default and filters to the busy ones", () => {
    render(<WorkspacesTab orgs={ORGS} users={USERS} onRefresh={vi.fn()} />)
    const rows = () => within(document.querySelector("[data-slot=admin-workspaces] tbody")!).getAllByRole("row")
    expect(rows()[0]).toHaveTextContent("Engineering Platform")
    fireEvent.click(screen.getByRole("button", { name: /^Busy/ }))
    expect(rows()).toHaveLength(1)
  })

  it("opens a workspace in the drawer with its settings", () => {
    render(<WorkspacesTab orgs={ORGS} users={USERS} onRefresh={vi.fn()} />)
    fireEvent.click(screen.getByRole("button", { name: "Open Engineering Platform" }))
    const drawer = screen.getByRole("dialog")
    expect(within(drawer).getByText("Allowed")).toBeInTheDocument()
    expect(within(drawer).getByText("$182.30")).toBeInTheDocument()
  })
})

describe("Admin › Users", () => {
  it("counts who is online, idle and locked, and filters to the locked", () => {
    render(<UsersTab users={USERS} workspaceId="ws-1" onRefresh={vi.fn()} />)
    fireEvent.click(screen.getByRole("button", { name: /^Locked/ }))
    expect(screen.getByText("Ondřej Veselý")).toBeInTheDocument()
    expect(screen.queryByText("Demo User")).toBeNull()
  })

  it("unlocks a locked account from its drawer", async () => {
    const onRefresh = vi.fn()
    render(<UsersTab users={USERS} workspaceId="ws-1" onRefresh={onRefresh} />)
    fireEvent.click(screen.getByRole("button", { name: "Ondřej Veselý" }))
    h.apiFetch.mockResolvedValue({ ok: true, status: 204, json: async () => ({}), text: async () => "" })
    fireEvent.click(screen.getByRole("button", { name: /unlock now/i }))
    await waitFor(() => expect(h.apiFetch.mock.calls.some(([u, init]) => String(u).includes("/api/v1/admin/users/u-2/unlock") && (init as RequestInit)?.method === "POST")).toBe(true))
    await waitFor(() => expect(onRefresh).toHaveBeenCalled())
  })

  it("lists signed-in devices and signs one out", async () => {
    h.apiFetch.mockImplementation(async (u: string) =>
      String(u).includes("/sessions?") ? ok({
        sessions: [{ id: "s-1", created_at: iso(864e5), last_used_at: iso(60e3), expires_at: iso(-864e5), user_agent: "Mozilla/5.0 (Macintosh; Intel Mac OS X) Chrome/140", ip: "10.0.0.2" }],
        cli_tokens: [{ id: "t-1", name: "laptop", scopes: ["read"], created_at: iso(864e5), last_used_at: null, expires_at: null }],
      }) : ok([]))
    render(<UsersTab users={USERS} workspaceId="ws-1" onRefresh={vi.fn()} />)
    fireEvent.click(screen.getByRole("button", { name: "Demo User" }))
    fireEvent.click(screen.getByRole("tab", { name: /sessions/i }))
    expect(await screen.findByText("Chrome · macOS")).toBeInTheDocument()
    expect(screen.getByText("laptop")).toBeInTheDocument()
    fireEvent.click(screen.getByRole("button", { name: "Sign out" }))
    await waitFor(() => expect(h.apiFetch.mock.calls.some(([u]) => String(u).includes("/sessions/s-1/revoke"))).toBe(true))
  })

  it("changes a role in one workspace through the members API", async () => {
    render(<UsersTab users={USERS} workspaceId="ws-1" onRefresh={vi.fn()} />)
    fireEvent.click(screen.getByRole("button", { name: "Ondřej Veselý" }))
    fireEvent.click(screen.getByRole("tab", { name: /workspaces/i }))
    fireEvent.change(screen.getByLabelText("Role in Dess"), { target: { value: "MANAGER" } })
    await waitFor(() => {
      const call = h.apiFetch.mock.calls.find(([u, init]) => String(u).includes("/api/v1/workspaces/ws-1/members/m-2") && (init as RequestInit)?.method === "PATCH")
      expect(JSON.parse(String((call![1] as RequestInit).body))).toEqual({ role: "MANAGER" })
    })
  })
})

describe("reading times and devices", () => {
  it("says how long ago in words", () => {
    expect(ago(new Date(now - 30e3).toISOString(), now)).toBe("just now")
    expect(ago(new Date(now - 12 * 60e3).toISOString(), now)).toBe("12 min ago")
    expect(ago(new Date(now - 864e5 - 1).toISOString(), now)).toBe("yesterday")
    expect(ago(null, now)).toBe("never")
    // SQLite's datetime('now') text, read as UTC.
    expect(ago(new Date(now - 3 * 3600e3).toISOString().replace("T", " ").slice(0, 19), now)).toBe("3 h ago")
  })

  it("names the browser and system", () => {
    expect(describeAgent("Mozilla/5.0 (iPhone; CPU iPhone OS 18_0 like Mac OS X) Safari/604.1")).toBe("Safari · iOS")
    expect(describeAgent(null)).toBe("Unknown device")
  })
})
