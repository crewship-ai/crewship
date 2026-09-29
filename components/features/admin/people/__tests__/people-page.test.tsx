// Admin › People & workspaces as an instance admin uses it: one page for who
// can sign in and where they work. Workspaces and Users were two tabs with a
// drawer each, and the person drawer could not give access to a workspace the
// admin was not standing in. Every write here goes to /admin/instance/… and is
// asserted by the request it sends, not by the component's own state.

import { describe, it, expect, vi, beforeEach } from "vitest"
import { render, screen, fireEvent, waitFor, cleanup, within } from "@testing-library/react"

const h = vi.hoisted(() => ({ apiFetch: vi.fn() }))
vi.mock("sonner", () => ({ toast: { success: vi.fn(), error: vi.fn(), info: vi.fn() } }))
vi.mock("@/lib/api-fetch", () => ({ apiFetch: (...a: unknown[]) => h.apiFetch(...a) }))
vi.mock("@/hooks/use-workspace", () => ({ useWorkspace: () => ({ workspaceId: "ws-a", role: "OWNER", loading: false }) }))
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
    memberships: [m("ws-a", "Acme", "OWNER")], active_sessions: 2, instance_admin: true, instance_admin_source: "oldest_workspace_owner" },
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
  h.apiFetch.mockImplementation(async (u: string, init?: RequestInit) => {
    const url = String(u)
    const method = init?.method ?? "GET"
    if (url.startsWith("/api/v1/admin/users?")) return res(USERS)
    if (url.startsWith("/api/v1/admin/workspaces?")) return res(WORKSPACES)
    if (url.includes("/sessions")) return res({ sessions: [], cli_tokens: [] })
    if (url === "/api/v1/admin/instance/people" && method === "POST") return res({ user_id: "u-new", email: "new@ex.com", memberships: [], setup_url: "https://x/reset-password?token=abc", expires_at: FUTURE }, 201)
    if (url === "/api/v1/admin/instance/workspaces" && method === "POST") return res({ id: "ws-new" }, 201)
    if (url.includes("/admin/instance/")) return res({}, method === "DELETE" ? 204 : 200)
    return res({})
  })
})

const openPerson = async (name: string) => {
  fireEvent.click(await screen.findByRole("row", { name: new RegExp(name) }))
  return screen.findByRole("region", { name: "Workspace access" })
}

describe("the People list", () => {
  it("lists everyone with their workspaces, the ones needing a hand first", async () => {
    render(<PeoplePage />)
    const table = await screen.findByRole("region", { name: "People" })
    const rows = within(table).getAllByRole("row").slice(1).map((r) => r.textContent ?? "")
    expect(rows[0]).toMatch(/Jana|Perko/)
    expect(rows.at(-1)).toMatch(/Boss/)
    expect(within(table).getByText("Setup pending")).toBeInTheDocument()
    expect(screen.getByRole("list", { name: "Needs attention" })).toHaveTextContent("Locked after 50 failed sign-ins")
  })

  it("narrows to one group from the panel", async () => {
    render(<PeoplePage />)
    await screen.findByRole("region", { name: "People" })
    fireEvent.click(screen.getByRole("button", { name: /^Locked/ }))
    const table = screen.getByRole("region", { name: "People" })
    expect(within(table).getAllByRole("row")).toHaveLength(2)
    expect(within(table).getByText("Jana")).toBeInTheDocument()
  })

  it("unlocks someone straight from the attention card", async () => {
    render(<PeoplePage />)
    const strip = await screen.findByRole("list", { name: "Needs attention" })
    fireEvent.click(within(strip).getByRole("button", { name: "Unlock" }))
    await waitFor(() => expect(calls("POST", "/api/v1/admin/users/u-jana/unlock")).toHaveLength(1))
  })
})

describe("a person's workspace access", () => {
  it("adds them to a workspace they are not in, even one the admin is not in", async () => {
    render(<PeoplePage />)
    const card = await openPerson("Jana")
    fireEvent.click(within(card).getByRole("button", { name: /Add Jana to a workspace/ }))
    fireEvent.change(within(card).getByRole("combobox", { name: "Role" }), { target: { value: "ADMIN" } })
    fireEvent.click(within(card).getByRole("button", { name: "Add" }))
    await waitFor(() => expect(calls("PUT", "/api/v1/admin/instance/workspaces/ws-b/members/u-jana")).toHaveLength(1))
    expect(body("PUT", "/api/v1/admin/instance/workspaces/ws-b/members/u-jana")).toEqual({ role: "ADMIN" })
  })

  it("changes a role in place and removes only after asking", async () => {
    render(<PeoplePage />)
    const card = await openPerson("Jana")
    fireEvent.change(within(card).getByRole("combobox", { name: "Role in Acme" }), { target: { value: "MANAGER" } })
    await waitFor(() => expect(body("PUT", "/api/v1/admin/instance/workspaces/ws-a/members/u-jana")).toEqual({ role: "MANAGER" }))

    fireEvent.click(within(card).getByRole("button", { name: "Remove from Acme" }))
    expect(calls("DELETE", "/members/u-jana")).toHaveLength(0)
    fireEvent.click(within(card).getByRole("button", { name: "Remove" }))
    await waitFor(() => expect(calls("DELETE", "/api/v1/admin/instance/workspaces/ws-a/members/u-jana")).toHaveLength(1))
  })

  it("keeps a workspace's only owner where they are", async () => {
    render(<PeoplePage />)
    const card = await openPerson("Perko")
    expect(within(card).queryByRole("combobox", { name: "Role in Lab" })).toBeNull()
    expect(within(card).getByRole("button", { name: "Remove from Lab" })).toBeDisabled()
  })

  it("issues a new setup link for someone who has not signed in yet", async () => {
    h.apiFetch.mockImplementationOnce(async () => res(USERS)).mockImplementationOnce(async () => res(WORKSPACES))
    render(<PeoplePage />)
    await openPerson("Perko")
    fireEvent.click(screen.getByRole("button", { name: "New setup link" }))
    await waitFor(() => expect(calls("POST", "/api/v1/admin/instance/people/u-perko/setup-link")).toHaveLength(1))
  })
})

