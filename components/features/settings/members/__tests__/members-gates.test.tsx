import { describe, it, expect, vi, beforeAll, beforeEach, afterEach } from "vitest"
import { render, screen, cleanup, waitFor, within, fireEvent } from "@testing-library/react"
import { QueryClient, QueryClientProvider } from "@tanstack/react-query"

/**
 * Settings › Members — who gets which control. The server gates at three
 * tiers (internal/api/router_crews.go) and the page draws the same lines or
 * hands out controls that 403:
 *
 *   everyone   the roster: Person, Role, Joined. Read-only.
 *   MANAGER+   the Invited view; role changes down the ladder.
 *   ADMIN+     abilities (never your own, never an Owner's), reach, "Needs a
 *              look", the Filter facets, Add member, remove (never an
 *              Owner), invitation reissue and revoke (an Admin invitation:
 *              the Owner only), bulk abilities, ability columns in the export.
 *
 * `capabilities` routes take the USER id; role / remove / access routes take
 * the MEMBERSHIP id.
 */

const api = vi.fn()
vi.mock("@/lib/api-fetch", () => ({ apiFetch: (...a: unknown[]) => api(...a) }))
vi.mock("sonner", () => ({ toast: { success: vi.fn(), error: vi.fn(), info: vi.fn(), warning: vi.fn() } }))
vi.mock("@/components/features/members/invite-member-dialog", () => ({ InviteMemberDialog: () => <button type="button">Add member</button> }))
const h = vi.hoisted(() => ({ role: "ADMIN" as string | null }))
vi.mock("@/hooks/use-workspace", () => ({ useWorkspace: () => ({ workspaceId: "ws1", role: h.role, loading: false }) }))
vi.mock("@/hooks/use-auth", () => ({ useAuth: () => ({ session: { user: { id: "u-caller", name: "Caller", email: "c@x.io" } } }) }))
vi.mock("@/hooks/use-mobile", () => ({ useIsMobile: () => false }))
vi.mock("next/navigation", () => ({
  useRouter: () => ({ push: vi.fn(), replace: vi.fn(), prefetch: vi.fn(), back: vi.fn() }),
  useSearchParams: () => new URLSearchParams(window.location.search),
  usePathname: () => "/settings/members",
}))

import { MembersPage } from "../members-page"

beforeAll(() => {
  Element.prototype.scrollIntoView = vi.fn()
  Element.prototype.hasPointerCapture = vi.fn(() => false)
  Element.prototype.setPointerCapture = vi.fn()
  Element.prototype.releasePointerCapture = vi.fn()
})

const member = (id: string, role: string, userId: string, name: string) => ({ id, role, created_at: "2026-03-03T10:00:00Z", user: { id: userId, email: `${userId}@x.io`, full_name: name, avatar_url: null } })
const ROSTER = [
  member("m-olive", "OWNER", "u-olive", "Olive Owner"),
  member("m-ada", "ADMIN", "u-ada", "Ada Admin"),
  member("m-mo", "MANAGER", "u-mo", "Mo Manager"),
  member("m-mel", "MEMBER", "u-mel", "Mel Member"),
  member("m-vic", "VIEWER", "u-vic", "Vic Viewer"),
]
const CAPS = { members: ROSTER.map((m) => ({ user_id: m.user.id, role: m.role, capabilities: m.user.id === "u-mel" ? ["chat", "skill.create"] : ["chat"] })) }
const ok = (b: unknown) => ({ ok: true, status: 200, json: async () => b, text: async () => "" })
const future = (days: number) => new Date(Date.now() + days * 86_400_000).toISOString()
const INVITES = [
  { id: "i-m", email: "member@x.io", role: "MEMBER", status: "pending", expires_at: future(5), created_at: new Date().toISOString() },
  { id: "i-a", email: "admin@x.io", role: "ADMIN", status: "pending", expires_at: future(5), created_at: new Date().toISOString() },
]

beforeEach(() => {
  api.mockReset()
  api.mockImplementation(async (url: string) => {
    if (url.includes("/members/capabilities")) return ok(CAPS)
    if (url.includes("/members?")) return ok(ROSTER)
    if (url.includes("/invitations")) return ok(INVITES)
    if (url.includes("/reveal-policy")) return ok({ enabled: false })
    const access = /\/members\/([^/]+)\/access(\/effective)?\?/.exec(url)
    if (access) return ok(access[2] ? { membership_id: access[1], revision: 1, explanation: "", account_restricted: false, decisions: [] } : { membership_id: access[1], revision: 1, mode: "trusted", rights: [] })
    return ok({})
  })
  h.role = "ADMIN"
})
afterEach(() => cleanup())

