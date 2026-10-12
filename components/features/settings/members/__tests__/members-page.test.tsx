import { describe, it, expect, vi, beforeAll, beforeEach, afterEach } from "vitest"
import { render, screen, cleanup, fireEvent, waitFor, within } from "@testing-library/react"
import { QueryClient, QueryClientProvider } from "@tanstack/react-query"

/**
 * Settings › Members as its own page (/settings/members).
 *
 * The in-page Settings section this replaced had its behaviour pinned in four
 * files (members-section, member-role-control, members-unified-row and the
 * capability gating test). That coverage moved here, against the new shape:
 *
 *   - the server gates the membership writes at two DIFFERENT tiers
 *     (internal/api/router_crews.go): invite (POST), remove (DELETE) and the
 *     capability grants are `roleManage` (ADMIN+); role change (PATCH) is
 *     `roleCreate` (MANAGER+), down the ladder. The page draws the line in
 *     the same place or it hands out controls that 403;
 *   - a capability toggle and a role change still PATCH the same endpoints,
 *     and still surface a refusal — apiFetch RESOLVES on 4xx, so a dropped
 *     `res.ok` check would read as success;
 *   - ?member=<userId> (the ⌘K palette's link) opens that person.
 */

const api = vi.fn()
vi.mock("@/lib/api-fetch", () => ({ apiFetch: (...a: unknown[]) => api(...a) }))
const toast = vi.hoisted(() => ({ success: vi.fn(), error: vi.fn(), info: vi.fn(), warning: vi.fn() }))
vi.mock("sonner", () => ({ toast }))
vi.mock("@/components/features/members/invite-member-dialog", () => ({
  InviteMemberDialog: () => <div data-testid="invite-dialog" />,
}))
const h = vi.hoisted(() => ({ role: "ADMIN" as string | null, userId: "u-caller" }))
vi.mock("@/hooks/use-workspace", () => ({
  useWorkspace: () => ({ workspaceId: "ws1", role: h.role, loading: false }),
}))
vi.mock("@/hooks/use-auth", () => ({
  useAuth: () => ({ session: { user: { id: h.userId, name: "Caller", email: "c@x.io" } } }),
}))
vi.mock("@/hooks/use-mobile", () => ({ useIsMobile: () => false }))
// Reads the real location, so a test sets the URL with history.replaceState
// the way a browser arrival would.
vi.mock("next/navigation", () => ({
  useRouter: () => ({ push: vi.fn(), replace: vi.fn(), prefetch: vi.fn(), back: vi.fn() }),
  useSearchParams: () => new URLSearchParams(window.location.search),
  usePathname: () => "/settings/members",
}))

import { MembersPage } from "../members-page"
import { MembersMatrix } from "../members-matrix"

beforeAll(() => {
  Element.prototype.scrollIntoView = vi.fn()
  Element.prototype.hasPointerCapture = vi.fn(() => false)
  Element.prototype.setPointerCapture = vi.fn()
  Element.prototype.releasePointerCapture = vi.fn()
})

function member(id: string, role: string, userId: string, name: string | null, email = `${userId}@x.io`) {
  return { id, role, created_at: new Date().toISOString(), user: { id: userId, email, full_name: name, avatar_url: null } }
}

// The caller ("u-caller") is not on the roster unless a test says so, to keep
// the "own row" lock out of the way of the tier assertions.
const ROSTER = [
  member("m-olive", "OWNER", "u-olive", "Olive Owner"),
  member("m-ada", "ADMIN", "u-ada", "Ada Admin"),
  member("m-mo", "MANAGER", "u-mo", "Mo Manager"),
  member("m-mel", "MEMBER", "u-mel", "Mel Member"),
  member("m-vic", "VIEWER", "u-vic", null, "vic@example.com"),
]

// The v109 OWNER backfill verbatim: 7 of the 8, reveal withheld.
const CAPS = {
  members: [
    { user_id: "u-olive", role: "OWNER", capabilities: ["chat", "routine.create", "skill.create", "credential.create", "credential.rotate", "issue.create", "memory.write"] },
    { user_id: "u-ada", role: "ADMIN", capabilities: ["chat", "credentials:reveal"] },
    { user_id: "u-mel", role: "MEMBER", capabilities: ["chat", "issue.create"] },
    { user_id: "u-vic", role: "VIEWER", capabilities: ["chat"] },
  ],
}

