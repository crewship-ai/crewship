// Admin › People & workspaces as an instance admin uses it: the panel (views,
// a short list of places, filters), the people lists and the access grid.
// Every write is asserted by the request it sends, not by the component's
// own state. One person's page and one workspace's page have their own files.

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
import { FUTURE, USERS, WORKSPACES, body, calls, m, serve, stubDom } from "./fixtures"

const people = (n = 3) => [
  ...USERS,
  ...Array.from({ length: n }, (_, i) => ({ id: `u-x${i}`, email: `x${i}@ex.com`, full_name: `Extra ${String(i).padStart(2, "0")}`, created_at: "2026-09-04T00:00:00Z", workspace: null, role: "MEMBER",
    memberships: [m("ws-a", "Acme", "MEMBER")], last_active_at: "2026-11-10T00:00:00Z" })),
]
const manyWorkspaces = (n: number) => [
  ...WORKSPACES,
  ...Array.from({ length: n }, (_, i) => ({ id: `ws-${i}`, name: `Team ${String(i).padStart(2, "0")}`, slug: `team-${i}`, created_at: "2026-02-01T00:00:00Z", _count_members: 1, _count_agents: 0, _count_crews: 0, runs_7d: i, runs_by_day: [], kind: "team" })),
]

const pick = async (trigger: HTMLElement, option: RegExp | string) => {
  fireEvent.click(trigger)
  const list = await screen.findByRole("listbox")
  fireEvent.click(within(list).getByRole("option", { name: option }))
}
const panel = () => screen.getByRole("navigation", { name: "People & workspaces sections" })

beforeEach(() => {
  cleanup()
  stubDom()
  window.history.replaceState(null, "", "/admin/people")
  serve(h.apiFetch)
})

describe("the panel", () => {
  it("names views and at most six places, then All N workspaces", async () => {
    serve(h.apiFetch, { users: people(), workspaces: manyWorkspaces(20) })
    render(<PeoplePage />)
    await screen.findByRole("region", { name: "People" })
    const nav = within(panel())
    for (const label of ["Everyone", "Needs a hand", "Instance admins", "Suspended", "All workspaces", "Personal accounts", "Who is where"]) {
      expect(nav.getByRole("button", { name: new RegExp(`^${label}`) })).toBeInTheDocument()
    }
    expect(nav.getByText("Busiest this week")).toBeInTheDocument()
    // The busiest first; six of 22, never the full list.
    const places = nav.getAllByTitle(/ · /)
    expect(places).toHaveLength(6)
    expect(places[0]).toHaveAttribute("title", "Team 19 · team-19")
    expect(nav.getByRole("button", { name: "All 22 workspaces…" })).toBeInTheDocument()
    // No three-way View control, and each action once.
    expect(screen.queryByRole("group", { name: "View" })).toBeNull()
    expect(screen.getAllByRole("button", { name: "New workspace" })).toHaveLength(1)
    expect(screen.getAllByRole("button", { name: "Add person" })).toHaveLength(1)
  })

  it("counts the people in each view", async () => {
    render(<PeoplePage />)
    await screen.findByRole("region", { name: "People" })
    const nav = within(panel())
    expect(nav.getByRole("button", { name: /^Everyone/ })).toHaveTextContent("3")
    // Jana is locked, Perko has not chosen a password: two people need a hand.
    expect(nav.getByRole("button", { name: /^Needs a hand/ })).toHaveTextContent("2")
    expect(nav.getByRole("button", { name: /^Instance admins/ })).toHaveTextContent("1")
  })

  it("filters by status and by workspace from Filter, with All N… for the rest", async () => {
    serve(h.apiFetch, { users: people(), workspaces: manyWorkspaces(10) })
    render(<PeoplePage />)
    await screen.findByRole("region", { name: "People" })
    fireEvent.click(screen.getByRole("button", { name: /Filter/ }))
    const filters = screen.getByRole("group", { name: "Filter people" })
    fireEvent.click(within(filters).getByRole("button", { name: /^Locked/ }))
    const table = screen.getByRole("region", { name: "People" })
    expect(within(table).getAllByRole("row")).toHaveLength(2)
    expect(window.location.search).toContain("facet=locked")
    fireEvent.click(within(filters).getByRole("button", { name: /^All 12…/ }))
    const dialog = await screen.findByRole("dialog", { name: "Workspace" })
    expect(within(dialog).getByPlaceholderText("Search 12 workspaces")).toBeInTheDocument()
    fireEvent.click(within(dialog).getByRole("button", { name: /Acme/ }))
    expect(window.location.search).toContain("in=ws-a")
  })
})

