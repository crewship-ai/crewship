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
 *   - the roster reads what each person may do as ONE line (the nearest
 *     preset and what differs), and one banner leads to "Needs a look" (who
 *     holds more than their role, invitations running out, who can reveal);
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
    const access = /\/members\/([^/]+)\/access(\/effective)?\?/.exec(url)
    if (access) {
      const row = roster.find((m) => m.id === access[1]) as { access_mode?: string } | undefined
      const mode = row?.access_mode ?? "trusted"
      return ok(access[2]
        ? { membership_id: access[1], revision: 1, explanation: "", account_restricted: mode === "restricted", decisions: [] }
        : { membership_id: access[1], revision: 1, mode, rights: [] })
    }
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

const table = () => screen.findByRole("table", { name: "People and their roles" })
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
/** The roster's abilities line of one person, as the text a reader sees. */
const abilities = (userId: string) => rowOf(userId).querySelector("[data-slot=abilities-line]")?.textContent
/** Opens the folded "Single abilities" of the open person and returns the sheet. */
const singles = async (d: HTMLElement) => {
  const toggle = await within(d).findByRole("button", { name: /Single abilities/ })
  if (toggle.getAttribute("aria-expanded") !== "true") fireEvent.click(toggle)
  return d
}

describe("Members — the roster", () => {
  it("groups people by role and reads what each may do as one line", async () => {
    renderPage()
    await table()
    await waitFor(() => expect(abilities("u-ada")).toBe("Chat only + reveal secrets"))
    // An owner holds everything; the others read as their nearest preset.
    expect(abilities("u-olive")).toBe("Everything")
    expect(abilities("u-mel")).toBe("Chat only + create issues")
    expect(abilities("u-vic")).toBe("Chat only")
    // A reveal holder is marked in red, an owner is not (ownership implies it).
    expect(within(rowOf("u-ada")).getByText("reveals")).toBeInTheDocument()
    expect(within(rowOf("u-olive")).queryByText("reveals")).toBeNull()
    const groups = screen.getAllByRole("rowheader").filter((h) => h.getAttribute("scope") === "rowgroup").map((h) => h.textContent)
    expect(groups).toEqual(["Owners1", "Admins1", "Managers1", "Members1", "Viewers1"])
    // Every fact once: no strip of figures that repeats the panel.
    expect(document.querySelector("[data-slot=members-summary]")).toBeNull()
    expect(screen.queryByText("Needs attention")).toBeNull()
    expect(screen.queryByText("Can do")).toBeNull()
  })

  it("heads the page with the people and invited counts, and the card with its own", async () => {
    invitations = [{ id: "i1", email: "pat@x.io", role: "MEMBER", status: "pending", expires_at: new Date(Date.now() + 5 * 86_400_000).toISOString(), created_at: new Date().toISOString() }]
    renderPage()
    await table()
    await waitFor(() => expect(document.querySelector("[data-slot=drill-head]")).toHaveTextContent("5 people · 1 invited"))
    expect(document.querySelector("[data-slot=members-toolbar]")).toHaveTextContent("5 people")
    fireEvent.change(screen.getByLabelText("Search members"), { target: { value: "mel" } })
    await waitFor(() => expect(document.querySelector("[data-slot=members-toolbar]")).toHaveTextContent("1 of 5 people"))
  })

  it("leads with ONE banner, and each kind of finding is a card of its own", async () => {
    roster = [...ROSTER, member("m-ned", "MEMBER", "u-ned", "Ned Newbie")]
    CAPS.members.push({ user_id: "u-ned", role: "MEMBER", capabilities: ["chat", "credential.create"] })
    invitations = [{ id: "i1", email: "pat@x.io", role: "MEMBER", status: "pending", expires_at: new Date(Date.now() + 86_400_000).toISOString(), created_at: new Date().toISOString() }]
    try {
      renderPage()
      await table()
      const banner = await slot("[data-slot=members-look-banner]")
      expect(banner).toHaveTextContent("3 things need a look")
      expect(document.querySelectorAll(".page-banner")).toHaveLength(1)
      expect(rowOf("u-ned").querySelector("[aria-label='More than a Member manages']")).not.toBeNull()
      fireEvent.click(within(banner).getByRole("button", { name: "Review" }))
      const above = await slot("[data-finding=above]")
      expect(above).toHaveTextContent("Ned Newbie")
      expect(above).toHaveTextContent("Member · can add secrets")
      expect(await slot("[data-finding=invites]")).toHaveTextContent("pat@x.io")
      expect(await slot("[data-finding=reveal]")).toHaveTextContent("Ada Admin")
      expect(window.location.search).toBe("?status=look")
      fireEvent.click(within(above).getByRole("button", { name: "Open" }))
      expect(await sheet()).toHaveAccessibleName("Member details: Ned Newbie")
    } finally {
      CAPS.members.pop()
    }
  })

  it("drops the banner while a filter is active, and for a read-only role", async () => {
    renderPage("/settings/members?role=MEMBER")
    await table()
    await waitFor(() => expect(document.querySelector("[data-slot=members-look-banner]")).toBeNull())
    cleanup()
    h.role = "MANAGER"
    renderPage()
    await table()
    expect(document.querySelector("[data-slot=members-look-banner]")).toBeNull()
  })

  it("turns a finding off from its card as a draft, never for the caller", async () => {
    roster = [...ROSTER, member("m-ned", "MEMBER", "u-ned", "Ned Newbie")]
    CAPS.members.push({ user_id: "u-ned", role: "MEMBER", capabilities: ["chat", "issue.create", "credential.rotate"] })
    try {
      renderPage("/settings/members?status=look")
      const above = await slot("[data-finding=above]")
      fireEvent.click(within(above).getByRole("button", { name: "Turn off Replace secrets for Ned Newbie" }))
      await save()
      await waitFor(() => expect(writes()).toHaveLength(1))
      expect(writes()[0][0]).toBe("/api/v1/workspaces/ws1/members/u-ned/capabilities?workspace_id=ws1")
      expect(bodyOf()).toEqual({ set: ["chat", "issue.create"] })
    } finally {
      CAPS.members.pop()
    }
  })

  it("caps a finding card at six rows and hands over to the filtered list", async () => {
    const many = Array.from({ length: 8 }, (_, i) => member(`m-x${i}`, "MEMBER", `u-x${i}`, `Xavi ${i}`))
    roster = [...ROSTER, ...many]
    many.forEach((m) => CAPS.members.push({ user_id: m.user.id, role: "MEMBER", capabilities: ["chat", "skill.create"] }))
    try {
      renderPage("/settings/members?status=look")
      const above = await slot("[data-finding=above]")
      expect(above.querySelectorAll("li[data-member]")).toHaveLength(6)
      expect(above).toHaveTextContent("6 of 8 shown")
      fireEvent.click(within(above).getByRole("button", { name: "Show all in the list" }))
      await waitFor(() => expect(window.location.search).toBe("?flag=risky-low-role"))
      await table()
      await waitFor(() => expect(document.querySelectorAll("tr[data-member]")).toHaveLength(8))
    } finally {
      many.forEach(() => CAPS.members.pop())
    }
  })

  it("says so when nothing needs a look", async () => {
    const saved = CAPS.members[1].capabilities
    CAPS.members[1].capabilities = ["chat"]
    try {
      renderPage("/settings/members?status=look")
      expect(await screen.findByText("Nothing needs a look")).toBeInTheDocument()
    } finally {
      CAPS.members[1].capabilities = saved
    }
  })

  it("names the views and the roles in the panel, and puts abilities and reach in the Filter", async () => {
    renderPage()
    await table()
    const panel = document.querySelector<HTMLElement>("[data-slot=drill-panel], [data-panel-id=drill-members]") ?? document.body
    for (const name of ["Everyone", "Needs a look", "Invited", "Owner", "Admin", "Manager", "Member", "Viewer"]) {
      expect(within(panel).getAllByText(name).length).toBeGreaterThan(0)
    }
    expect(screen.queryByText("Can do")).toBeNull()
    expect(screen.getByRole("button", { name: /Filter/ })).toBeInTheDocument()
  })

  it("drafts a change in the panel and sends it only on Save", async () => {
    renderPage("/settings/members?member=u-mel")
    const d = await singles(await sheet())
    fireEvent.click(await within(d).findByRole("switch", { name: "Give Write memory to Mel Member" }))
    expect(within(d).getByRole("switch", { name: "Take away Write memory from Mel Member" })).toHaveAttribute("aria-checked", "true")
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
    const d = await singles(await sheet())
    fireEvent.click(await within(d).findByRole("switch", { name: "Give Write memory to Mel Member" }))
    fireEvent.click(within(await saveBar()).getByRole("button", { name: "Discard" }))
    await waitFor(() => expect(within(d).getByRole("switch", { name: "Give Write memory to Mel Member" })).toHaveAttribute("aria-checked", "false"))
    expect(writes()).toHaveLength(0)
  })

  it("asks before granting anything high-stakes", async () => {
    renderPage("/settings/members?member=u-mel")
    const d = await singles(await sheet())
    fireEvent.click(await within(d).findByRole("switch", { name: "Give Reveal secrets to Mel Member" }))
    expect(screen.queryByRole("region", { name: "Unsaved changes" })).toBeNull()
    await confirmWith(/Give Reveal secrets/)
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
      fireEvent.click(within(warning).getByRole("button", { name: "Turn it off" }))
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
    // An owner's abilities are fixed: said, with no switches to flip.
    fireEvent.click(within(rowOf("u-olive")).getByTitle(/^Olive Owner/))
    const d = await sheet()
    await waitFor(() => expect(d).toHaveTextContent(/An owner.s role and abilities are fixed/))
    expect(within(d).queryByRole("switch")).toBeNull()
    expect(within(d).queryByRole("button", { name: /Remove member/ })).toBeNull()
    // Your own abilities are read, never edited.
    fireEvent.click(within(rowOf("u-ada")).getByTitle(/^Ada Admin/))
    await waitFor(() => expect(document.querySelector("[data-slot=member-detail]")).toHaveAccessibleName("Member details: Ada Admin"))
    const mine = await singles(await sheet())
    expect(within(mine).getByRole("switch", { name: "You cannot change your own abilities: Write memory" })).toBeDisabled()
    expect(within(mine).queryByRole("button", { name: /Remove member/ })).toBeNull()
  })

  it("changes the abilities of many people at once in one dialog", async () => {
    renderPage()
    await table()
    fireEvent.click(await within(rowOf("u-mel")).findByRole("checkbox", { name: "Select Mel Member" }))
    fireEvent.click(within(rowOf("u-vic")).getByRole("checkbox", { name: "Select vic@example.com" }))
    const bar = await screen.findByRole("toolbar", { name: "Change the selected people" })
    expect(bar).toHaveTextContent("2 selected")
    // The old Set access / Grant / Revoke menus are one button now.
    expect(within(bar).queryByRole("button", { name: /Revoke|Grant|Set access/ })).toBeNull()
    fireEvent.click(within(bar).getByRole("button", { name: "Abilities" }))
    const dialog = await screen.findByRole("dialog")
    expect(dialog).toHaveTextContent("Abilities for 2 people")
    fireEvent.click(within(within(dialog).getByRole("radiogroup", { name: "Create issues" })).getByRole("radio", { name: "Take away" }))
    fireEvent.click(within(dialog).getByRole("button", { name: "Apply" }))
    // Mel loses issue.create; Vic never had it, so only one person changes.
    await save()
    await waitFor(() => expect(writes()).toHaveLength(1))
    expect(writes()[0][0]).toBe("/api/v1/workspaces/ws1/members/u-mel/capabilities?workspace_id=ws1")
    expect(bodyOf()).toEqual({ set: ["chat"] })
  })

  it("sets a preset for everyone selected, and asks first when it gives something high-stakes", async () => {
    renderPage()
    await table()
    fireEvent.click(await within(rowOf("u-mel")).findByRole("checkbox", { name: "Select Mel Member" }))
    fireEvent.click(await screen.findByRole("button", { name: "Abilities" }))
    const dialog = await screen.findByRole("dialog")
    fireEvent.click(within(within(dialog).getByRole("radiogroup", { name: "Set everyone to" })).getByRole("radio", { name: "Builder" }))
    fireEvent.click(within(dialog).getByRole("button", { name: "Apply" }))
    const ask = await screen.findByRole("alertdialog")
    expect(ask).toHaveTextContent("Change abilities for 1 person?")
    expect(ask).toHaveTextContent(/Reveal secrets/)
    expect(writes()).toHaveLength(0)
    await confirmWith(/^Apply$/)
    await save()
    await waitFor(() => expect(writes()).toHaveLength(1))
    expect(bodyOf().set).toContain("credentials:reveal")
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
    renderPage("/settings/members?status=look")
    const card = await slot("[data-slot=members-reveal-policy]")
    await waitFor(() => expect(card).toHaveTextContent("Reveal is off for this workspace"))
    fireEvent.click(screen.getByRole("combobox", { name: "Person to grant reveal to" }))
    fireEvent.click(await screen.findByRole("option", { name: /Mel Member/ }))
    fireEvent.click(screen.getByRole("button", { name: "Grant" }))
    await confirmWith(/Give Reveal secrets/)
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
    expect(screen.queryByRole("table", { name: "People and their roles" })).toBeNull()
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
    // Mel holds Chat only plus one ability: a Custom set, with its difference.
    const custom = await within(d).findByRole("radio", { name: "Custom" })
    expect(custom).toHaveAttribute("aria-checked", "true")
    expect(custom).toHaveAccessibleDescription("Chat only + create issues")
    expect(within(d).getByRole("radio", { name: "Chat only" })).toHaveAttribute("aria-checked", "false")
    // The three questions, each in a card of its own, and the way out.
    for (const name of ["Role", "Abilities", "Reach"]) expect(within(d).getByRole("region", { name })).toBeInTheDocument()
    expect(within(d).getByRole("region", { name: "Remove from workspace" })).toBeInTheDocument()
    // Role: every role with its line.
    expect(within(d).getByRole("radio", { name: "Admin" })).toHaveAccessibleDescription("People, crews and every setting")
  })

  it("folds the nine single abilities away, counting those that are on", async () => {
    renderPage("/settings/members?member=u-mel")
    const d = await sheet()
    const toggle = await within(d).findByRole("button", { name: /Single abilities/ })
    expect(toggle).toHaveTextContent("1 of 9 on")
    expect(toggle).toHaveAttribute("aria-expanded", "false")
    expect(within(d).queryByRole("switch")).toBeNull()
    fireEvent.click(toggle)
    expect(within(d).getAllByRole("switch")).toHaveLength(9)
    expect(within(d).getByRole("group", { name: "Secrets" })).toHaveTextContent("high-stakes")
    // The reveal switch is the Owner's: only its state is shown here.
    await waitFor(() => expect(d.querySelector("[data-slot=member-reveal-policy]")).toHaveTextContent("Reveal is off"))
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
    // Abilities and Reach are the admin tier's.
    expect(within(d).queryByRole("region", { name: "Abilities" })).toBeNull()
    expect(within(d).queryByRole("region", { name: "Reach" })).toBeNull()
    expect(within(d).getByText(/You can only give roles below your own/)).toBeInTheDocument()
    expect(screen.queryByRole("checkbox", { name: /Select/ })).toBeNull()
    expect(api.mock.calls.some(([u]) => String(u).includes("/members/capabilities"))).toBe(false)
  })

  it("is read-only for a MEMBER", async () => {
    h.role = "MEMBER"
    renderPage("/settings/members?member=u-vic")
    const d = await sheet()
    expect(within(d).getByText(/Your role cannot change this person’s role/)).toBeInTheDocument()
    expect(within(d).queryByRole("button", { name: /Remove member/ })).toBeNull()
  })
})