const ok = (b: unknown) => ({ ok: true, status: 200, json: async () => b, text: async () => "" })
const writes = () => api.mock.calls.filter(([, init]) => (init as RequestInit | undefined)?.method)
let roster = ROSTER

/** Reads route by URL; a test queues the response to its own write with
 *  `nextWrite`. */
let nextWrite: unknown = null
beforeEach(() => {
  api.mockReset()
  toast.success.mockReset()
  toast.error.mockReset()
  roster = ROSTER
  nextWrite = null
  api.mockImplementation(async (url: string, init?: RequestInit) => {
    if (init?.method) return nextWrite ?? ok({})
    if (url.includes("/members/capabilities")) return ok(CAPS)
    if (url.includes("/members?")) return ok(roster)
    return ok({})
  })
  h.role = "ADMIN"
  h.userId = "u-caller"
  window.history.replaceState(null, "", "/settings/members")
})
afterEach(() => cleanup())

function renderPage(url = "/settings/members") {
  window.history.replaceState(null, "", url)
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(<QueryClientProvider client={qc}><MembersPage /></QueryClientProvider>)
}

const people = () => screen.getByRole("list", { name: "People" })
const rowFor = (name: string) => screen.getByRole("button", { name: `Show ${name}` })
const detail = () => screen.queryByRole("region", { name: /^Member details:/ })
/** Wait for the capability read to land (Mel's extra chip appears). */
async function capsLoaded() {
  await waitFor(() => expect(within(rowFor("Mel Member")).getByText("Create issues")).toBeInTheDocument())
}

describe("Members page — the sub-bar", () => {
  it("counts the people and offers Bulk edit and Add member to an ADMIN", async () => {
    renderPage()
    await screen.findByRole("list", { name: "People" })
    expect(screen.getByText("5 people · who can do what in this workspace")).toBeInTheDocument()
    expect(screen.getByTestId("invite-dialog")).toBeInTheDocument()
    expect(screen.getByRole("button", { name: "Bulk edit capabilities" })).toBeInTheDocument()
  })

  it("offers neither to a MANAGER — both are ADMIN+ routes", async () => {
    h.role = "MANAGER"
    renderPage()
    await screen.findByRole("list", { name: "People" })
    expect(screen.queryByTestId("invite-dialog")).toBeNull()
    expect(screen.queryByRole("button", { name: "Bulk edit capabilities" })).toBeNull()
    expect(screen.getByText(/only admins can invite or remove members/i)).toBeInTheDocument()
  })

  it("keeps the role ladder behind a help affordance", async () => {
    renderPage()
    await screen.findByRole("list", { name: "People" })
    expect(screen.queryByText(/All permissions except billing transfer/i)).toBeNull()
    fireEvent.click(screen.getByRole("button", { name: /what do the roles mean/i }))
    expect(await screen.findByText(/All permissions except billing transfer/i)).toBeInTheDocument()
  })
})

describe("Members page — the People list", () => {
  it("lists every person once, by email when they have no name", async () => {
    renderPage()
    await screen.findByRole("list", { name: "People" })
    for (const name of ["Olive Owner", "Ada Admin", "Mo Manager", "Mel Member", "vic@example.com"]) {
      expect(within(people()).getAllByText(name)).toHaveLength(1)
    }
  })

  it("shows only capabilities granted beyond the role, at most two and a +N", async () => {
    renderPage()
    await capsLoaded()
    // Mel: issue.create, and not the chat every role has.
    const mel = rowFor("Mel Member")
    expect(within(mel).queryByText("Chat")).toBeNull()
    expect(mel.querySelectorAll("[data-capability]")).toHaveLength(1)
    // Olive holds six beyond chat: two chips and "+4".
    const olive = rowFor("Olive Owner")
    expect(olive.querySelectorAll("[data-capability]")).toHaveLength(2)
    expect(within(olive).getByText("+4")).toBeInTheDocument()
    // Vic holds only chat: no chips at all.
    expect(rowFor("vic@example.com").querySelectorAll("[data-capability]")).toHaveLength(0)
  })

  it("never shows capability chips or reads capabilities below ADMIN", async () => {
    h.role = "MEMBER"
    renderPage()
    await screen.findByRole("list", { name: "People" })
    expect(document.querySelectorAll("[data-slot=member-extra-caps]")).toHaveLength(0)
    // The roster is the only request: the capability read is admin-only.
    expect(api.mock.calls.every(([url]) => !String(url).includes("/capabilities"))).toBe(true)
    expect(writes()).toHaveLength(0)
  })

  it("asks for no capabilities when the role is unknown (the #866.3 regression)", async () => {
    h.role = null
    renderPage()
    await screen.findByRole("list", { name: "People" })
    expect(api.mock.calls.every(([url]) => !String(url).includes("/capabilities"))).toBe(true)
  })
})

