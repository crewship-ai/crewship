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
 *   - ?member=<userId> (the ⌘K palette's link) opens that person;
 *   - the matrix describes each person as an access profile plus its
 *     exceptions, and a profile is one `{"preset"}` PATCH.
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

const matrix = () => screen.findByRole("table", { name: "People and what they may do" })
const rowOf = (userId: string) => document.querySelector<HTMLElement>(`tr[data-member="${userId}"]`)!
const sheet = () => waitFor(() => {
  const el = document.querySelector<HTMLElement>("[data-slot=member-detail]")
  if (!el) throw new Error("no person open")
  return el
})
const confirmWith = async (name: RegExp) => fireEvent.click(await screen.findByRole("button", { name }))

describe("Members — the access matrix", () => {
  it("groups people by role and describes each as a profile plus exceptions", async () => {
    renderPage()
    await matrix()
    await waitFor(() => expect(within(rowOf("u-ada")).getByRole("button", { name: /Access profile for Ada Admin/ })).toBeInTheDocument())
    expect(within(rowOf("u-ada")).getByRole("button", { name: /Access profile for Ada Admin: Chat only \+Reveal/ })).toBeInTheDocument()
    expect(within(rowOf("u-vic")).getByRole("button", { name: /Chat only$/ })).toBeInTheDocument()
    expect(within(rowOf("u-olive")).getByText("Owner · fixed")).toBeInTheDocument()
    const groups = screen.getAllByRole("rowheader").filter((h) => h.getAttribute("scope") === "rowgroup").map((h) => h.textContent)
    expect(groups).toEqual(["Owners1", "Admins1", "Managers1", "Members1", "Viewers1"])
    const summary = document.querySelector("[data-slot=members-summary]")!
    expect(summary).toHaveTextContent("5 people")
    expect(summary).toHaveTextContent("1 can reveal secrets")
  })

  it("marks an exception and grants a plain capability at once", async () => {
    renderPage()
    await matrix()
    const reveal = await within(rowOf("u-ada")).findByRole("checkbox", { name: "Revoke credentials:reveal from Ada Admin" })
    expect(reveal).toHaveAttribute("data-state", "added")
    fireEvent.click(within(rowOf("u-mel")).getByRole("checkbox", { name: "Grant memory.write to Mel Member" }))
    await waitFor(() => expect(writes()).toHaveLength(1))
    expect(writes()[0][0]).toBe("/api/v1/workspaces/ws1/members/u-mel/capabilities?workspace_id=ws1")
    expect(JSON.parse(String((writes()[0][1] as RequestInit).body))).toEqual({ grant: ["memory.write"] })
  })

  it("asks before granting anything high-stakes", async () => {
    renderPage()
    await matrix()
    fireEvent.click(await within(rowOf("u-mel")).findByRole("checkbox", { name: "Grant credentials:reveal to Mel Member" }))
    expect(writes()).toHaveLength(0)
    await confirmWith(/Grant Reveal secrets/)
    await waitFor(() => expect(writes()).toHaveLength(1))
    expect(JSON.parse(String((writes()[0][1] as RequestInit).body))).toEqual({ grant: ["credentials:reveal"] })
  })

  it("sets a profile with one preset call, confirming when it replaces exceptions", async () => {
    renderPage()
    await matrix()
    fireEvent.pointerDown(await within(rowOf("u-ada")).findByRole("button", { name: /Access profile for Ada Admin/ }), { button: 0, ctrlKey: false })
    fireEvent.click(await screen.findByRole("menuitem", { name: /^Power user/ }))
    await confirmWith(/Set to Power user/)
    await waitFor(() => expect(writes()).toHaveLength(1))
    expect(writes()[0][0]).toBe("/api/v1/workspaces/ws1/members/u-ada/capabilities?workspace_id=ws1")
    expect(JSON.parse(String((writes()[0][1] as RequestInit).body))).toEqual({ preset: "power" })
  })

  it("locks the owner and the caller's own row", async () => {
    h.userId = "u-ada"
    renderPage()
    await matrix()
    await waitFor(() => expect(within(rowOf("u-olive")).getByRole("checkbox", { name: /OWNER capabilities are immutable: memory.write/ })).toBeDisabled())
    expect(within(rowOf("u-ada")).getByRole("checkbox", { name: /You cannot modify your own capabilities: memory.write/ })).toBeDisabled()
    expect(within(rowOf("u-ada")).getByRole("checkbox", { name: "Select Ada Admin" })).toBeDisabled()
  })

  it("changes many people at once from the selection bar", async () => {
    renderPage()
    await matrix()
    fireEvent.click(await within(rowOf("u-mel")).findByRole("checkbox", { name: "Select Mel Member" }))
    fireEvent.click(within(rowOf("u-vic")).getByRole("checkbox", { name: "Select vic@example.com" }))
    const bar = await screen.findByRole("toolbar", { name: "Change the selected people" })
    expect(bar).toHaveTextContent("2 selected")
    fireEvent.pointerDown(within(bar).getByRole("button", { name: /Revoke/ }), { button: 0, ctrlKey: false })
    fireEvent.click(await screen.findByRole("menuitem", { name: "Create issues" }))
    await confirmWith(/^Revoke$/)
    await waitFor(() => expect(writes()).toHaveLength(2))
    expect(writes().map(([u]) => u)).toEqual([
      "/api/v1/workspaces/ws1/members/u-mel/capabilities?workspace_id=ws1",
      "/api/v1/workspaces/ws1/members/u-vic/capabilities?workspace_id=ws1",
    ])
    expect(JSON.parse(String((writes()[0][1] as RequestInit).body))).toEqual({ revoke: ["issue.create"] })
  })

  it("filters by profile, by exceptions and by who can reveal", async () => {
    renderPage("/settings/members?flag=reveal")
    await matrix()
    await waitFor(() => expect(document.querySelectorAll("tr[data-member]")).toHaveLength(1))
    expect(rowOf("u-ada")).toBeInTheDocument()
    cleanup()
    renderPage("/settings/members?profile=chat")
    await matrix()
    await waitFor(() => expect([...document.querySelectorAll("tr[data-member]")].map((r) => r.getAttribute("data-member"))).toEqual(["u-mo", "u-vic"]))
  })
})

describe("Members — one person", () => {
  it("opens from the name with role, profile and capabilities", async () => {
    renderPage()
    await matrix()
    fireEvent.click(within(rowOf("u-mel")).getByTitle(/^Mel Member/))
    const d = await sheet()
    expect(d).toHaveAccessibleName("Member details: Mel Member")
    expect(within(d).getByRole("radio", { name: "Member" })).toHaveAttribute("aria-checked", "true")
    expect(within(d).getByText(/1 exception: \+ Issues/)).toBeInTheDocument()
  })

  it("opens the person a ?member= link names", async () => {
    renderPage("/settings/members?member=u-ada")
    expect(await sheet()).toHaveAccessibleName("Member details: Ada Admin")
    // Read once, then dropped: closing the panel must not bring it back.
    await waitFor(() => expect(window.location.search).toBe(""))
  })

  it("changes a role through a confirmation, and surfaces a refusal", async () => {
    nextWrite = { ok: false, status: 403, json: async () => ({ detail: "Cannot modify this member" }), text: async () => "" }
    renderPage("/settings/members?member=u-mel")
    const d = await sheet()
    fireEvent.click(within(d).getByRole("radio", { name: "Manager" }))
    fireEvent.click(await screen.findByRole("button", { name: "Change role" }))
    await waitFor(() => expect(writes()).toHaveLength(1))
    expect(writes()[0][0]).toBe("/api/v1/workspaces/ws1/members/m-mel?workspace_id=ws1")
    await waitFor(() => expect(toast.error).toHaveBeenCalledWith("Cannot modify this member"))
  })

  it("lets a MANAGER change roles below them but never capabilities", async () => {
    h.role = "MANAGER"
    renderPage("/settings/members?member=u-mel")
    const d = await sheet()
    expect(within(d).getByRole("radio", { name: "Viewer" })).toBeEnabled()
    expect(within(d).getByRole("radio", { name: "Manager" })).toBeDisabled()
    expect(within(d).queryByText(/Access profile/)).toBeNull()
    expect(screen.queryByRole("checkbox", { name: /Select/ })).toBeNull()
    expect(api.mock.calls.some(([u]) => String(u).includes("/members/capabilities"))).toBe(false)
  })

  it("is read-only for a MEMBER", async () => {
    h.role = "MEMBER"
    renderPage("/settings/members?member=u-vic")
    const d = await sheet()
    expect(within(d).getByText(/Your role cannot change this person's role/)).toBeInTheDocument()
    expect(within(d).queryByRole("button", { name: /Remove member/ })).toBeNull()
  })
})

describe("Members — on a phone", () => {
  it("lists people by role with their profile, and opens one full screen", async () => {
    h.mobile = true
    renderPage()
    const list = await waitFor(() => {
      const el = document.querySelector<HTMLElement>("[data-slot=members-list]")
      if (!el) throw new Error("no list")
      return el
    })
    await waitFor(() => expect(within(list).getByText("+Reveal")).toBeInTheDocument())
    expect(document.querySelector("[data-slot=members-actions]")).toContainElement(screen.getByTestId("invite-dialog"))
    fireEvent.click(within(list).getByTitle(/^Mel Member/))
    expect(await sheet()).toHaveAccessibleName("Member details: Mel Member")
  })
})
