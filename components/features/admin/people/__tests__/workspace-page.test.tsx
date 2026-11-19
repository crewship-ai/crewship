// One workspace's page: its figures, members, kind, ownership and deletion,
// and what admins did to it.

import { describe, it, expect, vi, beforeEach } from "vitest"
import { render, screen, fireEvent, waitFor, cleanup, within } from "@testing-library/react"

const h = vi.hoisted(() => ({ apiFetch: vi.fn(), toastError: vi.fn() }))
vi.mock("sonner", () => ({ toast: { success: vi.fn(), error: (...a: unknown[]) => h.toastError(...a), info: vi.fn() } }))
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

const pick = async (trigger: HTMLElement, option: RegExp | string) => {
  fireEvent.click(trigger)
  fireEvent.click(within(await screen.findByRole("listbox")).getByRole("option", { name: option }))
}

beforeEach(() => {
  cleanup()
  stubDom()
  h.toastError.mockReset()
  window.history.replaceState(null, "", "/admin/people?ws=ws-a")
  serve(h.apiFetch)
})

describe("the head and the figures", () => {
  it("shows the address, dates, Rename, and Settings for the current workspace only", async () => {
    render(<PeoplePage />)
    const head = await screen.findByRole("region", { name: "Acme" })
    expect(head).toHaveTextContent("acme")
    expect(head).toHaveTextContent("Created")
    expect(within(head).getByRole("link", { name: "Settings" })).toHaveAttribute("href", "/settings?tab=general")
    cleanup()
    window.history.replaceState(null, "", "/admin/people?ws=ws-b")
    render(<PeoplePage />)
    const other = await screen.findByRole("region", { name: "Lab" })
    expect(within(other).queryByRole("link", { name: "Settings" })).toBeNull()
    expect(within(other).getByRole("button", { name: "Rename Lab" })).toBeInTheDocument()
  })

  it("shows four figures: people with the invited, crews with agents, runs and spend", async () => {
    render(<PeoplePage />)
    await screen.findByRole("region", { name: "Acme" })
    const figures = Array.from(document.querySelectorAll("[data-slot=settings-kpi]")).map((e) => e.textContent)
    expect(figures).toHaveLength(4)
    expect(figures[0]).toBe("People22 invited")
    expect(figures[1]).toBe("Crews13 agents")
    expect(figures[2]).toBe("Runs · 7 d4")
    expect(figures[3]).toBe("Spend · 30 d$12")
  })

  it("renames it; the address stays", async () => {
    render(<PeoplePage />)
    fireEvent.click(await screen.findByRole("button", { name: "Rename Acme" }))
    fireEvent.change(screen.getByLabelText("Workspace name"), { target: { value: "Acme Robotics" } })
    fireEvent.click(screen.getByRole("button", { name: "Save" }))
    await waitFor(() => expect(body(h.apiFetch, "PATCH", "/api/v1/admin/instance/workspaces/ws-a")).toEqual({ name: "Acme Robotics" }))
  })
})