describe("Members page — filters", () => {
  it("filters by role from the side panel, with counts", async () => {
    renderPage()
    await capsLoaded()
    const viewer = screen.getByRole("button", { name: /^Viewer\s*1$/ })
    fireEvent.click(viewer)
    expect(viewer).toHaveAttribute("aria-pressed", "true")
    expect(within(people()).getAllByRole("button")).toHaveLength(1)
    expect(window.location.search).toContain("role=VIEWER")
    expect(screen.getByRole("button", { name: /^All people\s*5$/ })).toBeInTheDocument()
  })

  it("filters by capability, counting its holders, with high-stakes ones in the destructive tone", async () => {
    renderPage()
    await capsLoaded()
    const reveal = screen.getByRole("button", { name: /^Reveal secrets\s*1$/ })
    expect(within(reveal).getByText("Reveal secrets")).toHaveClass("text-destructive")
    fireEvent.click(reveal)
    expect(within(people()).getAllByRole("button").map((b) => b.getAttribute("aria-label"))).toEqual(["Show Ada Admin"])
  })

  it("combines role, capability and search", async () => {
    renderPage()
    await capsLoaded()
    fireEvent.click(screen.getByRole("button", { name: /^Create issues\s*2$/ }))
    expect(within(people()).getAllByRole("button")).toHaveLength(2) // Olive and Mel
    fireEvent.change(screen.getByRole("textbox", { name: "Search members" }), { target: { value: "mel" } })
    expect(within(people()).getAllByRole("button").map((b) => b.getAttribute("aria-label"))).toEqual(["Show Mel Member"])
    fireEvent.click(screen.getByRole("button", { name: /^Owner\s*0$/ }))
    expect(screen.getByText("No one matches these filters.")).toBeInTheDocument()
    // Every filter is a removable chip; Clear all resets them.
    fireEvent.click(screen.getByRole("button", { name: "Clear all" }))
    expect(within(people()).getAllByRole("button")).toHaveLength(5)
  })

  it("searches by email too", async () => {
    renderPage()
    await screen.findByRole("list", { name: "People" })
    fireEvent.change(screen.getByRole("textbox", { name: "Search members" }), { target: { value: "example.com" } })
    expect(within(people()).getAllByRole("button").map((b) => b.getAttribute("aria-label"))).toEqual(["Show vic@example.com"])
  })

  it("offers no capability filter below ADMIN", async () => {
    h.role = "MANAGER"
    renderPage()
    await screen.findByRole("list", { name: "People" })
    expect(screen.queryByText("Has capability")).toBeNull()
    expect(screen.queryByRole("group", { name: "View" })).toBeNull()
  })
})

describe("Members page — one person", () => {
  it("opens nobody until a row is picked, then that person and only them", async () => {
    renderPage()
    await screen.findByRole("list", { name: "People" })
    expect(detail()).toBeNull()
    fireEvent.click(rowFor("Mel Member"))
    expect(detail()).toHaveAccessibleName("Member details: Mel Member")
    expect(rowFor("Mel Member")).toHaveAttribute("aria-pressed", "true")
    expect(rowFor("Mo Manager")).toHaveAttribute("aria-pressed", "false")
    expect(window.location.search).toBe("?member=u-mel")
    // What the person's own role grants, stated in their panel.
    expect(within(detail()!).getByText(/Own resource access only/i)).toBeInTheDocument()
  })

  it("opens the person a ?member= link names (the ⌘K palette's link)", async () => {
    renderPage("/settings/members?member=u-mo")
    expect(await screen.findByRole("region", { name: "Member details: Mo Manager" })).toBeInTheDocument()
  })

  it("opens nobody when the link names someone who is not a member", async () => {
    // A stale link, or a person removed since — the roster still renders.
    renderPage("/settings/members?member=u-ghost")
    await screen.findByRole("list", { name: "People" })
    expect(detail()).toBeNull()
  })

  it("goes back to the list from the panel", async () => {
    renderPage("/settings/members?member=u-mo")
    await screen.findByRole("region", { name: "Member details: Mo Manager" })
    fireEvent.click(screen.getByRole("button", { name: "All people" }))
    expect(detail()).toBeNull()
    expect(window.location.search).toBe("")
  })
})