describe("Everyone", () => {
  it("heads with the three counts and one banner for who needs a hand", async () => {
    render(<PeoplePage />)
    const table = await screen.findByRole("region", { name: "People" })
    expect(screen.getByText("3 people · 2 workspaces · 0 personal accounts")).toBeInTheDocument()
    const banner = screen.getByRole("status")
    expect(banner).toHaveTextContent("2 people need a hand")
    expect(banner).toHaveTextContent("1 locked out · 1 without a password · 0 in no workspace")
    // The old findings card is gone.
    expect(screen.queryByRole("region", { name: "Needs attention" })).toBeNull()
    const rows = within(table).getAllByRole("row").slice(1).map((r) => r.textContent ?? "")
    expect(rows[0]).toMatch(/Jana/)
    expect(rows.at(-1)).toMatch(/Boss/)
    expect(within(table).getByText("Setup pending")).toBeInTheDocument()
    expect(within(table).getByText("Instance admin")).toBeInTheDocument()
    fireEvent.click(within(banner).getByRole("button", { name: "Review" }))
    expect(await screen.findByRole("region", { name: /Locked out/ })).toBeInTheDocument()
  })

  it("shows two membership chips and +N, and pages 25 at a time", async () => {
    const wide = { ...USERS[0], id: "u-wide", full_name: "Aaa Wide", email: "wide@ex.com", memberships: [m("ws-a", "Acme", "MEMBER"), m("ws-b", "Lab", "MEMBER"), m("ws-c", "Third", "MEMBER")], instance_admin: false }
    serve(h.apiFetch, { users: [wide, ...people(40)], workspaces: WORKSPACES })
    render(<PeoplePage />)
    const table = await screen.findByRole("region", { name: "People" })
    expect(within(table).getByRole("row", { name: /Aaa Wide/ })).toHaveTextContent("+1")
    expect(within(table).getAllByRole("row")).toHaveLength(26)
    fireEvent.click(within(table).getByRole("button", { name: "Show 19 more" }))
    expect(within(table).getAllByRole("row")).toHaveLength(45)
  })

  it("orders A–Z and by last active, and exports the rows as CSV", async () => {
    const created = vi.fn(() => "blob:x")
    URL.createObjectURL = created
    URL.revokeObjectURL = vi.fn()
    // jsdom would navigate to the blob address and break every later URL.
    const click = vi.spyOn(HTMLAnchorElement.prototype, "click").mockImplementation(() => {})
    render(<PeoplePage />)
    const table = await screen.findByRole("region", { name: "People" })
    fireEvent.click(screen.getByRole("radio", { name: "A–Z" }))
    expect(within(table).getAllByRole("row")[1]).toHaveTextContent("Boss")
    fireEvent.click(screen.getByRole("radio", { name: "Last active" }))
    expect(within(table).getAllByRole("row")[1]).toHaveTextContent("Boss")
    expect(window.location.search).toContain("sort=recent")
    fireEvent.click(screen.getByRole("button", { name: "Export CSV" }))
    expect(created).toHaveBeenCalledTimes(1)
    expect(click).toHaveBeenCalledTimes(1)
    click.mockRestore()
    const blob = (created.mock.calls[0] as unknown as [Blob])[0]
    expect(await blob.text()).toContain("jana@ex.com")
  })

  it("keeps the person list narrowed to a status from the URL", async () => {
    window.history.replaceState(null, "", "/admin/people?facet=setup")
    render(<PeoplePage />)
    const table = await screen.findByRole("region", { name: "People" })
    expect(within(table).getAllByRole("row")).toHaveLength(2)
    expect(within(table).getByText("Perko")).toBeInTheDocument()
    // Narrowed: no banner, and the toolbar says what narrows.
    expect(screen.queryByRole("status")).toBeNull()
  })

  it("opens the old ?view=people&facet=admins link on Instance admins", async () => {
    window.history.replaceState(null, "", "/admin/people?view=people&facet=admins")
    render(<PeoplePage />)
    expect(await screen.findByText("People who run this server: other people, every workspace, limits and backups.")).toBeInTheDocument()
    expect(within(screen.getByRole("region", { name: "People" })).getAllByRole("row")).toHaveLength(2)
  })
})