function renderPage(url = "/settings/members") {
  window.history.replaceState(null, "", url)
  render(<QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}><MembersPage /></QueryClientProvider>)
}
const roster = () => screen.findByRole("table", { name: "People and their roles" })
const panel = () => document.querySelector<HTMLElement>("[data-panel-id=drill-members]")!
const sheet = () => waitFor(() => {
  const el = document.querySelector<HTMLElement>("[data-slot=member-detail]")
  if (!el) throw new Error("no person open")
  return el
})
const called = (part: string) => api.mock.calls.some(([u]) => String(u).includes(part))

const TIERS: { role: string; admin: boolean; manager: boolean }[] = [
  { role: "OWNER", admin: true, manager: true },
  { role: "ADMIN", admin: true, manager: true },
  { role: "MANAGER", admin: false, manager: true },
  { role: "MEMBER", admin: false, manager: false },
  { role: "VIEWER", admin: false, manager: false },
]

describe.each(TIERS)("the roster for $role", ({ role, admin, manager }) => {
  it(`${admin ? "offers" : "withholds"} abilities, selection, the Filter, Needs a look and Add member; ${manager ? "offers" : "withholds"} Invited`, async () => {
    h.role = role
    renderPage()
    const table = await roster()
    // Person, Role and Joined are everyone's.
    const heads = within(table).getAllByRole("columnheader").map((t) => t.textContent).filter(Boolean)
    expect(heads).toEqual(admin ? ["Person", "Role", "Abilities", "Joined", "Open"] : ["Person", "Role", "Joined", "Open"])
    expect(screen.queryAllByRole("checkbox", { name: /^Select / }).length > 0).toBe(admin)
    expect(screen.queryByRole("button", { name: /^Filter/ }) !== null).toBe(admin)
    expect(within(panel()).queryByText("Needs a look") !== null).toBe(admin)
    expect(within(panel()).queryByText("Invited") !== null).toBe(manager)
    expect(screen.queryByRole("button", { name: "Add member" }) !== null).toBe(admin)
    // Nothing is fetched that the role may not read.
    await waitFor(() => expect(called("/members?")).toBe(true))
    expect(called("/members/capabilities")).toBe(admin)
    expect(called("/invitations")).toBe(manager)
    expect(document.querySelector("[data-slot=abilities-line]") !== null).toBe(admin)
    expect(document.querySelector("[data-slot=members-look-banner]") !== null).toBe(admin)
    // Below Admin the page says who changes things.
    expect(document.querySelector("[data-slot=members-note]") !== null).toBe(!admin)
  })

  it(`${admin ? "opens" : "never opens"} the Needs a look view`, async () => {
    h.role = role
    renderPage("/settings/members?status=look")
    if (admin) {
      await waitFor(() => expect(document.querySelector("[data-slot=members-look]")).not.toBeNull())
      expect(await screen.findByText(/Member · can create skills/)).toBeInTheDocument()
    } else {
      // A view a role may not open is the roster, not an empty card.
      expect(await roster()).toBeInTheDocument()
      expect(document.querySelector("[data-slot=members-look]")).toBeNull()
    }
  })
})