describe("Members — on a phone", () => {
  it("lists people as rows with role and abilities in words, and opens one full screen", async () => {
    h.mobile = true
    renderPage()
    const list = await waitFor(() => {
      const el = document.querySelector<HTMLElement>("[data-slot=members-list]")
      if (!el) throw new Error("no list")
      return el
    })
    await waitFor(() => expect(within(list).getByText("reveals")).toBeInTheDocument())
    // Rows, not a table: nothing to scroll sideways at 390 px.
    expect(document.querySelector("table")).toBeNull()
    expect(document.querySelector("[data-slot=members-actions]")).toContainElement(screen.getByTestId("invite-dialog"))
    fireEvent.click(within(list).getByTitle(/^Mel Member/))
    expect(await sheet()).toHaveAccessibleName("Member details: Mel Member")
  })
})

/**
 * #2878 — a restricted member looked exactly like a trusted one. The roster
 * carries `access_mode` (sent only to a trusted OWNER/ADMIN); the page marks
 * the exception next to the role (Guest) and names the reach in the person's
 * panel.
 */
describe("Members — reach", () => {
  const withMode = (m: ReturnType<typeof member>, access_mode?: "trusted" | "restricted") => ({ ...m, ...(access_mode ? { access_mode } : {}) })

  it("marks a Guest's row and leaves a whole-workspace member's row unmarked", async () => {
    roster = [withMode(member("m-rita", "MEMBER", "u-rita", "Rita Restricted"), "restricted"), withMode(member("m-tom", "MEMBER", "u-tom", "Tom Trusted"), "trusted")]
    renderPage()
    await table()
    expect(within(rowOf("u-rita")).getByText("Guest")).toBeTruthy()
    // Role and reach are two facts: the role pill still reads Member.
    expect(within(rowOf("u-rita")).getByText("Member")).toBeTruthy()
    expect(within(rowOf("u-tom")).queryByText("Guest")).toBeNull()
  })

  it("names the reach in the person's panel", async () => {
    roster = [withMode(member("m-rita", "MEMBER", "u-rita", "Rita Restricted"), "restricted")]
    renderPage("/settings/members?member=u-rita")
    const d = await sheet()
    const reach = await within(d).findByRole("radiogroup", { name: "Reach" })
    expect(within(reach).getByRole("radio", { name: "Guest" })).toHaveAttribute("aria-checked", "true")
    expect(within(reach).getByRole("radio", { name: "Whole workspace" })).toHaveAttribute("aria-checked", "false")
  })

  // The role update refuses ADMIN for a Guest with 409
  // (workspaces_member_role.go), so the panel and the bulk bar do not offer it.
  it("does not offer Admin for a Guest, even to an Owner, and says why", async () => {
    h.role = "OWNER"
    roster = [withMode(member("m-rita", "MEMBER", "u-rita", "Rita Restricted"), "restricted")]
    renderPage("/settings/members?member=u-rita")
    const d = await sheet()
    const admin = within(d).getByRole("radio", { name: "Admin" })
    expect(admin).toBeDisabled()
    expect(admin).toHaveAttribute("title", "A Guest cannot be an administrator")
    expect(within(d).getByRole("radio", { name: "Manager" })).toBeEnabled()
    expect(d.querySelector("[data-slot=role-why]")).toHaveTextContent("A Guest cannot be an administrator")
  })

  it("offers Admin to an Owner for a whole-workspace member", async () => {
    h.role = "OWNER"
    roster = [withMode(member("m-tom", "MEMBER", "u-tom", "Tom Trusted"), "trusted")]
    renderPage("/settings/members?member=u-tom")
    expect(within(await sheet()).getByRole("radio", { name: "Admin" })).toBeEnabled()
  })

  it("does not guess a reach the server withheld", async () => {
    roster = [member("m-rita", "MEMBER", "u-rita", "Rita Restricted")]
    renderPage("/settings/members?member=u-rita")
    expect(await sheet()).toHaveAccessibleName("Member details: Rita Restricted")
    expect(document.querySelector("[data-slot=guest-pill]")).toBeNull()
  })

  it("filters by reach from the Filter facets", async () => {
    roster = [withMode(member("m-rita", "MEMBER", "u-rita", "Rita Restricted"), "restricted"), withMode(member("m-tom", "MEMBER", "u-tom", "Tom Trusted"), "trusted")]
    renderPage("/settings/members?reach=guest")
    await table()
    await waitFor(() => expect([...document.querySelectorAll("tr[data-member]")].map((r) => r.getAttribute("data-member"))).toEqual(["u-rita"]))
    expect(screen.getByRole("button", { name: "Remove filter Reach: Guest" })).toBeInTheDocument()
  })
})

