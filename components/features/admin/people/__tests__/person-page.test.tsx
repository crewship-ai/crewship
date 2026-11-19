// One person's page: where they work, how they sign in, whether they run the
// instance, their data, what admins did to the account, and suspending them.
// Includes the role gates the page enforces for each kind of person.

import { describe, it, expect, vi, beforeEach } from "vitest"
import { render, screen, fireEvent, waitFor, cleanup, within } from "@testing-library/react"

const h = vi.hoisted(() => ({ apiFetch: vi.fn(), toastError: vi.fn(), toastSuccess: vi.fn() }))
vi.mock("sonner", () => ({ toast: { success: (...a: unknown[]) => h.toastSuccess(...a), error: (...a: unknown[]) => h.toastError(...a), info: vi.fn() } }))
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
import { USERS, WORKSPACES, body, calls, m, res, serve, stubDom } from "./fixtures"

const ENV_OWNER = { id: "u-env", email: "owner@ex.com", full_name: "Env Owner", created_at: "2026-08-01T00:00:00Z", workspace: null, role: "OWNER",
  memberships: [m("ws-a", "Acme", "ADMIN")], instance_admin: true, instance_admin_source: "env", last_active_at: "2026-11-18T00:00:00Z" }
const SUSPENDED = { id: "u-susp", email: "gone@ex.com", full_name: "Gone Person", created_at: "2026-08-01T00:00:00Z", workspace: null, role: "MEMBER",
  memberships: [m("ws-a", "Acme", "MEMBER")], suspended_at: "2026-11-02T00:00:00Z", suspended_reason: "left the company", last_active_at: "2026-10-01T00:00:00Z" }
const GUEST = { id: "u-guest", email: "guest@ex.com", full_name: "Guest Person", created_at: "2026-08-01T00:00:00Z", workspace: null, role: "MEMBER",
  memberships: [m("ws-a", "Acme", "MEMBER")], last_active_at: "2026-11-02T00:00:00Z" }
const ALL = [...USERS, ENV_OWNER, SUSPENDED, GUEST]

const open = async (name: string) => {
  fireEvent.click(await screen.findByRole("row", { name: new RegExp(name) }))
  return screen.findByRole("region", { name: "Workspaces" })
}
beforeEach(() => {
  cleanup()
  stubDom()
  h.toastError.mockReset()
  h.toastSuccess.mockReset()
  window.history.replaceState(null, "", "/admin/people")
  serve(h.apiFetch, { users: ALL, workspaces: WORKSPACES })
})

describe("the head and the way back", () => {
  it("shows who they are, and goes back to the view they were opened from", async () => {
    window.history.replaceState(null, "", "/admin/people?view=admins")
    render(<PeoplePage />)
    await open("Boss")
    const head = screen.getByRole("region", { name: "Boss" })
    expect(head).toHaveTextContent("boss@ex.com")
    expect(head).toHaveTextContent("Joined")
    expect(head).toHaveTextContent("Last active")
    expect(head).toHaveTextContent("Instance admin")
    expect(window.location.search).toContain("person=u-boss")
    fireEvent.click(screen.getByRole("button", { name: "People" }))
    expect(await screen.findByText("People who run this server: other people, every workspace, limits and backups.")).toBeInTheDocument()
    expect(window.location.search).not.toContain("person=")
  })
})