describe("bulk actions", () => {
  it("suspends the people ticked after one confirmation that says sessions and CLI tokens stay ended", async () => {
    render(<PeoplePage />)
    const table = await screen.findByRole("region", { name: "People" })
    fireEvent.click(within(table).getByRole("checkbox", { name: "Select Jana" }))
    fireEvent.click(within(table).getByRole("checkbox", { name: "Select Perko" }))
    const bar = screen.getByRole("toolbar", { name: "Change the selected people" })
    fireEvent.click(within(bar).getByRole("button", { name: "Suspend" }))
    const dialog = await screen.findByTestId("confirm-dialog")
    expect(within(dialog).getByText("Suspend 2 people?")).toBeInTheDocument()
    expect(dialog).toHaveTextContent(/sessions and CLI tokens end first, and stay ended after a reactivation/)
    expect(calls(h.apiFetch, "POST", "/suspend")).toHaveLength(0)
    fireEvent.change(within(dialog).getByLabelText(/Reason/), { target: { value: "left" } })
    fireEvent.click(within(dialog).getByRole("button", { name: "Suspend 2" }))
    await waitFor(() => expect(calls(h.apiFetch, "POST", "/suspend")).toHaveLength(2))
    expect(body(h.apiFetch, "POST", "/people/u-jana/suspend")).toEqual({ reason: "left" })
  })

  it("adds the people ticked to a workspace picked by search, never as Owner", async () => {
    render(<PeoplePage />)
    const table = await screen.findByRole("region", { name: "People" })
    fireEvent.click(within(table).getByRole("checkbox", { name: "Select Jana" }))
    fireEvent.click(within(table).getByRole("checkbox", { name: "Select Perko" }))
    fireEvent.click(within(screen.getByRole("toolbar", { name: "Change the selected people" })).getByRole("button", { name: /Add to workspace/ }))
    const dialog = await screen.findByRole("dialog", { name: "Add 2 people to a workspace" })
    expect(within(dialog).queryByRole("radio", { name: "Owner" })).toBeNull()
    await pick(within(dialog).getByRole("combobox", { name: "Workspace" }), /Lab/)
    fireEvent.click(within(dialog).getByRole("radio", { name: "Viewer" }))
    fireEvent.click(within(dialog).getByRole("button", { name: "Add 2" }))
    await waitFor(() => expect(calls(h.apiFetch, "PUT", "/workspaces/ws-b/members/")).toHaveLength(2))
    expect(body(h.apiFetch, "PUT", "/workspaces/ws-b/members/u-jana")).toEqual({ role: "VIEWER" })
  })
})

