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
const h = vi.hoisted(() => ({ role: "ADMIN" as string | null, userId: "u-caller", mobile: false }))
vi.mock("@/hooks/use-workspace", () => ({
  useWorkspace: () => ({ workspaceId: "ws1", role: h.role, loading: false }),
}))
vi.mock("@/hooks/use-auth", () => ({
  useAuth: () => ({ session: { user: { id: h.userId, name: "Caller", email: "c@x.io" } } }),
}))
vi.mock("@/hooks/use-mobile", () => ({ useIsMobile: () => h.mobile }))
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
  h.mobile = false
  window.history.replaceState(null, "", "/settings/members")
})
afterEach(() => cleanup())

function renderPage(url = "/settings/members") {
  window.history.replaceState(null, "", url)
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(<QueryClientProvider client={qc}><MembersPage /></QueryClientProvider>)
}

/** The people in the side panel, in order — each row's title is
 *  "<name> · <email>". */
const personRows = () => Array.from(document.querySelectorAll<HTMLElement>("nav button[title]"))
const names = () => personRows().map((b) => b.getAttribute("title")!.split(" · ")[0])
const rowFor = (name: string) => personRows().find((b) => b.getAttribute("title")!.startsWith(`${name} · `))!
const detail = () => screen.queryByRole("region", { name: /^Member details:/ })
const roster$ = () => screen.findByRole("region", { name: /^Member details:/ })
/** Wait for the capability read to land (Mel's row says "1 extra"). */
async function capsLoaded() {
  await waitFor(() => expect(rowFor("Mel Member")).toHaveTextContent("1 extra"))
}
function openCapabilityFilter() {
  fireEvent.click(document.querySelector<HTMLElement>('[aria-haspopup="dialog"]')!)
}
const capOption = (label: string) => screen.getAllByRole("button").find((b) => b.getAttribute("aria-pressed") !== null && b.textContent?.startsWith(label))!

describe("Members page — the sub-bar and summary", () => {
  it("states the numbers first and offers Bulk edit and Add member to an ADMIN", async () => {
    renderPage()
    await roster$()
    const summary = document.querySelector("[data-slot=members-summary]")!
    expect(summary).toHaveTextContent("5 people")
    expect(summary).toHaveTextContent("2 owners and admins")
    await waitFor(() => expect(summary).toHaveTextContent("1 can reveal secrets"))
    expect(screen.getByTestId("invite-dialog")).toBeInTheDocument()
    expect(screen.getByRole("button", { name: "Bulk edit capabilities" })).toBeInTheDocument()
  })

  it("offers neither to a MANAGER — both are ADMIN+ routes", async () => {
    h.role = "MANAGER"
    renderPage()
    await roster$()
    expect(screen.queryByTestId("invite-dialog")).toBeNull()
    expect(screen.queryByRole("button", { name: "Bulk edit capabilities" })).toBeNull()
    expect(screen.getByText(/only admins can invite or remove members/i)).toBeInTheDocument()
  })
})