describe("a person's workspaces", () => {
  it("adds them to a workspace they are not in through the searchable picker", async () => {
    render(<PeoplePage />)
    const card = await open("Jana")
    fireEvent.click(within(card).getByRole("button", { name: /Add Jana to a workspace/ }))
    const dialog = await screen.findByRole("dialog", { name: "Add to a workspace" })
    // Only the workspaces they are not in are offered.
    fireEvent.click(within(dialog).getByRole("combobox", { name: "Workspace" }))
    expect(within(await screen.findByRole("listbox")).getAllByRole("option")).toHaveLength(1)
    fireEvent.click(within(screen.getByRole("listbox")).getByRole("option", { name: /Lab/ }))
    fireEvent.click(within(dialog).getByRole("radio", { name: "Admin" }))
    fireEvent.click(within(dialog).getByRole("button", { name: "Add" }))
    await waitFor(() => expect(body(h.apiFetch, "PUT", "/workspaces/ws-b/members/u-jana")).toEqual({ role: "ADMIN" }))
  })

  it("changes a role in place and removes only after asking", async () => {
    render(<PeoplePage />)
    const card = await open("Jana")
    fireEvent.change(within(card).getByRole("combobox", { name: "Role in Acme" }), { target: { value: "MANAGER" } })
    await waitFor(() => expect(body(h.apiFetch, "PUT", "/workspaces/ws-a/members/u-jana")).toEqual({ role: "MANAGER" }))
    fireEvent.click(within(card).getByRole("button", { name: "Remove from Acme" }))
    expect(calls(h.apiFetch, "DELETE", "/members/u-jana")).toHaveLength(0)
    const dialog = await screen.findByRole("alertdialog", { name: "Remove Jana from Acme?" })
    fireEvent.click(within(dialog).getByRole("button", { name: "Remove" }))
    await waitFor(() => expect(calls(h.apiFetch, "DELETE", "/api/v1/admin/instance/workspaces/ws-a/members/u-jana")).toHaveLength(1))
  })

  it("explains a page-ownership refusal and what to do instead", async () => {
    const base = h.apiFetch.getMockImplementation()!
    h.apiFetch.mockImplementation(async (u: string, init?: RequestInit) =>
      String(u).includes("/workspaces/ws-a/members/u-jana") && init?.method === "DELETE"
        ? res({ error: "cannot remove this member: page ownership must be resolved first. Ask a workspace administrator to resolve ownership.", code: "page_ownership_transfer_required", pages: [{ id: "p1" }, { id: "p2" }] }, 409)
        : base(u, init))
    render(<PeoplePage />)
    const card = await open("Jana")
    fireEvent.click(within(card).getByRole("button", { name: "Remove from Acme" }))
    fireEvent.click(within(await screen.findByRole("alertdialog", { name: "Remove Jana from Acme?" })).getByRole("button", { name: "Remove" }))
    const notice = await screen.findByRole("alertdialog", { name: "Pages need a new owner first" })
    expect(notice).toHaveTextContent("page ownership must be resolved first")
    expect(notice).toHaveTextContent("Jana owns 2 pages in Acme")
    expect(notice).toHaveTextContent("crewship workspace page-ownership transfer")
    // Not a bare toast.
    expect(h.toastError).not.toHaveBeenCalled()
  })
})

describe("sign-in", () => {
  it("issues a new setup link for someone who has not chosen a password, shown once in a dialog", async () => {
    render(<PeoplePage />)
    await open("Perko")
    fireEvent.click(screen.getByRole("button", { name: "New setup link" }))
    await waitFor(() => expect(calls(h.apiFetch, "POST", "/api/v1/admin/instance/people/u-perko/setup-link")).toHaveLength(1))
    const dialog = await screen.findByRole("dialog", { name: "Send them this link" })
    expect(within(dialog).getByLabelText("Setup link")).toHaveValue("https://x/reset-password?token=link")
  })

  it("does not claim a person who never signed in chose a password", async () => {
    serve(h.apiFetch, { users: [...ALL, { ...USERS[2], id: "u-ghost", full_name: "Ghost", email: "ghost@ex.com", setup_link_expires_at: null, last_active_at: null, memberships: [m("ws-a", "Acme", "MEMBER")] }], workspaces: WORKSPACES })
    render(<PeoplePage />)
    await open("Ghost")
    const signIn = screen.getByRole("region", { name: "Sign-in" })
    expect(signIn).not.toHaveTextContent("Chosen by the person")
    expect(signIn).toHaveTextContent("Never signed in")
    fireEvent.click(within(signIn).getByRole("button", { name: "Issue setup link" }))
    await waitFor(() => expect(calls(h.apiFetch, "POST", "/api/v1/admin/instance/people/u-ghost/setup-link")).toHaveLength(1))
  })

  it("unlocks a locked account", async () => {
    render(<PeoplePage />)
    await open("Jana")
    const signIn = screen.getByRole("region", { name: "Sign-in" })
    expect(signIn).toHaveTextContent("Locked after 50 failed sign-ins")
    fireEvent.click(within(signIn).getByRole("button", { name: "Unlock" }))
    await waitFor(() => expect(calls(h.apiFetch, "POST", "/api/v1/admin/users/u-jana/unlock")).toHaveLength(1))
  })
})