describe("Members page — role", () => {
  it("gives an ADMIN a role control for those below them, and text for the OWNER", async () => {
    renderPage("/settings/members?member=u-mel")
    const group = await screen.findByRole("radiogroup", { name: "Role for Mel Member" })
    // ADMIN may grant only below Admin; Admin is shown but cannot be picked.
    expect(within(group).getByRole("radio", { name: "Member" })).toHaveAttribute("aria-checked", "true")
    expect(within(group).getByRole("radio", { name: "Manager" })).toBeEnabled()
    expect(within(group).getByRole("radio", { name: "Admin" })).toBeDisabled()

    fireEvent.click(rowFor("Olive Owner"))
    expect(screen.queryByRole("radiogroup")).toBeNull()
    expect(within(detail()!).getByText(/immutable/)).toBeInTheDocument()
  })

  it("gives a MANAGER a role control only over people they outrank", async () => {
    h.role = "MANAGER"
    renderPage("/settings/members?member=u-mel")
    expect(await screen.findByRole("radiogroup", { name: "Role for Mel Member" })).toBeInTheDocument()
    fireEvent.click(rowFor("Ada Admin"))
    expect(screen.queryByRole("radiogroup")).toBeNull()
  })

  it("gives a MEMBER no control at all — read-only, with no switches either", async () => {
    h.role = "MEMBER"
    renderPage("/settings/members?member=u-vic")
    await screen.findByRole("region", { name: /^Member details:/ })
    expect(screen.queryByRole("radiogroup")).toBeNull()
    expect(screen.queryAllByRole("switch")).toHaveLength(0)
    expect(screen.queryByRole("button", { name: /remove member/i })).toBeNull()
    expect(screen.getByText(/only managers and admins can make changes here/i)).toBeInTheDocument()
  })

  it("never offers a change on the caller's own row", async () => {
    h.role = "OWNER"
    h.userId = "u-olive"
    renderPage("/settings/members?member=u-olive")
    await screen.findByRole("region", { name: /^Member details:/ })
    expect(screen.queryByRole("radiogroup")).toBeNull()
  })

  async function changeMelTo(role: string) {
    renderPage("/settings/members?member=u-mel")
    const group = await screen.findByRole("radiogroup", { name: "Role for Mel Member" })
    fireEvent.click(within(group).getByRole("radio", { name: role }))
    // Approval first: a confirm dialog gates the PATCH.
    expect(writes()).toHaveLength(0)
    fireEvent.click(await screen.findByRole("button", { name: /^Change role$/i }))
  }

  it("PATCHes the membership after the confirmation and reports success", async () => {
    await changeMelTo("Manager")
    await waitFor(() => expect(writes()).toHaveLength(1))
    const [url, init] = writes()[0] as [string, RequestInit]
    expect(init.method).toBe("PATCH")
    expect(url).toBe("/api/v1/workspaces/ws1/members/m-mel?workspace_id=ws1")
    expect(JSON.parse(String(init.body))).toEqual({ role: "MANAGER" })
    await waitFor(() => expect(toast.success).toHaveBeenCalledWith("Role changed to MANAGER"))
  })

  it("surfaces the server's problem detail instead of reporting success", async () => {
    nextWrite = { ok: false, status: 403, json: async () => ({ detail: "cannot grant a role at or above your own" }) }
    await changeMelTo("Manager")
    await waitFor(() => expect(toast.error).toHaveBeenCalledWith("cannot grant a role at or above your own"))
    expect(toast.success).not.toHaveBeenCalled()
  })
})

