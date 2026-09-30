// Part C: People & workspaces for an instance admin who belongs to no
// workspace. The lists load without one, a person's profile opens, and the
// per-workspace data actions pick one of that person's workspaces.

import { describe, it, expect, vi, beforeEach } from "vitest"
import { render, screen, fireEvent, waitFor, cleanup, within } from "@testing-library/react"

const h = vi.hoisted(() => ({ apiFetch: vi.fn() }))
vi.mock("sonner", () => ({ toast: { success: vi.fn(), error: vi.fn(), info: vi.fn() } }))
vi.mock("@/lib/api-fetch", () => ({ apiFetch: (...a: unknown[]) => h.apiFetch(...a) }))
vi.mock("@/hooks/use-workspace", () => ({ useWorkspace: () => ({ workspaceId: null, role: null, loading: false }) }))
vi.mock("@/hooks/use-auth", () => ({
  useAuth: () => ({ session: { user: { id: "u-boss", email: "boss@ex.com" } } }),
  useIsInstanceAdmin: () => true,
  useSessionSafe: () => ({ data: { user: { id: "u-boss" } }, status: "authenticated" }),
}))
vi.mock("@/hooks/use-mobile", () => ({ useIsMobile: () => false }))
vi.mock("next/navigation", () => ({ useRouter: () => ({ push: vi.fn(), replace: vi.fn() }), usePathname: () => "/admin/people" }))

import { PeoplePage } from "../people-page"

const FUTURE = new Date(Date.now() + 3 * 86_400_000).toISOString()
const m = (workspace_id: string, name: string, role: string) => ({ member_id: `m-${workspace_id}`, workspace_id, name, slug: name.toLowerCase(), role, joined_at: "2026-09-01T00:00:00Z" })
const USERS = [
  { id: "u-boss", email: "boss@ex.com", full_name: "Boss", created_at: "2026-09-01T00:00:00Z", workspace: null, role: "OWNER",
    memberships: [m("ws-a", "Acme", "OWNER")], active_sessions: 2, instance_admin: true, instance_admin_source: "role" },
  { id: "u-jana", email: "jana@ex.com", full_name: "Jana", created_at: "2026-09-02T00:00:00Z", workspace: null, role: "MEMBER",
    memberships: [m("ws-a", "Acme", "MEMBER")], locked_until: FUTURE, failed_login_count: 50 },
  { id: "u-perko", email: "perko@ex.com", full_name: "Perko", created_at: "2026-09-03T00:00:00Z", workspace: null, role: null,
    memberships: [m("ws-b", "Lab", "OWNER")], setup_link_expires_at: FUTURE },
]
const WORKSPACES = [
  { id: "ws-a", name: "Acme", slug: "acme", created_at: "2026-01-01T00:00:00Z", _count_members: 2, _count_agents: 3, _count_crews: 1, runs_7d: 4, runs_by_day: [1, 0, 0, 1, 0, 2, 0], current: true },
  { id: "ws-b", name: "Lab", slug: "lab", created_at: "2026-06-01T00:00:00Z", _count_members: 1, _count_agents: 0, _count_crews: 0, runs_7d: 0, runs_by_day: [0, 0, 0, 0, 0, 0, 0] },
]

const res = (body: unknown, status = 200) => ({ ok: status < 400, status, json: async () => body, text: async () => "", headers: { get: (k: string) => (k === "X-Admin-Scope" ? "instance" : null) } })
const calls = (method: string, part: string) =>
  h.apiFetch.mock.calls.filter(([u, init]) => String(u).includes(part) && ((init as RequestInit | undefined)?.method ?? "GET") === method)
const body = (method: string, part: string) => JSON.parse(String((calls(method, part)[0][1] as RequestInit).body))

beforeEach(() => {
  cleanup()
  window.history.replaceState(null, "", "/admin/people")
  h.apiFetch.mockReset()
  h.apiFetch.mockImplementation(async (u: string) => {
    const url = String(u)
    if (url === "/api/v1/admin/users") return res(USERS)
    if (url === "/api/v1/admin/workspaces") return res(WORKSPACES)
    if (url.includes("/sessions")) return res({ sessions: [], cli_tokens: [] })
    return res({})
  })
})

describe("People with no workspace", () => {
  it("opens a person's profile, and their sessions load without a workspace", async () => {
    render(<PeoplePage />)
    fireEvent.click(await screen.findByRole("row", { name: /Jana/ }))
    expect(await screen.findByRole("region", { name: "Workspace access" })).toBeInTheDocument()
    await waitFor(() => expect(h.apiFetch).toHaveBeenCalledWith("/api/v1/admin/users/u-jana/sessions"))
  })
})