describe("Needs a hand", () => {
  const HAND = [
    ...USERS,
    { id: "u-none", email: "none@ex.com", full_name: "Nowhere", created_at: "2026-09-04T00:00:00Z", workspace: null, role: null, memberships: [], last_active_at: "2026-11-10T00:00:00Z" },
    ...Array.from({ length: 8 }, (_, i) => ({ id: `u-s${i}`, email: `s${i}@ex.com`, full_name: `Setup ${i}`, created_at: "2026-09-04T00:00:00Z", workspace: null, role: null, memberships: [m("ws-a", "Acme", "MEMBER")], setup_link_expires_at: FUTURE })),
  ]
  const EMPTY_ACCOUNT = { id: "ws-iv", name: "Ivana's Workspace", slug: "ivana-s-workspace", kind: "personal", created_at: "2026-09-04T00:00:00Z", _count_members: 1, _count_agents: 0, _count_crews: 0, runs_7d: 0, runs_by_day: [] }
  beforeEach(() => {
    window.history.replaceState(null, "", "/admin/people?view=hand")
    serve(h.apiFetch, { users: HAND, workspaces: [...WORKSPACES, EMPTY_ACCOUNT] })
  })

  it("shows the four cards, each capped at six rows with a link to the filtered list", async () => {
    render(<PeoplePage />)
    const locked = await screen.findByRole("region", { name: "Locked out · 1" })
    expect(within(locked).getByRole("button", { name: "Unlock" })).toBeInTheDocument()
    const setup = screen.getByRole("region", { name: "Have not chosen a password · 9" })
    expect(within(setup).getAllByRole("button", { name: "New setup link" })).toHaveLength(6)
    expect(setup).toHaveTextContent("6 of 9 shown")
    expect(screen.getByRole("region", { name: "In no workspace · 1" })).toBeInTheDocument()
    expect(screen.getByRole("region", { name: "Empty personal accounts · 1" })).toBeInTheDocument()
    fireEvent.click(within(setup).getByRole("button", { name: "Show all" }))
    await waitFor(() => expect(window.location.search).toContain("facet=setup"))
    expect(window.location.search).not.toContain("view=hand")
  })

  it("unlocks, issues a setup link shown once, voids and adds to a workspace", async () => {
    render(<PeoplePage />)
    const locked = await screen.findByRole("region", { name: "Locked out · 1" })
    fireEvent.click(within(locked).getByRole("button", { name: "Unlock" }))
    await waitFor(() => expect(calls(h.apiFetch, "POST", "/api/v1/admin/users/u-jana/unlock")).toHaveLength(1))

    const setup = screen.getByRole("region", { name: /Have not chosen a password/ })
    fireEvent.click(within(setup).getAllByRole("button", { name: "Void" })[0])
    await waitFor(() => expect(calls(h.apiFetch, "DELETE", "/setup-link")).toHaveLength(1))
    fireEvent.click(within(setup).getAllByRole("button", { name: "New setup link" })[0])
    const link = await screen.findByRole("dialog", { name: "Send them this link" })
    expect(within(link).getByLabelText("Setup link")).toHaveValue("https://x/reset-password?token=link")

    fireEvent.click(within(link).getByRole("button", { name: "Done" }))
    const nowhere = screen.getByRole("region", { name: "In no workspace · 1" })
    fireEvent.click(within(nowhere).getByRole("button", { name: "Add to a workspace" }))
    const dialog = await screen.findByRole("dialog", { name: "Add to a workspace" })
    await pick(within(dialog).getByRole("combobox", { name: "Workspace" }), /Acme/)
    fireEvent.click(within(dialog).getByRole("button", { name: "Add" }))
    await waitFor(() => expect(body(h.apiFetch, "PUT", "/workspaces/ws-a/members/u-none")).toEqual({ role: "MEMBER" }))
  })

  it("removes the empty personal accounts after asking", async () => {
    render(<PeoplePage />)
    const card = await screen.findByRole("region", { name: "Empty personal accounts · 1" })
    fireEvent.click(within(card).getByRole("button", { name: "Remove 1" }))
    expect(calls(h.apiFetch, "DELETE", "/workspaces/ws-iv")).toHaveLength(0)
    fireEvent.click(within(await screen.findByTestId("confirm-dialog")).getByRole("button", { name: "Remove" }))
    await waitFor(() => expect(body(h.apiFetch, "DELETE", "/workspaces/ws-iv")).toEqual({ confirm_slug: "ivana-s-workspace" }))
  })
})