describe("Members page — capabilities", () => {
  it("shows one person's switches, grouped, with Credentials marked high-stakes", async () => {
    renderPage("/settings/members?member=u-mel")
    await capsLoaded()
    const panel = detail()!
    for (const g of ["Routines", "Content", "Credentials"]) expect(within(panel).getByRole("region", { name: g })).toBeInTheDocument()
    expect(within(panel).getByRole("heading", { name: /Credentials/ })).toHaveClass("text-destructive")
    expect(within(panel).getByRole("switch", { name: "Revoke issue.create from Mel Member" })).toHaveAttribute("aria-checked", "true")
    expect(within(panel).getByRole("switch", { name: "Grant memory.write to Mel Member" })).toHaveAttribute("aria-checked", "false")
    // chat comes with the role: on, and locked.
    const chat = within(panel).getByRole("switch", { name: /Chat is always granted/ })
    expect(chat).toHaveAttribute("aria-checked", "true")
    expect(chat).toBeDisabled()
  })

  it("shows an OWNER the grants they actually hold, locked — immutable is not full", async () => {
    renderPage("/settings/members?member=u-olive")
    await capsLoaded()
    const reveal = screen.getByRole("switch", { name: /OWNER capabilities are immutable: credentials:reveal/ })
    expect(reveal).toHaveAttribute("aria-checked", "false")
    expect(reveal).toBeDisabled()
    expect(screen.getByRole("switch", { name: /OWNER capabilities are immutable: memory.write/ })).toHaveAttribute("aria-checked", "true")
  })

  it("PATCHes one grant for the open person and keeps the server's set", async () => {
    nextWrite = ok({ user_id: "u-mel", role: "MEMBER", capabilities: ["chat", "issue.create", "memory.write"] })
    renderPage("/settings/members?member=u-mel")
    await capsLoaded()
    fireEvent.click(screen.getByRole("switch", { name: "Grant memory.write to Mel Member" }))
    await waitFor(() => expect(writes()).toHaveLength(1))
    const [url, init] = writes()[0] as [string, RequestInit]
    expect(url).toBe("/api/v1/workspaces/ws1/members/u-mel/capabilities?workspace_id=ws1")
    expect(JSON.parse(String(init.body))).toEqual({ grant: ["memory.write"] })
    await waitFor(() => expect(screen.getByRole("switch", { name: "Revoke memory.write from Mel Member" })).toBeInTheDocument())
  })

  it("rolls back and says why when the server refuses — never the raw text", async () => {
    nextWrite = { ok: false, status: 403, text: async () => "forbidden: caller lost admin", json: async () => ({}) }
    renderPage("/settings/members?member=u-mel")
    await capsLoaded()
    fireEvent.click(screen.getByRole("switch", { name: "Grant memory.write to Mel Member" }))
    await waitFor(() => expect(toast.error).toHaveBeenCalled())
    expect(String(toast.error.mock.calls[0][0])).toMatch(/permission denied/i)
    expect(String(toast.error.mock.calls[0][0])).not.toMatch(/caller lost admin/)
    await waitFor(() => expect(screen.getByRole("switch", { name: "Grant memory.write to Mel Member" })).toBeInTheDocument())
  })

  it("keeps resource access with the person, for an ADMIN", async () => {
    renderPage("/settings/members?member=u-mel")
    await screen.findByRole("region", { name: /^Member details:/ })
    expect(screen.getByRole("region", { name: "Resource access for Mel Member" })).toBeInTheDocument()
    expect(screen.getByRole("button", { name: "Edit resource access" })).toBeInTheDocument()
  })
})