describe("members", () => {
  it("changes a role, removes after asking, and keeps the only owner in place", async () => {
    render(<PeoplePage />)
    const card = await screen.findByRole("region", { name: "Members" })
    fireEvent.change(within(card).getByRole("combobox", { name: "Role of Jana" }), { target: { value: "VIEWER" } })
    await waitFor(() => expect(body(h.apiFetch, "PUT", "/workspaces/ws-a/members/u-jana")).toEqual({ role: "VIEWER" }))
    // Boss is the only owner: a pill, no role select, no remove.
    expect(within(card).queryByRole("combobox", { name: "Role of Boss" })).toBeNull()
    expect(within(card).getByRole("button", { name: "Remove Boss" })).toBeDisabled()
    fireEvent.click(within(card).getByRole("button", { name: "Remove Jana" }))
    fireEvent.click(within(await screen.findByRole("alertdialog", { name: "Remove Jana from Acme?" })).getByRole("button", { name: "Remove" }))
    await waitFor(() => expect(calls(h.apiFetch, "DELETE", "/workspaces/ws-a/members/u-jana")).toHaveLength(1))
  })

  it("pages the members 25 at a time", async () => {
    const many = Array.from({ length: 30 }, (_, i) => ({ id: `u-m${i}`, email: `m${i}@ex.com`, full_name: `Member ${String(i).padStart(2, "0")}`, created_at: "2026-09-04T00:00:00Z", workspace: null, role: "MEMBER", memberships: [m("ws-a", "Acme", "MEMBER")] }))
    serve(h.apiFetch, { users: [...USERS, ...many], workspaces: WORKSPACES })
    render(<PeoplePage />)
    const card = await screen.findByRole("region", { name: "Members" })
    expect(within(card).getAllByRole("button", { name: /^Remove / })).toHaveLength(25)
    fireEvent.click(within(card).getByRole("button", { name: /Show \d+ more/ }))
    expect(within(card).getAllByRole("button", { name: /^Remove / })).toHaveLength(32)
  })

  it("adds someone known with a role, and someone new with an account and a setup link", async () => {
    render(<PeoplePage />)
    const card = await screen.findByRole("region", { name: "Members" })
    fireEvent.click(within(card).getByRole("button", { name: "Add a person" }))
    fireEvent.change(within(card).getByLabelText("Person to add"), { target: { value: "perko@ex.com" } })
    fireEvent.click(within(card).getByRole("button", { name: "Add" }))
    await waitFor(() => expect(body(h.apiFetch, "PUT", "/workspaces/ws-a/members/u-perko")).toEqual({ role: "MEMBER" }))

    fireEvent.click(within(card).getByRole("button", { name: "Add a person" }))
    fireEvent.change(within(card).getByLabelText("Person to add"), { target: { value: "fresh@ex.com" } })
    fireEvent.click(within(card).getByRole("button", { name: "Add" }))
    await waitFor(() => expect(body(h.apiFetch, "POST", "/api/v1/admin/instance/people")).toEqual({ email: "fresh@ex.com", full_name: "", memberships: [{ workspace_id: "ws-a", role: "MEMBER" }] }))
    expect(await screen.findByLabelText("Setup link")).toHaveValue("https://x/reset-password?token=abc")
  })

  it("explains a page-ownership refusal instead of a bare toast", async () => {
    serve(h.apiFetch, { users: USERS, workspaces: WORKSPACES, overrides: (url, method) =>
      url.includes("/workspaces/ws-a/members/u-jana") && method === "DELETE"
        ? res({ error: "cannot remove this member: page ownership must be resolved first.", code: "page_ownership_transfer_required", pages: [{ id: "p1" }] }, 409) : undefined })
    render(<PeoplePage />)
    const card = await screen.findByRole("region", { name: "Members" })
    fireEvent.click(within(card).getByRole("button", { name: "Remove Jana" }))
    fireEvent.click(within(await screen.findByRole("alertdialog", { name: "Remove Jana from Acme?" })).getByRole("button", { name: "Remove" }))
    expect(await screen.findByRole("alertdialog", { name: "Pages need a new owner first" })).toHaveTextContent("Jana owns 1 page in Acme")
    expect(h.toastError).not.toHaveBeenCalled()
  })

  it("lists who has not chosen a password under Waiting for setup", async () => {
    window.history.replaceState(null, "", "/admin/people?ws=ws-b")
    render(<PeoplePage />)
    const card = await screen.findByRole("region", { name: "Waiting for setup" })
    expect(card).toHaveTextContent("Perko")
    fireEvent.click(within(card).getByRole("button", { name: "New link" }))
    expect(await screen.findByRole("dialog", { name: "Send them this link" })).toBeInTheDocument()
  })
})