describe("admin actions on this account", () => {
  const ROWS = [
    { id: "a2", user_id: "u-boss", user_email: "boss@ex.com", action: "instance.member_added", entity_type: "workspace_member", entity_id: "u-jana", target_workspace_id: "ws-b", metadata: JSON.stringify({ role: "MEMBER" }), created_at: "2026-11-18T10:00:00Z" },
    { id: "a1", user_id: "u-boss", user_email: "boss@ex.com", action: "instance.user_created", entity_type: "user", entity_id: "u-jana", target_workspace_id: null, metadata: "{}", created_at: "2026-10-01T10:00:00Z" },
  ]
  it("reads the audit log by the person's id, newest first, and checks the chain on demand", async () => {
    serve(h.apiFetch, { users: ALL, workspaces: WORKSPACES, audit: [ROWS[1], ROWS[0]] })
    render(<PeoplePage />)
    await open("Jana")
    const card = await screen.findByRole("region", { name: "Admin actions on this account" })
    expect(await within(card).findByText("Jana added to Lab as Member")).toBeInTheDocument()
    expect(calls(h.apiFetch, "GET", "/api/v1/admin/instance/audit?entity_id=u-jana&limit=100")).toHaveLength(1)
    const texts = within(card).getAllByText(/Jana/).map((e) => e.textContent)
    expect(texts[0]).toBe("Jana added to Lab as Member")
    expect(texts[1]).toBe("Created the account of Jana")
    fireEvent.click(within(card).getByRole("button", { name: "Check now" }))
    expect(await within(card).findByText("Chain intact")).toBeInTheDocument()
    expect(calls(h.apiFetch, "GET", "/api/v1/admin/instance/audit/verify")).toHaveLength(1)
  })

  it("says so when the chain does not verify", async () => {
    serve(h.apiFetch, { users: ALL, workspaces: WORKSPACES, verify: { ok: false, broken_seq: 41, reason: "hash mismatch" } })
    render(<PeoplePage />)
    await open("Jana")
    const card = await screen.findByRole("region", { name: "Admin actions on this account" })
    fireEvent.click(within(card).getByRole("button", { name: "Check now" }))
    expect(await within(card).findByText(/Chain broken at entry 41 · hash mismatch/)).toBeInTheDocument()
  })
})

describe("suspending", () => {
  it("asks through the shared confirmation that says sessions and CLI tokens stay ended, then suspends", async () => {
    render(<PeoplePage />)
    await open("Jana")
    const zone = screen.getByRole("region", { name: "Suspend account" })
    fireEvent.click(within(zone).getByRole("button", { name: /Suspend/ }))
    expect(calls(h.apiFetch, "POST", "/suspend")).toHaveLength(0)
    const dialog = await screen.findByRole("alertdialog", { name: "Suspend Jana?" })
    expect(dialog).toHaveTextContent("Their sessions and CLI tokens end first, and stay ended after a reactivation")
    fireEvent.change(within(dialog).getByLabelText(/Reason/), { target: { value: "left the company" } })
    fireEvent.click(within(dialog).getByRole("button", { name: "Suspend Jana" }))
    await waitFor(() => expect(body(h.apiFetch, "POST", "/api/v1/admin/instance/people/u-jana/suspend")).toEqual({ reason: "left the company" }))
  })

  it("reactivates a suspended account", async () => {
    render(<PeoplePage />)
    await open("Gone Person")
    const zone = screen.getByRole("region", { name: "Suspended" })
    expect(zone).toHaveTextContent("Reason: left the company")
    fireEvent.click(within(zone).getByRole("button", { name: "Reactivate" }))
    await waitFor(() => expect(calls(h.apiFetch, "POST", "/api/v1/admin/instance/people/u-susp/reactivate")).toHaveLength(1))
  })
})