describe("Members page — removal", () => {
  it("removes after the confirmation with the same DELETE, then closes the panel", async () => {
    renderPage("/settings/members?member=u-mel")
    await screen.findByRole("region", { name: /^Member details:/ })
    fireEvent.click(screen.getByRole("button", { name: /remove member/i }))
    expect(writes()).toHaveLength(0)
    roster = ROSTER.filter((m) => m.id !== "m-mel")
    fireEvent.click(await screen.findByRole("button", { name: /^Remove$/ }))
    await waitFor(() => expect(writes()).toHaveLength(1))
    const [url, init] = writes()[0] as [string, RequestInit]
    expect(init.method).toBe("DELETE")
    expect(url).toBe("/api/v1/workspaces/ws1/members/m-mel?workspace_id=ws1")
    await waitFor(() => expect(toast.success).toHaveBeenCalledWith("Member removed"))
    await waitFor(() => expect(detail()).toBeNull())
  })

  it("offers no removal for the OWNER, for yourself, or below ADMIN", async () => {
    renderPage("/settings/members?member=u-olive")
    await screen.findByRole("region", { name: /^Member details:/ })
    expect(screen.queryByRole("button", { name: /remove member/i })).toBeNull()
    cleanup()
    h.role = "MANAGER"
    renderPage("/settings/members?member=u-mel")
    await screen.findByRole("region", { name: /^Member details:/ })
    expect(screen.queryByRole("button", { name: /remove member/i })).toBeNull()
  })
})

describe("Members page — matrix", () => {
  it("draws each cell as comes-with-role, granted or off, and toggles with the same PATCH", async () => {
    renderPage("/settings/members?view=matrix")
    const grid = await screen.findByRole("region", { name: "Capability matrix" })
    await waitFor(() => expect(within(grid).getByRole("checkbox", { name: "Revoke issue.create from Mel Member" })).toBeInTheDocument())
    const melRow = grid.querySelector('tr[data-member="u-mel"]')!
    expect(melRow.querySelector('[data-state="role"]')).toBeTruthy()
    expect(within(melRow as HTMLElement).getByRole("checkbox", { name: /Chat is always granted/ })).toBeDisabled()
    expect(within(melRow as HTMLElement).getByRole("checkbox", { name: "Revoke issue.create from Mel Member" })).toHaveAttribute("data-state", "granted")
    expect(within(melRow as HTMLElement).getByRole("checkbox", { name: "Grant memory.write to Mel Member" })).toHaveAttribute("data-state", "off")
    // The OWNER row is locked whatever it holds.
    expect(screen.getByRole("checkbox", { name: /OWNER capabilities are immutable: memory.write/ })).toBeDisabled()

    fireEvent.click(screen.getByRole("checkbox", { name: "Grant memory.write to Mel Member" }))
    await waitFor(() => expect(writes()).toHaveLength(1))
    const [url, init] = writes()[0] as [string, RequestInit]
    expect(url).toBe("/api/v1/workspaces/ws1/members/u-mel/capabilities?workspace_id=ws1")
    expect(JSON.parse(String(init.body))).toEqual({ grant: ["memory.write"] })
  })

  it("follows the side panel's filters and opens a person from their name", async () => {
    renderPage("/settings/members?view=matrix&role=MEMBER")
    const grid = await screen.findByRole("region", { name: "Capability matrix" })
    expect(grid.querySelectorAll("tbody tr")).toHaveLength(1)
    fireEvent.click(within(grid).getByRole("button", { name: "Show Mel Member" }))
    expect(await screen.findByRole("region", { name: "Member details: Mel Member" })).toBeInTheDocument()
  })

  it("opens People instead for a caller who cannot read capabilities", async () => {
    h.role = "MANAGER"
    renderPage("/settings/members?view=matrix")
    await screen.findByRole("list", { name: "People" })
    expect(screen.queryByRole("region", { name: "Capability matrix" })).toBeNull()
  })

  it("is read-only when the caller cannot edit", () => {
    const qc = new QueryClient()
    render(
      <QueryClientProvider client={qc}>
        <MembersMatrix members={ROSTER.slice(3)} workspaceId="ws1" currentUserId="u-caller"
          capsByUser={{ "u-mel": ["chat", "issue.create"] }} canEdit={false} onSelect={vi.fn()} />
      </QueryClientProvider>,
    )
    const cells = screen.getAllByRole("checkbox")
    expect(cells.length).toBeGreaterThan(0)
    for (const c of cells) expect(c).toBeDisabled()
    expect(screen.getByRole("checkbox", { name: "Mel Member has issue.create" })).toHaveAttribute("aria-checked", "true")
    expect(screen.getByText(/Read-only — only admins change capabilities/)).toBeInTheDocument()
  })
})