describe("All workspaces", () => {
  it("lists workspace, owner, people, crews, runs and last activity; opens one", async () => {
    window.history.replaceState(null, "", "/admin/people?view=workspaces")
    render(<PeoplePage />)
    const card = await screen.findByRole("region", { name: "Workspaces" })
    expect(screen.getByText("2 workspaces · 3 people · 3 agents")).toBeInTheDocument()
    const rows = within(card).getAllByRole("row").slice(1)
    expect(rows[0]).toHaveTextContent("Acme")
    expect(rows[0]).toHaveTextContent("Boss")
    fireEvent.click(within(card).getByRole("radio", { name: "A–Z" }))
    fireEvent.click(within(card).getByRole("radio", { name: "Newest" }))
    expect(within(card).getAllByRole("row")[1]).toHaveTextContent("Lab")
    fireEvent.click(within(card).getByRole("row", { name: /Lab/ }))
    expect(await screen.findByRole("region", { name: "Members" })).toBeInTheDocument()
    expect(window.location.search).toContain("ws=ws-b")
  })

  it("marks a workspace nobody owns", async () => {
    window.history.replaceState(null, "", "/admin/people?view=workspaces")
    serve(h.apiFetch, { users: USERS.slice(0, 2), workspaces: WORKSPACES })
    render(<PeoplePage />)
    const card = await screen.findByRole("region", { name: "Workspaces" })
    expect(within(card).getByRole("row", { name: /Lab/ })).toHaveTextContent("No owner")
  })
})

describe("Who is where", () => {
  const WIDE = manyWorkspaces(8)
  beforeEach(() => {
    window.history.replaceState(null, "", "/admin/people?view=access")
  })

  it("takes access away from a cell, and keeps an only owner's cell shut", async () => {
    render(<PeoplePage />)
    const matrix = await screen.findByRole("region", { name: "Who is where" })
    expect(within(matrix).getByRole("button", { name: "Perko in Lab: Owner" })).toBeDisabled()
    expect(within(matrix).getByRole("button", { name: "Perko in Lab: Owner" })).toHaveAttribute("title", "The only owner; hand the workspace over first")
    fireEvent.click(within(matrix).getByRole("button", { name: "Jana in Acme: Member" }))
    fireEvent.change(within(matrix).getByRole("combobox", { name: "Jana in Acme" }), { target: { value: "" } })
    await waitFor(() => expect(calls(h.apiFetch, "DELETE", "/api/v1/admin/instance/workspaces/ws-a/members/u-jana")).toHaveLength(1))
  })

  it("draws at most six columns chosen in a searchable dialog, people in them first", async () => {
    const outsider = { ...USERS[1], id: "u-out", full_name: "Aaron", email: "aaron@ex.com", locked_until: null, memberships: [m("ws-9", "Team 09", "MEMBER")] }
    serve(h.apiFetch, { users: [outsider, ...USERS], workspaces: WIDE })
    render(<PeoplePage />)
    const matrix = await screen.findByRole("region", { name: "Who is where" })
    expect(within(matrix).getAllByRole("columnheader")).toHaveLength(6)
    expect(within(matrix).getByText("5 of 10 workspaces", { exact: false })).toBeInTheDocument()
    fireEvent.click(within(matrix).getByRole("button", { name: "Choose workspaces" }))
    const dialog = await screen.findByRole("dialog", { name: "Workspaces to compare" })
    fireEvent.change(within(dialog).getByLabelText("Search workspaces"), { target: { value: "lab" } })
    expect(within(dialog).getAllByRole("checkbox")).toHaveLength(1)
    fireEvent.click(within(dialog).getByRole("checkbox"))
    fireEvent.change(within(dialog).getByLabelText("Search workspaces"), { target: { value: "" } })
    // Five were drawn, Lab makes six: a seventh cannot be ticked.
    const boxes = within(dialog).getAllByRole("checkbox")
    expect(boxes.filter((b) => (b as HTMLInputElement).checked)).toHaveLength(6)
    expect(boxes.filter((b) => (b as HTMLInputElement).disabled && !(b as HTMLInputElement).checked).length).toBeGreaterThan(0)
    fireEvent.click(within(dialog).getByRole("button", { name: "Show" }))
    expect(within(matrix).getByRole("columnheader", { name: /Lab/ })).toBeInTheDocument()
    // Aaron is in none of the columns chosen: the people in them come first.
    const rows = within(matrix).getAllByRole("row").slice(1).map((r) => r.textContent ?? "")
    expect(rows.at(-1)).toMatch(/Aaron/)
  })
})

