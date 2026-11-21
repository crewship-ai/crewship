// Shared by the People & workspaces page tests: a small instance, and an
// apiFetch that answers it the way the admin routes do.
import { vi } from "vitest"

export const FUTURE = new Date(Date.now() + 3 * 86_400_000).toISOString()
export const m = (workspace_id: string, name: string, role: string) =>
  ({ member_id: `m-${workspace_id}`, workspace_id, name, slug: name.toLowerCase(), role, joined_at: "2026-09-01T00:00:00Z" })

export const USERS = [
  { id: "u-boss", email: "boss@ex.com", full_name: "Boss", created_at: "2026-09-01T00:00:00Z", workspace: null, role: "OWNER",
    memberships: [m("ws-a", "Acme", "OWNER")], active_sessions: 2, last_active_at: "2026-11-18T00:00:00Z", instance_admin: true, instance_admin_source: "role" },
  { id: "u-jana", email: "jana@ex.com", full_name: "Jana", created_at: "2026-09-02T00:00:00Z", workspace: null, role: "MEMBER",
    memberships: [m("ws-a", "Acme", "MEMBER")], locked_until: FUTURE, failed_login_count: 50, last_active_at: "2026-11-01T00:00:00Z" },
  { id: "u-perko", email: "perko@ex.com", full_name: "Perko", created_at: "2026-09-03T00:00:00Z", workspace: null, role: null,
    memberships: [m("ws-b", "Lab", "OWNER")], setup_link_expires_at: FUTURE },
]
export const WORKSPACES = [
  { id: "ws-a", name: "Acme", slug: "acme", created_at: "2026-01-01T00:00:00Z", _count_members: 2, _count_agents: 3, _count_crews: 1, runs_7d: 4, runs_by_day: [1, 0, 0, 1, 0, 2, 0], current: true, kind: "team", pending_invitations: 2, cost_30d_usd: 12.4 },
  { id: "ws-b", name: "Lab", slug: "lab", created_at: "2026-06-01T00:00:00Z", _count_members: 1, _count_agents: 0, _count_crews: 0, runs_7d: 0, runs_by_day: [0, 0, 0, 0, 0, 0, 0], kind: "team" },
]

export const res = (body: unknown, status = 200) => ({
  ok: status < 400, status, json: async () => body, text: async () => "",
  headers: { get: (k: string) => (k === "X-Admin-Scope" ? "instance" : null) },
})

export interface Data { users: unknown[]; workspaces: unknown[]; audit?: unknown[]; verify?: unknown; overrides?: (url: string, method: string) => unknown | undefined }

/** An apiFetch that answers the lists, sessions, audit and every instance write. */
export function serve(apiFetch: ReturnType<typeof vi.fn>, data: Data = { users: USERS, workspaces: WORKSPACES }) {
  apiFetch.mockReset()
  apiFetch.mockImplementation(async (u: string, init?: RequestInit) => {
    const url = String(u)
    const method = init?.method ?? "GET"
    const o = data.overrides?.(url, method)
    if (o !== undefined) return o
    if (url.startsWith("/api/v1/admin/users?")) return res(data.users)
    if (url.startsWith("/api/v1/admin/workspaces?")) return res(data.workspaces)
    if (url.includes("/sessions")) return res({ sessions: [], cli_tokens: [] })
    if (url.startsWith("/api/v1/admin/instance/audit/verify")) return res(data.verify ?? { ok: true, count: 3 })
    if (url.startsWith("/api/v1/admin/instance/audit")) return res(data.audit ?? [])
    if (url === "/api/v1/admin/instance/people" && method === "POST") return res({ user_id: "u-new", email: "new@ex.com", memberships: [], setup_url: "https://x/reset-password?token=abc", expires_at: FUTURE }, 201)
    if (url.endsWith("/setup-link") && method === "POST") return res({ setup_url: "https://x/reset-password?token=link", expires_at: FUTURE })
    if (url === "/api/v1/admin/instance/workspaces" && method === "POST") return res({ id: "ws-new" }, 201)
    if (url.includes("/admin/instance/")) return res({}, method === "DELETE" ? 204 : 200)
    return res({})
  })
}

export const calls = (apiFetch: ReturnType<typeof vi.fn>, method: string, part: string) =>
  apiFetch.mock.calls.filter(([u, init]) => String(u).includes(part) && ((init as RequestInit | undefined)?.method ?? "GET") === method)
export const body = (apiFetch: ReturnType<typeof vi.fn>, method: string, part: string) =>
  JSON.parse(String((calls(apiFetch, method, part)[0][1] as RequestInit).body))

/** jsdom lacks what cmdk's list calls; environment only. */
export function stubDom() {
  Element.prototype.scrollIntoView ??= vi.fn()
  globalThis.ResizeObserver ??= class { observe() {} unobserve() {} disconnect() {} } as unknown as typeof ResizeObserver
}