/** Review of #3056: what may a draft write, and where. */
describe("Members — drafts stay where they were made", () => {
  it("does not replace a member's grants when the capability cache has no entry for them", async () => {
    // A newly added person can reach the refreshed roster before the bulk
    // capability cache has been refreshed. Missing is not an empty set.
    renderPage("/settings/members?member=u-mo")
    const control = await within(await singles(await sheet())).findByRole("switch", { name: "Give Write memory to Mo Manager" })
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
    fireEvent.click(await within(await singles(await sheet())).findByRole("switch", { name: "Give Write memory to Mel Member" }))
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
    const control = await within(await singles(await sheet())).findByRole("switch", { name: "Give Write memory to Mel Member" })
    // Mel holds issue.create; an empty set here would have revoked it on Save.
    expect(control).toBeDisabled()
    fireEvent.click(control)
    expect(screen.queryByRole("region", { name: "Unsaved changes" })).toBeNull()
    expect(writes()).toHaveLength(0)
  })

  it("shows the unread-capabilities failure as the kit PageBanner (alert, bad tone) with Retry (COM-03)", async () => {
    const normal = api.getMockImplementation()!
    api.mockImplementation(async (url: string, init?: RequestInit) => {
      if (!init?.method && url.includes("/members/capabilities")) return { ok: false, status: 500, json: async () => ({}), text: async () => "" }
      return normal(url, init)
    })
    renderPage("/settings/members")
    const banner = await screen.findByRole("alert")
    expect(banner).toHaveAttribute("data-tone", "bad")
    expect(banner).toHaveClass("page-banner", "page-banner-bad")
    expect(banner).toHaveAttribute("data-slot", "members-caps-error")
    expect(banner).toHaveTextContent(/Could not read what people may do/)
    expect(document.querySelectorAll(".page-banner")).toHaveLength(1)
    const before = api.mock.calls.filter(([url]) => String(url).includes("/members/capabilities")).length
    fireEvent.click(within(banner).getByRole("button", { name: "Retry" }))
    await waitFor(() => expect(api.mock.calls.filter(([url]) => String(url).includes("/members/capabilities")).length).toBeGreaterThan(before))
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
    expect(await screen.findByText("No invitations.")).toBeInTheDocument()
  })

  it("shows what the server says: a revoked invitation is not an expired one", async () => {
    const past = new Date(Date.now() - 3 * 86_400_000).toISOString()
    invitations = [
      { ...pending("i1", "MEMBER", "open@x.io") },
      { ...pending("i2", "MEMBER", "late@x.io"), status: "expired", expires_at: past },
      { ...pending("i3", "VIEWER", "gone@x.io"), status: "revoked", expires_at: "1970-01-01T00:00:00Z" },
    ]
    renderPage("/settings/members?status=invited")
    const list = await slot("[data-slot=members-invitations]")
    expect(list.querySelector("[data-invitation=i3]")).toHaveAttribute("data-status", "revoked")
    expect(within(list.querySelector<HTMLElement>("[data-invitation=i3]")!).getByText("Revoked")).toBeInTheDocument()
    expect(list.querySelector("[data-invitation=i3]")).not.toHaveTextContent(/Expired/)
    expect(list.querySelector("[data-invitation=i2]")).toHaveTextContent("Expired 3 days ago")
    // A revoked invitation can be sent again, not revoked again.
    expect(within(list).queryByRole("button", { name: "Revoke gone@x.io" })).toBeNull()
    expect(within(list).getByRole("button", { name: "Reissue gone@x.io" })).toHaveTextContent("Invite again")
    // The page head counts the ones still waiting.
    expect(document.querySelector("[data-slot=drill-head]")).not.toBeNull()
  })

  it("pages a long list of invitations 25 at a time", async () => {
    invitations = Array.from({ length: 30 }, (_, i) => pending(`i${i}`, "MEMBER", `p${i}@x.io`))
    renderPage("/settings/members?status=invited")
    const list = await slot("[data-slot=members-invitations]")
    expect(list.querySelectorAll("li[data-invitation]")).toHaveLength(25)
    fireEvent.click(screen.getByRole("button", { name: "Show 5 more" }))
    await waitFor(() => expect(list.querySelectorAll("li[data-invitation]")).toHaveLength(30))
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
    const p = await peek()
    expect(await sheet()).toHaveAccessibleName("Member details: Mel Member")
    // The roster behind the modal Peek is hidden from the a11y tree; wait for
    // the neighbours instead.
    await within(p).findByRole("button", { name: "Next (J)" })
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
    fireEvent.click(await within(await singles(d)).findByRole("switch", { name: "Give Reveal secrets to Mel Member" }))
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
    const head = document.querySelector<HTMLElement>("[data-slot=drill-head]")
    expect(head).not.toBeNull()
    expect(within(head!).getByRole("heading", { level: 2, name: "Members" })).toBeInTheDocument()
    // No figure yet: "0 people" would be a wrong answer while the roster loads.
    expect(head).not.toHaveTextContent(/0 people/)
  })

  it("names the CLI command for the person in the peek's footer", async () => {
    renderPage("/settings/members?member=u-mel")
    const p = await peek()
    expect(p).toHaveTextContent("crewship workspace member capabilities list u-mel")
    expect(p).toHaveTextContent("A role change applies at once. Abilities and reach wait for Save.")
  })
})