describe("kind, ownership and deletion", () => {
  it("saves the kind at once", async () => {
    render(<PeoplePage />)
    const kind = await screen.findByRole("radiogroup", { name: "Kind" })
    expect(within(kind).getByRole("button", { name: /^Workspace/ })).toHaveAttribute("aria-pressed", "true")
    fireEvent.click(within(kind).getByRole("button", { name: /^Personal account/ }))
    await waitFor(() => expect(body(h.apiFetch, "PATCH", "/api/v1/admin/instance/workspaces/ws-a")).toEqual({ kind: "personal" }))
  })

  it("hands itself over after asking, and deletes only once its address is typed", async () => {
    render(<PeoplePage />)
    const zone = await screen.findByRole("region", { name: "Danger zone" })
    await pick(within(zone).getByRole("combobox", { name: "New owner" }), /Jana/)
    fireEvent.click(within(zone).getByRole("button", { name: "Transfer" }))
    const transfer = await screen.findByRole("alertdialog", { name: "Make Jana the owner of Acme?" })
    fireEvent.click(within(transfer).getByRole("button", { name: "Transfer" }))
    await waitFor(() => expect(body(h.apiFetch, "POST", "/api/v1/admin/instance/workspaces/ws-a/transfer-ownership")).toEqual({ user_id: "u-jana" }))
    await waitFor(() => expect(screen.queryByRole("alertdialog")).toBeNull())

    fireEvent.click(within(zone).getByRole("button", { name: /Delete…/ }))
    const del = await screen.findByRole("alertdialog", { name: "Delete Acme?" })
    const go = within(del).getByRole("button", { name: "Delete Acme" })
    expect(go).toBeDisabled()
    fireEvent.change(within(del).getByLabelText(/Type/), { target: { value: "acme" } })
    fireEvent.click(go)
    await waitFor(() => expect(body(h.apiFetch, "DELETE", "/api/v1/admin/instance/workspaces/ws-a")).toEqual({ confirm_slug: "acme" }))
  })

  it("shows the server's refusal when ownership would go to a Guest", async () => {
    serve(h.apiFetch, { users: USERS, workspaces: WORKSPACES, overrides: (url) =>
      url.endsWith("/transfer-ownership") ? res({ error: "Restore trusted access explicitly before transferring ownership to a Guest" }, 409) : undefined })
    render(<PeoplePage />)
    const zone = await screen.findByRole("region", { name: "Danger zone" })
    await pick(within(zone).getByRole("combobox", { name: "New owner" }), /Jana/)
    fireEvent.click(within(zone).getByRole("button", { name: "Transfer" }))
    fireEvent.click(within(await screen.findByRole("alertdialog")).getByRole("button", { name: "Transfer" }))
    await waitFor(() => expect(h.toastError).toHaveBeenCalled())
    expect(String(h.toastError.mock.calls[0][0])).toContain("transferring ownership to a Guest")
    // The confirmation stays open.
    expect(screen.getByRole("alertdialog", { name: "Make Jana the owner of Acme?" })).toBeInTheDocument()
  })
})

describe("admin actions on this workspace", () => {
  it("reads the audit log by the workspace id", async () => {
    serve(h.apiFetch, { users: USERS, workspaces: WORKSPACES, audit: [
      { id: "a", user_id: "u-boss", user_email: "boss@ex.com", action: "instance.workspace_updated", entity_type: "workspace", entity_id: "ws-a", target_workspace_id: "ws-a", metadata: JSON.stringify({ name: { from: "Mktg", to: "Acme" } }), created_at: "2026-11-12T10:00:00Z" },
    ] })
    render(<PeoplePage />)
    const card = await screen.findByRole("region", { name: "Admin actions on this workspace" })
    expect(await within(card).findByText("Renamed from “Mktg” to “Acme”")).toBeInTheDocument()
    expect(card).toHaveTextContent("by Boss")
    expect(calls(h.apiFetch, "GET", "/api/v1/admin/instance/audit?entity_id=ws-a&limit=100")).toHaveLength(1)
  })
})