describe("Members page — the side panel", () => {
  it("lists every person once, grouped by role, by email when they have no name", async () => {
    renderPage()
    await roster$()
    expect(names()).toEqual(["Olive Owner", "Ada Admin", "Mo Manager", "Mel Member", "vic@example.com"])
    for (const g of ["Owners", "Admins", "Managers", "Members", "Viewers"]) expect(screen.getByRole("button", { name: new RegExp(`^${g}`) })).toBeInTheDocument()
  })

  it("says under each name what they hold beyond the role", async () => {
    renderPage()
    await capsLoaded()
    expect(rowFor("Olive Owner")).toHaveTextContent("Everything")
    expect(rowFor("Ada Admin")).toHaveTextContent("1 extra · 1 high-stakes")
    expect(rowFor("Mel Member")).toHaveTextContent("1 extra")
    expect(rowFor("vic@example.com")).toHaveTextContent("Role defaults")
  })

  it("never reads capabilities below ADMIN, or with an unknown role (the #866.3 regression)", async () => {
    for (const role of ["MEMBER", null]) {
      cleanup()
      api.mockClear()
      h.role = role
      renderPage()
      await roster$()
      expect(api.mock.calls.every(([url]) => !String(url).includes("/capabilities"))).toBe(true)
      expect(writes()).toHaveLength(0)
    }
  })

  it("filters by role, with counts, kept in the URL", async () => {
    renderPage()
    await capsLoaded()
    const viewer = screen.getByRole("button", { name: /^Viewer\s*1$/ })
    fireEvent.click(viewer)
    expect(viewer).toHaveAttribute("aria-pressed", "true")
    expect(names()).toEqual(["vic@example.com"])
    expect(window.location.search).toContain("role=VIEWER")
    expect(screen.getByRole("button", { name: /^All people\s*5$/ })).toBeInTheDocument()
  })

  it("filters by capability from the Filter popover, high-stakes ones in the destructive tone", async () => {
    renderPage()
    await capsLoaded()
    openCapabilityFilter()
    const reveal = capOption("Reveal secrets")
    expect(reveal).toHaveTextContent("1")
    expect(within(reveal).getByText("Reveal secrets")).toHaveClass("text-destructive")
    fireEvent.click(reveal)
    expect(names()).toEqual(["Ada Admin"])
  })

  it("combines role, capability and search, and clears them all", async () => {
    renderPage()
    await capsLoaded()
    openCapabilityFilter()
    fireEvent.click(capOption("Create issues"))
    expect(names()).toEqual(["Olive Owner", "Mel Member"])
    fireEvent.change(screen.getByRole("textbox", { name: "Search members" }), { target: { value: "mel" } })
    expect(names()).toEqual(["Mel Member"])
    fireEvent.click(screen.getByRole("button", { name: /^Owner\s*0$/ }))
    expect(names()).toEqual([])
    expect(screen.getAllByText("Nobody matches these filters.").length).toBeGreaterThan(0)
    // Close the filter popover (it has a Clear all of its own), then clear
    // from the chips row.
    fireEvent.keyDown(document, { key: "Escape" })
    await waitFor(() => expect(screen.getAllByRole("button", { name: "Clear all" })).toHaveLength(1))
    fireEvent.click(screen.getByRole("button", { name: "Clear all" }))
    expect(names()).toHaveLength(5)
  })

  it("searches by email too", async () => {
    renderPage()
    await roster$()
    fireEvent.change(screen.getByRole("textbox", { name: "Search members" }), { target: { value: "example.com" } })
    expect(names()).toEqual(["vic@example.com"])
  })

  it("offers no capability filter or matrix below ADMIN", async () => {
    h.role = "MANAGER"
    renderPage()
    await roster$()
    expect(document.querySelector('[aria-haspopup="dialog"]')).toBeNull()
    expect(screen.queryByRole("group", { name: "View" })).toBeNull()
  })
})

describe("Members page — one person", () => {
  it("always has someone open — the first person, then whoever is picked", async () => {
    renderPage()
    expect(await roster$()).toHaveAccessibleName("Member details: Olive Owner")
    fireEvent.click(rowFor("Mel Member"))
    expect(detail()).toHaveAccessibleName("Member details: Mel Member")
    expect(rowFor("Mel Member")).toHaveAttribute("aria-current", "true")
    expect(window.location.search).toBe("?member=u-mel")
    // The person's profile and what their role means, side by side.
    expect(within(detail()!).getByRole("region", { name: "Mel Member profile" })).toBeInTheDocument()
    expect(within(detail()!).getByText(/Own resource access only/i)).toBeInTheDocument()
  })

  it("opens the person a ?member= link names (the ⌘K palette's link)", async () => {
    renderPage("/settings/members?member=u-mo")
    expect(await screen.findByRole("region", { name: "Member details: Mo Manager" })).toBeInTheDocument()
  })

  it("falls back to the first person when the link names someone who is not a member", async () => {
    renderPage("/settings/members?member=u-ghost")
    expect(await roster$()).toHaveAccessibleName("Member details: Olive Owner")
  })
})

