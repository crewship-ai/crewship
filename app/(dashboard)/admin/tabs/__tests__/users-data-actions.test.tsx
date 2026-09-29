// GDPR actions used to be their own nav row with its own user picker: a
// search box, a list of people, and two buttons — a second roster of the same
// humans the Users tab already lists, kept in step by hand.
//
// The actions belong to a PERSON, and people live on Users. Expanding a row
// puts the action where the thing it acts on already is, and the extra nav
// row (and the duplicate picker under it) goes away.

import { describe, it, expect, vi, beforeEach } from "vitest"
import { render, screen, fireEvent, waitFor, cleanup, within } from "@testing-library/react"

import { UsersTab } from "../users-tab"

const h = vi.hoisted(() => ({ apiFetch: vi.fn() }))

vi.mock("sonner", () => ({ toast: { success: vi.fn(), error: vi.fn(), info: vi.fn() } }))
vi.mock("@/lib/api-fetch", () => ({ apiFetch: (...args: unknown[]) => h.apiFetch(...args) }))

const USERS = [
  {
    id: "u-fredy", email: "pablosrbino@gmail.com", full_name: "Fredy", role: "MEMBER",
    created_at: "2026-07-29T13:06:17Z", workspace: { id: "ws-1", name: "Demo User's Workspace" },
  },
  {
    id: "u-demo", email: "demo@crewship.ai", full_name: "Demo User", role: "OWNER",
    created_at: "2026-07-23T13:15:24Z", workspace: { id: "ws-1", name: "Demo User's Workspace" },
  },
]

function renderTab(props: Record<string, unknown> = {}) {
  return render(<UsersTab users={USERS} workspaceId="ws-1" onRefresh={vi.fn()} {...props} />)
}

// A row opens the person in a drawer; the data actions are its Data tab.
function expandFredy() {
  fireEvent.click(screen.getByRole("button", { name: /fredy/i }))
  fireEvent.click(screen.getByRole("tab", { name: "Data" }))
}

beforeEach(() => {
  cleanup()
  h.apiFetch.mockReset()
  global.URL.createObjectURL = vi.fn(() => "blob:x")
  global.URL.revokeObjectURL = vi.fn()
})

describe("Users — a row opens what you can do to that person", () => {
  it("keeps the actions closed until a row is opened", () => {
    renderTab()
    expect(screen.queryByRole("button", { name: /export/i })).toBeNull()
  })

  it("reveals export and erase for the person whose row was opened", () => {
    renderTab()
    expandFredy()

    const panel = screen.getByRole("region", { name: /pablosrbino@gmail.com/i })
    expect(within(panel).getByRole("button", { name: /export/i })).toBeInTheDocument()
    expect(within(panel).getByRole("button", { name: /erase|delete/i })).toBeInTheDocument()
  })

  it("exports that person, scoped to the workspace", async () => {
    h.apiFetch.mockResolvedValue({ ok: true, status: 200, json: async () => ({ user: {} }), text: async () => "" })
    renderTab()
    expandFredy()
    fireEvent.click(screen.getByRole("button", { name: /export/i }))

    await waitFor(() => expect(h.apiFetch.mock.calls.some(([u]) => String(u).includes("/data"))).toBe(true))
    const [url] = h.apiFetch.mock.calls.find(([u]) => String(u).includes("/data")) as [string]
    expect(url).toContain("/api/v1/admin/users/u-fredy/data")
    expect(url).toContain("workspace_id=ws-1")
  })

  it("still demands a reason and an explicit confirmation before erasing", async () => {
    h.apiFetch.mockResolvedValue({ ok: true, status: 200, json: async () => ({ action_id: "ga_1" }), text: async () => "" })
    renderTab()
    expandFredy()
    fireEvent.click(screen.getByRole("button", { name: /erase|delete user data/i }))

    const dialog = await screen.findByRole("alertdialog")
    const confirm = within(dialog).getByRole("button", { name: /delete permanently|erase permanently/i })
    // Nothing typed, nothing ticked: the irreversible action stays shut.
    expect(confirm).toBeDisabled()

    fireEvent.change(within(dialog).getByLabelText(/reason/i), { target: { value: "SAR #1234" } })
    expect(confirm).toBeDisabled()
    fireEvent.click(within(dialog).getByRole("checkbox"))
    expect(confirm).toBeEnabled()
  })

  // One person at a time: the drawer is modal, and it names who it acts on.
  it("opens the drawer for the person whose row was clicked", () => {
    renderTab()
    expandFredy()
    const drawer = screen.getByRole("dialog")
    expect(within(drawer).getByText("pablosrbino@gmail.com")).toBeInTheDocument()
    expect(within(drawer).queryByText("demo@crewship.ai")).toBeNull()
  })
})

describe("Users — finding a person", () => {
  it("filters by name, email or id", () => {
    renderTab()
    fireEvent.change(screen.getByPlaceholderText(/search/i), { target: { value: "pablos" } })

    expect(screen.getByText("Fredy")).toBeInTheDocument()
    expect(screen.queryByText("Demo User")).toBeNull()
  })

  it("says so when nothing matches, rather than showing an empty table", () => {
    renderTab()
    fireEvent.change(screen.getByPlaceholderText(/search/i), { target: { value: "zzz" } })
    expect(screen.getByText(/no matching users/i)).toBeInTheDocument()
  })
})