describe("personal accounts", () => {
  const IVANA = { id: "u-ivana", email: "ivana@ex.com", full_name: "Ivana Viewer", created_at: "2026-09-04T00:00:00Z", workspace: null, role: null,
    memberships: [m("ws-a", "Acme", "VIEWER"), m("ws-iv", "Ivana Viewer's Workspace", "OWNER")] }
  const IV_WS = { id: "ws-iv", name: "Ivana Viewer's Workspace", slug: "ivana-viewer-s-workspace", kind: "personal", created_at: "2026-09-04T00:00:00Z",
    _count_members: 1, _count_agents: 0, _count_crews: 0, runs_7d: 0, runs_by_day: [0, 0, 0, 0, 0, 0, 0] }
  beforeEach(() => serve(h.apiFetch, { users: [...USERS, IVANA], workspaces: [...WORKSPACES, IV_WS] }))

  it("lists a personal account under People, not among the workspaces, and finds it by its owner", async () => {
    render(<PeoplePage />)
    const table = await screen.findByRole("region", { name: "People" })
    const row = within(table).getByRole("row", { name: /Ivana Viewer/ })
    expect(row).toHaveTextContent("Personal account")
    expect(row).not.toHaveTextContent("Ivana Viewer's Workspace")
    expect(within(panel()).queryByRole("button", { name: /^Ivana Viewer/ })).toBeNull()
    fireEvent.change(screen.getByPlaceholderText(/Search people, workspaces/), { target: { value: "ivana" } })
    fireEvent.click(within(panel()).getByRole("button", { name: /^Personal accounts/ }))
    const card = await screen.findByRole("region", { name: "Personal accounts" })
    fireEvent.click(within(card).getByRole("row", { name: /Ivana Viewer/ }))
    expect(await screen.findByRole("radiogroup", { name: "Kind" })).toBeInTheDocument()
    expect(screen.getByRole("button", { name: "Personal accounts" })).toBeInTheDocument()
  })

  it("leaves personal accounts out of the access grid", async () => {
    window.history.replaceState(null, "", "/admin/people?view=access")
    render(<PeoplePage />)
    expect(await screen.findByRole("columnheader", { name: /Acme/ })).toBeInTheDocument()
    expect(screen.queryByRole("columnheader", { name: /Ivana Viewer's Workspace/ })).toBeNull()
  })

  it("ticks only the empty ones and removes them after asking", async () => {
    window.history.replaceState(null, "", "/admin/people?view=accounts")
    render(<PeoplePage />)
    const card = await screen.findByRole("region", { name: "Personal accounts" })
    expect(card).toHaveTextContent("1 empty")
    fireEvent.click(within(card).getByRole("checkbox", { name: /Ivana Viewer's personal account/ }))
    fireEvent.click(within(card).getByRole("button", { name: /Remove 1 empty/ }))
    expect(calls(h.apiFetch, "DELETE", "/api/v1/admin/instance/workspaces/ws-iv")).toHaveLength(0)
    const dialog = await screen.findByTestId("confirm-dialog")
    expect(within(dialog).getByText("Remove 1 empty personal account?")).toBeInTheDocument()
    expect(dialog.querySelector("[data-tone=lost]")).toHaveTextContent(/Only accounts with no crews, agents or data/)
    expect(dialog.querySelector("[data-tone=kept]")).toHaveTextContent(/keep signing in/)
    fireEvent.click(within(dialog).getByRole("button", { name: "Remove" }))
    await waitFor(() => expect(body(h.apiFetch, "DELETE", "/api/v1/admin/instance/workspaces/ws-iv")).toEqual({ confirm_slug: "ivana-viewer-s-workspace" }))
  })

  it("locks a personal account that has crews", async () => {
    window.history.replaceState(null, "", "/admin/people?view=accounts")
    serve(h.apiFetch, { users: [...USERS, IVANA], workspaces: [...WORKSPACES, { ...IV_WS, _count_crews: 2 }] })
    render(<PeoplePage />)
    const card = await screen.findByRole("region", { name: "Personal accounts" })
    expect(within(card).getByRole("checkbox", { name: /Ivana Viewer's personal account/ })).toBeDisabled()
  })
})

describe("adding", () => {
  it("creates a person with workspaces found by search, a role each, and shows the setup link once", async () => {
    render(<PeoplePage />)
    await screen.findByRole("region", { name: "People" })
    fireEvent.click(screen.getByRole("button", { name: "Add person" }))
    const dialog = await screen.findByRole("dialog", { name: "Add person" })
    fireEvent.change(within(dialog).getByLabelText("Email"), { target: { value: "new@ex.com" } })
    await pick(within(dialog).getByRole("combobox", { name: "Add a workspace" }), /Lab/)
    // Owner is never offered.
    expect(within(within(dialog).getByRole("radiogroup", { name: "Role in Lab" })).queryByRole("radio", { name: "Owner" })).toBeNull()
    fireEvent.click(within(within(dialog).getByRole("radiogroup", { name: "Role in Lab" })).getByRole("radio", { name: "Member" }))
    fireEvent.click(within(dialog).getByRole("button", { name: "Create and get link" }))
    await waitFor(() => expect(calls(h.apiFetch, "POST", "/api/v1/admin/instance/people")).toHaveLength(1))
    expect(body(h.apiFetch, "POST", "/api/v1/admin/instance/people")).toEqual({ email: "new@ex.com", full_name: "", memberships: [{ workspace_id: "ws-b", role: "MEMBER" }] })
    expect(await screen.findByLabelText("Setup link")).toHaveValue("https://x/reset-password?token=abc")
  })

  it("creates a workspace owned by the person chosen, with the agent language", async () => {
    render(<PeoplePage />)
    await screen.findByRole("region", { name: "People" })
    fireEvent.click(screen.getByRole("button", { name: "New workspace" }))
    const dialog = await screen.findByRole("dialog", { name: "New workspace" })
    fireEvent.change(within(dialog).getByLabelText("Name"), { target: { value: "Marketing Team" } })
    expect(within(dialog).getByLabelText("Address")).toHaveValue("marketing-team")
    await pick(within(dialog).getByRole("combobox", { name: "Owner" }), /Jana/)
    await pick(within(dialog).getByRole("combobox", { name: "Agent language" }), /Czech/)
    fireEvent.click(within(dialog).getByRole("button", { name: "Create workspace" }))
    await waitFor(() => expect(body(h.apiFetch, "POST", "/api/v1/admin/instance/workspaces")).toEqual({ name: "Marketing Team", slug: "marketing-team", owner_user_id: "u-jana", preferred_language: "Czech" }))
  })
})

describe("scope and failure", () => {
  it("keeps the own-workspace banner for someone who sees one workspace only", async () => {
    const base = h.apiFetch.getMockImplementation()!
    h.apiFetch.mockImplementation(async (u: string, init?: RequestInit) => {
      const r = (await base(u, init)) as { headers: { get: (k: string) => string | null } }
      if (String(u).startsWith("/api/v1/admin/users?")) return { ...r, headers: { get: () => "workspace" } }
      return r
    })
    render(<PeoplePage />)
    expect(await screen.findByText(/You see your own workspace only/)).toBeInTheDocument()
  })

  it("is the kit PageBanner (alert, bad tone) and Retry reads again", async () => {
    const normal = h.apiFetch.getMockImplementation()!
    let fail = true
    h.apiFetch.mockImplementation(async (u: string, init?: RequestInit) =>
      fail && String(u).startsWith("/api/v1/admin/users?") ? { ok: false, status: 500, json: async () => ({ error: "boom" }), headers: { get: () => null } } : normal(u, init))
    render(<PeoplePage />)
    const banner = await screen.findByRole("alert")
    expect(banner).toHaveAttribute("data-tone", "bad")
    expect(banner).toHaveClass("page-banner", "page-banner-bad")
    expect(document.querySelectorAll(".page-banner")).toHaveLength(1)
    fail = false
    fireEvent.click(within(banner).getByRole("button", { name: "Retry" }))
    await waitFor(() => expect(screen.queryByRole("alert")).toBeNull())
  })
})