describe("suspending", () => {
  it("asks in place, then suspends", async () => {
    render(<PeoplePage />)
    await openPerson("Jana")
    const zone = screen.getByRole("region", { name: "Danger zone" })
    fireEvent.click(within(zone).getByRole("button", { name: /Suspend/ }))
    expect(calls("POST", "/suspend")).toHaveLength(0)
    fireEvent.click(within(zone).getByRole("button", { name: "Suspend Jana" }))
    await waitFor(() => expect(calls("POST", "/api/v1/admin/instance/people/u-jana/suspend")).toHaveLength(1))
  })

  it("offers no way to suspend yourself", async () => {
    render(<PeoplePage />)
    await openPerson("Boss")
    expect(within(screen.getByRole("region", { name: "Danger zone" })).getByRole("button", { name: /Suspend/ })).toBeDisabled()
  })
})

describe("adding", () => {
  it("creates a person with access and shows the setup link once", async () => {
    render(<PeoplePage />)
    await screen.findByRole("region", { name: "People" })
    fireEvent.click(screen.getByRole("button", { name: /Add person/ }))
    fireEvent.change(screen.getByLabelText("Email"), { target: { value: "new@ex.com" } })
    fireEvent.change(screen.getByRole("combobox", { name: "Role in Lab" }), { target: { value: "MEMBER" } })
    fireEvent.click(screen.getByRole("button", { name: "Create and get link" }))
    await waitFor(() => expect(calls("POST", "/api/v1/admin/instance/people")).toHaveLength(1))
    expect(body("POST", "/api/v1/admin/instance/people")).toEqual({ email: "new@ex.com", full_name: "", memberships: [{ workspace_id: "ws-b", role: "MEMBER" }] })
    expect(await screen.findByLabelText("Setup link")).toHaveValue("https://x/reset-password?token=abc")
  })

  it("creates a workspace owned by the person chosen", async () => {
    render(<PeoplePage />)
    await screen.findByRole("region", { name: "People" })
    fireEvent.click(screen.getAllByRole("button", { name: /New workspace/ })[0])
    fireEvent.change(screen.getByLabelText("Name"), { target: { value: "Marketing Team" } })
    expect(screen.getByLabelText("Slug")).toHaveValue("marketing-team")
    fireEvent.change(screen.getByLabelText("Owner"), { target: { value: "u-jana" } })
    fireEvent.click(screen.getByRole("button", { name: "Create workspace" }))
    await waitFor(() => expect(body("POST", "/api/v1/admin/instance/workspaces")).toEqual({ name: "Marketing Team", slug: "marketing-team", owner_user_id: "u-jana" }))
  })
})

describe("a workspace", () => {
  it("hands itself over after asking, and deletes only once its slug is typed", async () => {
    render(<PeoplePage />)
    fireEvent.click(await screen.findByRole("button", { name: /^Acme/ }))
    const zone = await screen.findByRole("region", { name: "Danger zone" })
    fireEvent.change(within(zone).getByRole("combobox", { name: "New owner" }), { target: { value: "u-jana" } })
    fireEvent.click(within(zone).getByRole("button", { name: "Transfer" }))
    fireEvent.click(within(zone).getAllByRole("button", { name: "Transfer" }).at(-1)!)
    await waitFor(() => expect(body("POST", "/api/v1/admin/instance/workspaces/ws-a/transfer-ownership")).toEqual({ user_id: "u-jana" }))

    fireEvent.click(within(zone).getByRole("button", { name: /Delete…/ }))
    const go = within(zone).getByRole("button", { name: "Delete Acme" })
    expect(go).toBeDisabled()
    fireEvent.change(within(zone).getByLabelText(/Type/), { target: { value: "acme" } })
    fireEvent.click(go)
    await waitFor(() => expect(body("DELETE", "/api/v1/admin/instance/workspaces/ws-a")).toEqual({ confirm_slug: "acme" }))
  })
})

describe("the access matrix", () => {
  it("takes access away from a cell", async () => {
    render(<PeoplePage />)
    await screen.findByRole("region", { name: "People" })
    fireEvent.click(screen.getByRole("button", { name: /^Access$/ }))
    const matrix = await screen.findByRole("region", { name: "Access" })
    // The only owner's cell cannot be opened; everyone else's can.
    expect(within(matrix).getByRole("button", { name: "Perko in Lab: Owner" })).toBeDisabled()
    fireEvent.click(within(matrix).getByRole("button", { name: "Jana in Acme: Member" }))
    fireEvent.change(within(matrix).getByRole("combobox", { name: "Jana in Acme" }), { target: { value: "" } })
    await waitFor(() => expect(calls("DELETE", "/api/v1/admin/instance/workspaces/ws-a/members/u-jana")).toHaveLength(1))
  })
})
