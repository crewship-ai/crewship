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
 *   - the roster reads what each person may do as three area chips, and the
 *     page leads with what needs attention (who can reveal, who holds more
 *     than their role, invitations running out);
 *   - capability edits are drafts until the page's Save.
 */

const api = vi.fn()
vi.mock("@/lib/api-fetch", () => ({ apiFetch: (...a: unknown[]) => api(...a) }))
const toast = vi.hoisted(() => ({ success: vi.fn(), error: vi.fn(), info: vi.fn(), warning: vi.fn() }))
vi.mock("sonner", () => ({ toast }))
vi.mock("@/components/features/members/invite-member-dialog", () => ({
  InviteMemberDialog: () => <div data-testid="invite-dialog" />,
}))
const h = vi.hoisted(() => ({ role: "ADMIN" as string | null, userId: "u-caller", mobile: false, workspaceId: "ws1" }))
vi.mock("@/hooks/use-workspace", () => ({
  useWorkspace: () => ({ workspaceId: h.workspaceId, role: h.role, loading: false }),
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
let invitations: unknown[] = []

/** Reads route by URL; a test queues the response to its own write with
 *  `nextWrite`. */
let nextWrite: unknown = null
beforeEach(() => {
  api.mockReset()
  toast.success.mockReset()
  toast.error.mockReset()
  roster = ROSTER
  invitations = []
  nextWrite = null
  api.mockImplementation(async (url: string, init?: RequestInit) => {
    if (init?.method) return nextWrite ?? ok({})
    if (url.includes("/members/capabilities")) return ok(CAPS)
    if (url.includes("/members?")) return ok(roster)
    if (url.includes("/invitations")) return ok(invitations)
    if (url.includes("/reveal-policy")) return ok({ enabled: false })
    return ok({})
  })
  h.role = "ADMIN"
  h.userId = "u-caller"
  h.mobile = false
  h.workspaceId = "ws1"
  window.history.replaceState(null, "", "/settings/members")
})
afterEach(() => cleanup())

function renderPage(url = "/settings/members") {
  window.history.replaceState(null, "", url)
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(<QueryClientProvider client={qc}><MembersPage /></QueryClientProvider>)
}

const table = () => screen.findByRole("table", { name: "People and what they may do" })
const rowOf = (userId: string) => document.querySelector<HTMLElement>(`tr[data-member="${userId}"]`)!
const sheet = () => waitFor(() => {
  const el = document.querySelector<HTMLElement>("[data-slot=member-detail]")
  if (!el) throw new Error("no person open")
  return el
})
const confirmWith = async (name: RegExp) => fireEvent.click(await screen.findByRole("button", { name }))
/** The page's save bar (the first one: the person panel docks a second). */
const saveBar = () => screen.findAllByRole("region", { name: "Unsaved changes" }).then((r) => r[0])
const save = async () => fireEvent.click(within(await saveBar()).getByRole("button", { name: "Save" }))
const bodyOf = (i = 0) => JSON.parse(String((writes()[i][1] as RequestInit).body))
/** waitFor until a selector matches — querySelector alone returns null, which
 *  waitFor would accept on the first try. */
const slot = (selector: string, root: ParentNode = document) => waitFor(() => {
  const el = root.querySelector<HTMLElement>(selector)
  if (!el) throw new Error(`no ${selector}`)
  return el
})
const areas = (userId: string) => [...rowOf(userId).querySelectorAll("[data-area]")].map((e) => e.getAttribute("data-area"))

describe("Members — the roster", () => {
  it("groups people by role and reads what each may do as area chips", async () => {
    renderPage()
    await table()
    await waitFor(() => expect(areas("u-ada")).toEqual(["secrets"]))
    expect(within(rowOf("u-ada")).getByText(/reveal/)).toBeInTheDocument()
    expect(areas("u-olive")).toEqual(["routines", "content", "secrets"])
    expect(within(rowOf("u-mel")).getByText("Content")).toBeInTheDocument()
    expect(within(rowOf("u-mel")).getByText("1/4")).toBeInTheDocument()
    expect(areas("u-vic")).toEqual(["chat"])
    const groups = screen.getAllByRole("rowheader").filter((h) => h.getAttribute("scope") === "rowgroup").map((h) => h.textContent)
    expect(groups).toEqual(["Owners1", "Admins1", "Managers1", "Members1", "Viewers1"])
    const summary = document.querySelector("[data-slot=members-summary]")!
    expect(summary).toHaveTextContent("5 people")
    expect(summary).toHaveTextContent("1 can reveal secrets")
  })

  it("leads with what needs attention, each finding with its verb", async () => {
    roster = [...ROSTER, member("m-ned", "MEMBER", "u-ned", "Ned Newbie")]
    CAPS.members.push({ user_id: "u-ned", role: "MEMBER", capabilities: ["chat", "credential.create"] })
    invitations = [{ id: "i1", email: "pat@x.io", role: "MEMBER", expires_at: new Date(Date.now() + 86_400_000).toISOString(), created_at: new Date().toISOString() }]
    try {
      renderPage()
      await table()
      const reveal = await slot("[data-finding=reveal]")
      expect(reveal).toHaveTextContent("1 person can reveal secrets in plaintext.")
      const above = document.querySelector<HTMLElement>("[data-finding=above]")!
      expect(above).toHaveTextContent("Ned Newbie can create credentials as a Member.")
      expect(rowOf("u-ned").querySelector("[aria-label='Above their role']")).not.toBeNull()
      await waitFor(() => expect(document.querySelector("[data-finding=invites]")).toHaveTextContent("pat@x.io"))
      fireEvent.click(within(above).getByRole("button", { name: /Open/ }))
      expect(await sheet()).toHaveAccessibleName("Member details: Ned Newbie")
    } finally {
      CAPS.members.pop()
    }
  })

  it("says so when nothing needs attention", async () => {
    const saved = CAPS.members[1].capabilities
    CAPS.members[1].capabilities = ["chat"]
    try {
      renderPage()
      await table()
      expect(await screen.findByText(/Nothing needs attention/)).toBeInTheDocument()
    } finally {
      CAPS.members[1].capabilities = saved
    }
  })

  it("drafts a change in the panel and sends it only on Save", async () => {
    renderPage("/settings/members?member=u-mel")
    const d = await sheet()
    fireEvent.click(await within(d).findByRole("switch", { name: "Grant memory.write to Mel Member" }))
    expect(within(d).getByRole("switch", { name: "Revoke memory.write from Mel Member" })).toHaveAttribute("aria-checked", "true")
    expect(rowOf("u-mel")).toHaveAttribute("data-unsaved")
    expect(await saveBar()).toHaveTextContent("1 unsaved change")
    expect(writes()).toHaveLength(0)
    await save()
    await waitFor(() => expect(writes()).toHaveLength(1))
    expect(writes()[0][0]).toBe("/api/v1/workspaces/ws1/members/u-mel/capabilities?workspace_id=ws1")
    expect(bodyOf()).toEqual({ set: ["chat", "issue.create", "memory.write"] })
  })

  it("drops the draft on Discard", async () => {
    renderPage("/settings/members?member=u-mel")
    const d = await sheet()
    fireEvent.click(await within(d).findByRole("switch", { name: "Grant memory.write to Mel Member" }))
    fireEvent.click(within(await saveBar()).getByRole("button", { name: "Discard" }))
    await waitFor(() => expect(within(d).getByRole("switch", { name: "Grant memory.write to Mel Member" })).toHaveAttribute("aria-checked", "false"))
    expect(writes()).toHaveLength(0)
  })

  it("asks before granting anything high-stakes", async () => {
    renderPage("/settings/members?member=u-mel")
    const d = await sheet()
    fireEvent.click(await within(d).findByRole("switch", { name: "Grant credentials:reveal to Mel Member" }))
    expect(screen.queryByRole("region", { name: "Unsaved changes" })).toBeNull()
    await confirmWith(/Grant Reveal secrets/)
    await save()
    await waitFor(() => expect(writes()).toHaveLength(1))
    expect(bodyOf()).toEqual({ set: ["chat", "credentials:reveal", "issue.create"] })
  })

  it("sets a preset as a draft of the whole set", async () => {
    renderPage("/settings/members?member=u-ada")
    const d = await sheet()
    fireEvent.click(await within(d).findByRole("radio", { name: "Power user" }))
    await save()
    await waitFor(() => expect(writes()).toHaveLength(1))
    expect(writes()[0][0]).toBe("/api/v1/workspaces/ws1/members/u-ada/capabilities?workspace_id=ws1")
    expect(bodyOf()).toEqual({ set: ["chat", "issue.create", "memory.write", "routine.create", "routine.run"] })
  })

  it("turns off what is above a person's role from the panel's warning", async () => {
    roster = [...ROSTER, member("m-ned", "MEMBER", "u-ned", "Ned Newbie")]
    CAPS.members.push({ user_id: "u-ned", role: "MEMBER", capabilities: ["chat", "issue.create", "credentials:reveal"] })
    try {
      renderPage("/settings/members?member=u-ned")
      const d = await sheet()
      const warning = await slot("[data-slot=member-above-role]", d)
      expect(within(warning).getByRole("button", { name: "Make Manager" })).toBeInTheDocument()
      fireEvent.click(within(warning).getByRole("button", { name: "Turn Reveal off" }))
      await save()
      await waitFor(() => expect(writes()).toHaveLength(1))
      expect(bodyOf()).toEqual({ set: ["chat", "issue.create"] })
    } finally {
      CAPS.members.pop()
    }
  })

  it("locks the owner and the caller's own row", async () => {
    h.userId = "u-ada"
    renderPage()
    await table()
    expect(within(rowOf("u-ada")).getByRole("checkbox", { name: "Select Ada Admin" })).toBeDisabled()
    expect(within(rowOf("u-olive")).getByRole("checkbox", { name: "Select Olive Owner" })).toBeDisabled()
    fireEvent.click(within(rowOf("u-olive")).getByTitle(/^Olive Owner/))
    const d = await sheet()
    await waitFor(() => expect(within(d).getByRole("switch", { name: /OWNER capabilities are immutable: memory.write/ })).toBeDisabled())
  })

  it("changes many people at once from the selection bar", async () => {
    renderPage()
    await table()
    fireEvent.click(await within(rowOf("u-mel")).findByRole("checkbox", { name: "Select Mel Member" }))
    fireEvent.click(within(rowOf("u-vic")).getByRole("checkbox", { name: "Select vic@example.com" }))
    const bar = await screen.findByRole("toolbar", { name: "Change the selected people" })
    expect(bar).toHaveTextContent("2 selected")
    fireEvent.pointerDown(within(bar).getByRole("button", { name: /Revoke/ }), { button: 0, ctrlKey: false })
    fireEvent.click(await screen.findByRole("menuitem", { name: "Create issues" }))
    // Mel loses issue.create; Vic never had it, so only one person changes.
    await save()
    await waitFor(() => expect(writes()).toHaveLength(1))
    expect(writes()[0][0]).toBe("/api/v1/workspaces/ws1/members/u-mel/capabilities?workspace_id=ws1")
    expect(bodyOf()).toEqual({ set: ["chat"] })
  })

  it("filters by area, by chat only and by who can reveal", async () => {
    renderPage("/settings/members?flag=reveal")
    await table()
    await waitFor(() => expect(document.querySelectorAll("tr[data-member]")).toHaveLength(1))
    expect(rowOf("u-ada")).toBeInTheDocument()
    cleanup()
    renderPage("/settings/members?area=chat")
    await table()
    await waitFor(() => expect([...document.querySelectorAll("tr[data-member]")].map((r) => r.getAttribute("data-member"))).toEqual(["u-mo", "u-vic"]))
    cleanup()
    renderPage("/settings/members?area=routines")
    await table()
    await waitFor(() => expect([...document.querySelectorAll("tr[data-member]")].map((r) => r.getAttribute("data-member"))).toEqual(["u-olive"]))
  })

  it("lists who can reveal with the workspace switch, and grants reveal as a draft", async () => {
    renderPage("/settings/members?flag=reveal")
    await table()
    const card = await slot("[data-slot=members-reveal-policy]")
    await waitFor(() => expect(card).toHaveTextContent("Reveal is off for this workspace"))
    fireEvent.change(screen.getByLabelText("Person to grant reveal to"), { target: { value: "u-mel" } })
    fireEvent.click(screen.getByRole("button", { name: "Grant" }))
    await confirmWith(/Grant Reveal secrets/)
    await save()
    await waitFor(() => expect(writes()).toHaveLength(1))
    expect(writes()[0][0]).toBe("/api/v1/workspaces/ws1/members/u-mel/capabilities?workspace_id=ws1")
    expect(bodyOf()).toEqual({ set: ["chat", "credentials:reveal", "issue.create"] })
  })

  it("lists pending invitations with when each runs out", async () => {
    invitations = [{ id: "i1", email: "pat@x.io", role: "MEMBER", expires_at: new Date(Date.now() + 5 * 86_400_000).toISOString(), created_at: new Date().toISOString(), inviter: { id: "u-ada", email: "ada@x.io", full_name: "Ada Admin" } }]
    renderPage("/settings/members?status=invited")
    const list = await slot("[data-slot=members-invitations]")
    expect(list).toHaveTextContent("pat@x.io")
    expect(list).toHaveTextContent("invited by Ada Admin")
    expect(list).toHaveTextContent("Expires in 5 days")
    expect(screen.queryByRole("table", { name: "People and what they may do" })).toBeNull()
  })
})

describe("Members — one person", () => {
  it("opens from the row with role, preset and what differs from it", async () => {
    renderPage()
    await table()
    fireEvent.click(within(rowOf("u-mel")).getByTitle(/^Mel Member/))
    const d = await sheet()
    expect(d).toHaveAccessibleName("Member details: Mel Member")
    expect(within(d).getByRole("radio", { name: "Member" })).toHaveAttribute("aria-checked", "true")
    expect(within(d).getByRole("radio", { name: "Chat only" })).toHaveAttribute("aria-checked", "true")
    expect(d.querySelector("[data-slot=member-exceptions]")).toHaveTextContent("+Issues")
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
    await waitFor(() => expect(toast.error).toHaveBeenCalledWith("Cannot modify this member", { duration: Infinity }))
  })

  it("lets a MANAGER change roles below them but never capabilities", async () => {
    h.role = "MANAGER"
    renderPage("/settings/members?member=u-mel")
    const d = await sheet()
    expect(within(d).getByRole("radio", { name: "Viewer" })).toBeEnabled()
    expect(within(d).getByRole("radio", { name: "Manager" })).toBeDisabled()
    expect(within(d).queryByText("What they can do")).toBeNull()
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
  it("lists people by role with what they may do as icons, and opens one full screen", async () => {
    h.mobile = true
    renderPage()
    const list = await waitFor(() => {
      const el = document.querySelector<HTMLElement>("[data-slot=members-list]")
      if (!el) throw new Error("no list")
      return el
    })
    await waitFor(() => expect(within(list).getByLabelText("Secrets, can reveal")).toBeInTheDocument())
    expect(document.querySelector("[data-slot=members-actions]")).toContainElement(screen.getByTestId("invite-dialog"))
    fireEvent.click(within(list).getByTitle(/^Mel Member/))
    expect(await sheet()).toHaveAccessibleName("Member details: Mel Member")
  })
})

/**
 * #2878 — a restricted member looked exactly like a trusted one. The roster
 * carries `access_mode` (sent only to a trusted OWNER/ADMIN); the page marks
 * the exception next to the role and names the mode in the person's panel.
 */
describe("Members — access mode", () => {
  const withMode = (m: ReturnType<typeof member>, access_mode?: "trusted" | "restricted") => ({ ...m, ...(access_mode ? { access_mode } : {}) })

  it("marks a restricted member's row and leaves a trusted member's row unmarked", async () => {
    roster = [withMode(member("m-rita", "MEMBER", "u-rita", "Rita Restricted"), "restricted"), withMode(member("m-tom", "MEMBER", "u-tom", "Tom Trusted"), "trusted")]
    renderPage()
    await table()
    expect(within(rowOf("u-rita")).getByText("Restricted")).toBeTruthy()
    // Role and access are two facts: the role pill still reads Member.
    expect(within(rowOf("u-rita")).getByText("Member")).toBeTruthy()
    expect(within(rowOf("u-tom")).queryByText("Restricted")).toBeNull()
  })

  it("names the access mode in the person's panel", async () => {
    roster = [withMode(member("m-rita", "MEMBER", "u-rita", "Rita Restricted"), "restricted")]
    renderPage("/settings/members?member=u-rita")
    expect((await screen.findByTestId("member-access-mode")).textContent).toMatch(/^Restricted/)
  })

  it("does not guess an access mode the server withheld", async () => {
    roster = [member("m-rita", "MEMBER", "u-rita", "Rita Restricted")]
    renderPage("/settings/members?member=u-rita")
    expect(await sheet()).toHaveAccessibleName("Member details: Rita Restricted")
    expect(screen.queryByTestId("member-access-mode")).toBeNull()
    expect(screen.queryByText("Restricted")).toBeNull()
  })
})

/** Review of #3056: what may a draft write, and where. */
describe("Members — drafts stay where they were made", () => {
  it("does not replace a member's grants when the capability cache has no entry for them", async () => {
    // A newly added person can reach the refreshed roster before the bulk
    // capability cache has been refreshed. Missing is not an empty set.
    renderPage("/settings/members?member=u-mo")
    const control = await within(await sheet()).findByRole("switch", { name: "Grant memory.write to Mo Manager" })
    await waitFor(() => expect(api.mock.calls.some(([url]) => String(url).includes("/members/capabilities"))).toBe(true))
    expect(control).toBeDisabled()
    fireEvent.click(control)
    expect(writes()).toHaveLength(0)
    expect(screen.queryByRole("region", { name: "Unsaved changes" })).toBeNull()
  })

  it("does not save one workspace's drafts into another", async () => {
    const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
    window.history.replaceState(null, "", "/settings/members?member=u-mel")
    const ui = () => <QueryClientProvider client={qc}><MembersPage /></QueryClientProvider>
    const rendered = render(ui())
    fireEvent.click(await within(await sheet()).findByRole("switch", { name: "Grant memory.write to Mel Member" }))
    await saveBar()
    // The switcher asks first (PageSaveProvider's guard); switched anyway,
    // ws1's draft is neither shown nor savable in ws2.
    h.workspaceId = "ws2"
    rendered.rerender(ui())
    await waitFor(() => expect(api.mock.calls.some(([url]) => String(url).includes("ws2/members?"))).toBe(true))
    await waitFor(() => expect(screen.queryByRole("region", { name: "Unsaved changes" })).toBeNull())
    fireEvent.keyDown(window, { key: "s", ctrlKey: true })
    expect(writes()).toHaveLength(0)
  })

  it("will not edit what people may do until it has been read", async () => {
    const normal = api.getMockImplementation()!
    api.mockImplementation(async (url: string, init?: RequestInit) => {
      if (!init?.method && url.includes("/members/capabilities")) return { ok: false, status: 500, json: async () => ({}), text: async () => "" }
      return normal(url, init)
    })
    renderPage("/settings/members?member=u-mel")
    expect(await screen.findByText(/Could not read what people may do/)).toBeInTheDocument()
    const control = await within(await sheet()).findByRole("switch", { name: "Grant memory.write to Mel Member" })
    // Mel holds issue.create; an empty set here would have revoked it on Save.
    expect(control).toBeDisabled()
    fireEvent.click(control)
    expect(screen.queryByRole("region", { name: "Unsaved changes" })).toBeNull()
    expect(writes()).toHaveLength(0)
  })
})

// Harbor 2: the Invited view offers Reissue (a new link, shown once, valid 7
// days) and Revoke (after a confirmation) on each pending invitation. An
// ADMIN invitation is the owner's to manage, as the server enforces.
describe("Members — pending invitations", () => {
  const pending = (id: string, role: string, email: string) => ({
    id, email, role,
    expires_at: new Date(Date.now() + 2 * 86_400_000).toISOString(),
    created_at: new Date().toISOString(),
    inviter: null,
  })

  it("reissues an invitation and shows the new link once", async () => {
    invitations = [pending("i1", "MEMBER", "pat@x.io")]
    nextWrite = ok({ id: "i1", email: "pat@x.io", role: "MEMBER", token: "wi1_new", expires_at: new Date(Date.now() + 7 * 86_400_000).toISOString() })
    renderPage("/settings/members?status=invited")
    const list = await slot("[data-slot=members-invitations]")
    fireEvent.click(within(list).getByRole("button", { name: "Reissue pat@x.io" }))
    await waitFor(() => expect(writes()[0][0]).toBe("/api/v1/workspaces/ws1/invitations/i1/reissue?workspace_id=ws1"))
    expect(await within(list).findByDisplayValue(`${window.location.origin}/join#invitation_token=wi1_new`)).toBeInTheDocument()
    expect(list).toHaveTextContent(/won’t be shown again/)
  })

  it("revokes an invitation after a confirmation", async () => {
    invitations = [pending("i1", "MEMBER", "pat@x.io")]
    renderPage("/settings/members?status=invited")
    const list = await slot("[data-slot=members-invitations]")
    fireEvent.click(within(list).getByRole("button", { name: "Revoke pat@x.io" }))
    expect(writes()).toHaveLength(0)
    await confirmWith(/^Revoke invitation$/)
    await waitFor(() => expect(writes()[0]).toEqual(["/api/v1/workspaces/ws1/invitations/i1?workspace_id=ws1", { method: "DELETE" }]))
  })

  it("leaves an Admin invitation to the owner", async () => {
    invitations = [pending("i2", "ADMIN", "boss@x.io")]
    renderPage("/settings/members?status=invited")
    const list = await slot("[data-slot=members-invitations]")
    expect(within(list).queryByRole("button", { name: "Revoke boss@x.io" })).toBeNull()
    expect(within(list).getByText(/Only the owner manages an Admin invitation/)).toBeInTheDocument()
  })

  it("says when nothing is pending", async () => {
    renderPage("/settings/members?status=invited")
    expect(await screen.findByText("No pending invitations.")).toBeInTheDocument()
  })
})

// Harbor 2 (#3487 SET-01): one person opens in the kit's Peek — 480 px from
// the right, J / K to the next / previous person in the order the roster
// shows them — and each confirmation says WHEN it lands: a role change is
// written at once ("Applies now"), a capability is a draft until the page's
// Save ("Takes effect when you save").
describe("Members — the person peek", () => {
  const peek = () => waitFor(() => {
    const el = document.querySelector<HTMLElement>("[data-slot=peek]")
    if (!el) throw new Error("no peek")
    return el
  })

  it("opens the person in the shared 480 px Peek, not a 520 px sheet", async () => {
    renderPage("/settings/members?member=u-mel")
    const p = await peek()
    expect(p).toContainElement(await sheet())
    expect(p.className).toContain("w-[480px]")
    expect(document.querySelector("[data-slot=member-sheet]")).toBeNull()
    expect(within(p).getByRole("heading", { name: "Mel Member" })).toBeInTheDocument()
  })

  it("moves to the next and previous person with J and K, in roster order", async () => {
    renderPage("/settings/members?member=u-mel")
    await table()
    const p = await peek()
    expect(await sheet()).toHaveAccessibleName("Member details: Mel Member")
    fireEvent.keyDown(p, { key: "j" })
    await waitFor(() => expect(document.querySelector("[data-slot=member-detail]")).toHaveAccessibleName("Member details: vic@example.com"))
    fireEvent.keyDown(p, { key: "k" })
    fireEvent.keyDown(p, { key: "k" })
    await waitFor(() => expect(document.querySelector("[data-slot=member-detail]")).toHaveAccessibleName("Member details: Mo Manager"))
    expect(within(p).getByRole("button", { name: "Previous (K)" })).toBeInTheDocument()
    expect(within(p).getByRole("button", { name: "Next (J)" })).toBeInTheDocument()
  })

  it("says a role change applies now", async () => {
    renderPage("/settings/members?member=u-mel")
    const d = await sheet()
    fireEvent.click(within(d).getByRole("radio", { name: "Manager" }))
    const dialog = await screen.findByRole("alertdialog")
    expect(dialog).toHaveTextContent(/Applies now/)
    expect(dialog).not.toHaveTextContent(/when you save/i)
    expect(dialog.querySelector("[data-when=now]")).not.toBeNull()
  })

  it("says a high-stakes grant takes effect when you save", async () => {
    renderPage("/settings/members?member=u-mel")
    const d = await sheet()
    fireEvent.click(await within(d).findByRole("switch", { name: "Grant credentials:reveal to Mel Member" }))
    const dialog = await screen.findByRole("alertdialog")
    expect(dialog).toHaveTextContent(/Takes effect when you save/)
    expect(dialog).not.toHaveTextContent(/Applies now/)
    expect(dialog.querySelector("[data-when=save]")).not.toBeNull()
  })

  it("says a role change for many people applies now", async () => {
    renderPage()
    await table()
    fireEvent.click(await within(rowOf("u-mel")).findByRole("checkbox", { name: "Select Mel Member" }))
    const bar = await screen.findByRole("toolbar", { name: "Change the selected people" })
    fireEvent.pointerDown(within(bar).getByRole("button", { name: /Role/ }), { button: 0, ctrlKey: false })
    fireEvent.click(await screen.findByRole("menuitem", { name: "Viewer" }))
    const dialog = await screen.findByRole("alertdialog")
    expect(dialog).toHaveTextContent(/Applies now/)
    expect(dialog.querySelector("[data-when=now]")).not.toBeNull()
  })

  // SET-07: the section head belongs to the Settings drill frame — drawn once,
  // and already there while the roster loads.
  it("says what the page is once, in the section head, even while loading", async () => {
    api.mockImplementation(() => new Promise(() => {}))
    renderPage()
    const head = document.querySelector<HTMLElement>("[data-slot=settings-page-head]")
    expect(head).not.toBeNull()
    expect(within(head!).getByRole("heading", { level: 2, name: "Members" })).toBeInTheDocument()
    expect(screen.getAllByText(/Who is in this workspace/)).toHaveLength(1)
  })

  it("names the CLI command for the person in the peek's footer", async () => {
    renderPage("/settings/members?member=u-mel")
    const p = await peek()
    expect(p).toHaveTextContent("crewship workspace member capabilities list u-mel")
  })
})