describe("the person sheet, by caller", () => {
  const open = async (userId: string) => {
    renderPage(`/settings/members?member=${userId}`)
    return sheet()
  }
  const radios = (root: HTMLElement) => within(within(root).getByRole("radiogroup", { name: /^Role for/ })).getAllByRole("radio")

  it("an OWNER sets any role below Owner, never their own, and removes anyone but an Owner", async () => {
    h.role = "OWNER"
    const d = await open("u-ada")
    for (const r of radios(d)) expect(r).toBeEnabled()
    expect(within(d).getByRole("region", { name: "Abilities" })).toBeInTheDocument()
    expect(within(d).getByRole("region", { name: "Reach" })).toBeInTheDocument()
    expect(within(d).getByRole("button", { name: "Remove member" })).toBeInTheDocument()
    cleanup()
    const o = await open("u-olive")
    expect(within(o).getByText(/An owner’s role is fixed/)).toBeInTheDocument()
    expect(within(o).queryByRole("radiogroup", { name: /^Role for/ })).toBeNull()
    expect(within(o).queryByRole("button", { name: "Remove member" })).toBeNull()
  })

  it("an ADMIN grants roles below Admin, and only an Owner makes an Admin", async () => {
    h.role = "ADMIN"
    const d = await open("u-mel")
    const group = within(d).getByRole("radiogroup", { name: /^Role for/ })
    expect(within(group).getByRole("radio", { name: "Admin" })).toBeDisabled()
    expect(within(group).getByRole("radio", { name: "Manager" })).toBeEnabled()
    expect(within(group).getByRole("radio", { name: "Viewer" })).toBeEnabled()
    expect(within(d).getByText(/Only an Owner makes an Admin/)).toBeInTheDocument()
    expect(within(d).getByRole("region", { name: "Abilities" })).toBeInTheDocument()
    expect(within(d).getByRole("button", { name: "Remove member" })).toBeInTheDocument()
  })

  it("an ADMIN cannot touch an Owner, another Admin or themselves", async () => {
    h.role = "ADMIN"
    const peer = await open("u-ada")
    expect(within(peer).getByText(/Your role cannot change this person’s role/)).toBeInTheDocument()
    expect(within(peer).queryByRole("radiogroup", { name: /^Role for/ })).toBeNull()
  })

  it("a MANAGER changes roles below Manager and nothing else", async () => {
    h.role = "MANAGER"
    const d = await open("u-mel")
    const group = within(d).getByRole("radiogroup", { name: /^Role for/ })
    expect(within(group).getByRole("radio", { name: "Viewer" })).toBeEnabled()
    expect(within(group).getByRole("radio", { name: "Member" })).toBeEnabled()
    expect(within(group).getByRole("radio", { name: "Manager" })).toBeDisabled()
    expect(within(group).getByRole("radio", { name: "Admin" })).toBeDisabled()
    for (const name of ["Abilities", "Reach", "Remove from workspace"]) expect(within(d).queryByRole("region", { name })).toBeNull()
    expect(within(d).queryByRole("button", { name: "Remove member" })).toBeNull()
    expect(called("/access?")).toBe(false)
    expect(called("/members/capabilities")).toBe(false)
  })

  it.each(["MEMBER", "VIEWER"])("a %s sees the role and nothing to change", async (role) => {
    h.role = role
    const d = await open("u-vic")
    expect(within(d).getByText(/Your role cannot change this person’s role/)).toBeInTheDocument()
    expect(within(d).queryByRole("radiogroup")).toBeNull()
    expect(within(d).queryByRole("switch")).toBeNull()
    for (const name of ["Abilities", "Reach", "Remove from workspace"]) expect(within(d).queryByRole("region", { name })).toBeNull()
  })

  it("addresses abilities by USER id and role by MEMBERSHIP id", async () => {
    h.role = "ADMIN"
    const d = await open("u-mel")
    fireEvent.click(await within(d).findByRole("radio", { name: "Chat only" }))
    // Reach reads the MEMBERSHIP's policy.
    await waitFor(() => expect(called("/members/m-mel/access?")).toBe(true))
    expect(called("/members/u-mel/access")).toBe(false)
  })
})

describe("invitations, by caller", () => {
  it("an OWNER reissues and revokes an Admin invitation", async () => {
    h.role = "OWNER"
    renderPage("/settings/members?status=invited")
    const list = await waitFor(() => { const el = document.querySelector<HTMLElement>("[data-slot=members-invitations]"); if (!el) throw new Error("no list"); return el })
    expect(within(list).getByRole("button", { name: "Reissue admin@x.io" })).toBeInTheDocument()
    expect(within(list).getByRole("button", { name: "Revoke admin@x.io" })).toBeInTheDocument()
  })

  it("an ADMIN manages every invitation except an Admin's", async () => {
    h.role = "ADMIN"
    renderPage("/settings/members?status=invited")
    const list = await waitFor(() => { const el = document.querySelector<HTMLElement>("[data-slot=members-invitations]"); if (!el) throw new Error("no list"); return el })
    expect(within(list).getByRole("button", { name: "Reissue member@x.io" })).toBeInTheDocument()
    expect(within(list).queryByRole("button", { name: "Reissue admin@x.io" })).toBeNull()
    expect(within(list).getByText(/Only the owner manages an Admin invitation/)).toBeInTheDocument()
  })

  it("a MANAGER sees the Invited view and nothing to do with it", async () => {
    h.role = "MANAGER"
    renderPage("/settings/members?status=invited")
    const list = await waitFor(() => { const el = document.querySelector<HTMLElement>("[data-slot=members-invitations]"); if (!el) throw new Error("no list"); return el })
    expect(list).toHaveTextContent("member@x.io")
    expect(within(list).queryByRole("button")).toBeNull()
  })

  it.each(["MEMBER", "VIEWER"])("a %s never gets the Invited view", async (role) => {
    h.role = role
    renderPage("/settings/members?status=invited")
    expect(await roster()).toBeInTheDocument()
    expect(document.querySelector("[data-slot=members-invitations]")).toBeNull()
    expect(called("/invitations")).toBe(false)
  })
})