// ── Role gates ──────────────────────────────────────────────────────────────
// Who may do what to whom. The page is behind the instance-admin gate; inside
// it, these are the rules it enforces itself and the ones it surfaces from the
// server's 409.
describe("role gates", () => {
  it("yourself: no suspend, and an instance admin named by role cannot remove themselves", async () => {
    render(<PeoplePage />)
    await open("Boss")
    expect(within(screen.getByRole("region", { name: "Suspend account" })).getByRole("button", { name: /Suspend/ })).toBeDisabled()
    expect(screen.getByRole("region", { name: "Suspend account" })).toHaveTextContent("You cannot suspend yourself")
    const admin = screen.getByRole("region", { name: "Instance administration" })
    expect(within(admin).getByRole("button", { name: "Remove" })).toBeDisabled()
    expect(admin).toHaveTextContent("Another instance admin has to remove you")
  })

  it("the env-defined owner: read-only admin source, not tickable, no suspend", async () => {
    render(<PeoplePage />)
    const table = await screen.findByRole("region", { name: "People" })
    expect(within(table).getByRole("checkbox", { name: "Select Env Owner" })).toBeDisabled()
    expect(within(table).getByRole("checkbox", { name: "Select Boss" })).toBeDisabled()
    fireEvent.click(within(table).getByRole("row", { name: /Env Owner/ }))
    const admin = await screen.findByRole("region", { name: "Instance administration" })
    expect(admin).toHaveTextContent("Instance owner, set on the server (CREWSHIP_OWNER_EMAIL)")
    expect(within(admin).getByText("Set on the server")).toBeInTheDocument()
    expect(within(admin).queryByRole("button")).toBeNull()
    expect(within(screen.getByRole("region", { name: "Suspend account" })).getByRole("button", { name: /Suspend/ })).toBeDisabled()
  })

  it("a suspended person cannot become an instance admin, and says why", async () => {
    render(<PeoplePage />)
    await open("Gone Person")
    const admin = screen.getByRole("region", { name: "Instance administration" })
    expect(within(admin).getByRole("button", { name: "Make instance admin" })).toBeDisabled()
    expect(admin).toHaveTextContent("A suspended account cannot become an instance admin")
  })

  it("anyone else can be made an instance admin", async () => {
    render(<PeoplePage />)
    await open("Guest Person")
    fireEvent.click(within(screen.getByRole("region", { name: "Instance administration" })).getByRole("button", { name: "Make instance admin" }))
    await waitFor(() => expect(calls(h.apiFetch, "PUT", "/api/v1/admin/instance/admins/u-guest")).toHaveLength(1))
  })

  it("a Guest cannot be an Admin or Owner: the server's refusal is shown, not swallowed", async () => {
    const base = h.apiFetch.getMockImplementation()!
    h.apiFetch.mockImplementation(async (u: string, init?: RequestInit) =>
      String(u).includes("/workspaces/ws-a/members/u-guest") && init?.method === "PUT"
        ? res({ error: "Restore trusted access explicitly before promoting a Guest to administrator" }, 409)
        : base(u, init))
    render(<PeoplePage />)
    const card = await open("Guest Person")
    fireEvent.change(within(card).getByRole("combobox", { name: "Role in Acme" }), { target: { value: "ADMIN" } })
    await waitFor(() => expect(h.toastError).toHaveBeenCalled())
    expect(String(h.toastError.mock.calls[0][0])).toContain("Restore trusted access explicitly")
  })

  it("the last owner of a workspace keeps the role and cannot be removed", async () => {
    render(<PeoplePage />)
    const card = await open("Perko")
    expect(within(card).queryByRole("combobox", { name: "Role in Lab" })).toBeNull()
    expect(card).toHaveTextContent("Only owner · hand it over from the workspace")
    expect(within(card).getByRole("button", { name: "Remove from Lab" })).toBeDisabled()
  })

  it("the last active instance admin cannot be suspended or removed: the server's 409 is shown and nothing changes", async () => {
    const solo = { ...GUEST, id: "u-solo", full_name: "Solo Admin", email: "solo@ex.com", instance_admin: true, instance_admin_source: "role" }
    serve(h.apiFetch, { users: [...ALL, solo], workspaces: WORKSPACES, overrides: (url, method) =>
      (url.includes("/people/u-solo/suspend") || (url.includes("/admins/u-solo") && method === "DELETE"))
        ? res({ error: "the last active instance administrator cannot be suspended or removed" }, 409) : undefined })
    render(<PeoplePage />)
    await open("Solo Admin")
    fireEvent.click(within(screen.getByRole("region", { name: "Instance administration" })).getByRole("button", { name: "Remove" }))
    await waitFor(() => expect(h.toastError).toHaveBeenCalledTimes(1))
    expect(String(h.toastError.mock.calls[0][0])).toContain("last active instance administrator")

    fireEvent.click(within(screen.getByRole("region", { name: "Suspend account" })).getByRole("button", { name: /Suspend/ }))
    const dialog = await screen.findByRole("alertdialog", { name: "Suspend Solo Admin?" })
    fireEvent.click(within(dialog).getByRole("button", { name: "Suspend Solo" }))
    await waitFor(() => expect(h.toastError).toHaveBeenCalledTimes(2))
    // The confirmation stays open after a refusal.
    expect(screen.getByRole("alertdialog", { name: "Suspend Solo Admin?" })).toBeInTheDocument()
  })
})