describe("Members page — role", () => {
  it("gives an ADMIN a role choice for those below them, and plain text for the OWNER", async () => {
    renderPage("/settings/members?member=u-mel")
    const group = await screen.findByRole("radiogroup", { name: "Role for Mel Member" })
    expect(within(group).getByRole("radio", { name: "Member" })).toHaveAttribute("aria-checked", "true")
    expect(within(group).getByRole("radio", { name: "Manager" })).toBeEnabled()
    expect(within(group).getByRole("radio", { name: "Admin" })).toBeDisabled()

    fireEvent.click(rowFor("Olive Owner"))
    expect(screen.queryByRole("radiogroup")).toBeNull()
    expect(within(detail()!).getByText(/immutable/)).toBeInTheDocument()
  })

  it("gives a MANAGER a role choice only over people they outrank", async () => {
    h.role = "MANAGER"
    renderPage("/settings/members?member=u-mel")
    expect(await screen.findByRole("radiogroup", { name: "Role for Mel Member" })).toBeInTheDocument()
    fireEvent.click(rowFor("Ada Admin"))
    expect(screen.queryByRole("radiogroup")).toBeNull()
  })

  it("gives a MEMBER no control at all — read-only, with no switches either", async () => {
    h.role = "MEMBER"
    renderPage("/settings/members?member=u-vic")
    await roster$()
    expect(screen.queryByRole("radiogroup")).toBeNull()
    expect(screen.queryAllByRole("switch")).toHaveLength(0)
    expect(screen.queryByRole("button", { name: /remove member/i })).toBeNull()
    expect(screen.getByText(/read-only — managers and admins make changes/i)).toBeInTheDocument()
  })

  it("never offers a change on the caller's own row", async () => {
    h.role = "OWNER"
    h.userId = "u-olive"
    renderPage("/settings/members?member=u-olive")
    await roster$()
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
    await roster$()
    expect(screen.getByRole("region", { name: "Resource access for Mel Member" })).toBeInTheDocument()
    expect(screen.getByRole("button", { name: "Edit resource access" })).toBeInTheDocument()
  })
})

describe("Members page — removal", () => {
  it("removes after the confirmation with the same DELETE, then opens someone else", async () => {
    renderPage("/settings/members?member=u-mel")
    await roster$()
    fireEvent.click(screen.getByRole("button", { name: /remove member/i }))
    expect(writes()).toHaveLength(0)
    roster = ROSTER.filter((m) => m.id !== "m-mel")
    fireEvent.click(await screen.findByRole("button", { name: /^Remove$/ }))
    await waitFor(() => expect(writes()).toHaveLength(1))
    const [url, init] = writes()[0] as [string, RequestInit]
    expect(init.method).toBe("DELETE")
    expect(url).toBe("/api/v1/workspaces/ws1/members/m-mel?workspace_id=ws1")
    await waitFor(() => expect(toast.success).toHaveBeenCalledWith("Member removed"))
    await waitFor(() => expect(detail()).not.toHaveAccessibleName("Member details: Mel Member"))
  })

  it("offers no removal for the OWNER, for yourself, or below ADMIN", async () => {
    renderPage("/settings/members?member=u-olive")
    await roster$()
    expect(screen.queryByRole("button", { name: /remove member/i })).toBeNull()
    cleanup()
    h.role = "MANAGER"
    renderPage("/settings/members?member=u-mel")
    await roster$()
    expect(screen.queryByRole("button", { name: /remove member/i })).toBeNull()
  })
})

describe("Members page — on a phone", () => {
  it("moves the actions into the page and picks the person from a list", async () => {
    h.mobile = true
    renderPage()
    await roster$()
    // The sub-bar has room for the Filters button only; the actions sit in
    // the page instead of overflowing it.
    expect(document.querySelector("[data-slot=members-actions]")).toContainElement(screen.getByTestId("invite-dialog"))
    const pick = screen.getByRole("combobox", { name: "Person" })
    fireEvent.change(pick, { target: { value: "u-mo" } })
    expect(detail()).toHaveAccessibleName("Member details: Mo Manager")
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
    await screen.findByRole("region", { name: /^Member details:/ })
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
