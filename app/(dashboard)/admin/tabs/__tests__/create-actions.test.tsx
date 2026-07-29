// Admin → Workspaces and Admin → Users were read-only lists. Every other
// admin surface can act on what it shows; these two could only report that a
// workspace or a person existed, and the way to add either was somewhere else
// entirely — the workspace switcher in the top bar, or Settings → Members.
//
// Both now carry the SAME dialog those surfaces use, rather than a second
// implementation of "create a workspace" that would drift from the first.

import { describe, it, expect, vi, beforeEach } from "vitest"
import { render, screen, fireEvent, waitFor, cleanup, within } from "@testing-library/react"

import { WorkspacesTab } from "../workspaces-tab"
import { UsersTab } from "../users-tab"

const h = vi.hoisted(() => ({ apiFetch: vi.fn() }))

vi.mock("sonner", () => ({ toast: { success: vi.fn(), error: vi.fn(), info: vi.fn() } }))
vi.mock("@/lib/api-fetch", () => ({ apiFetch: (...args: unknown[]) => h.apiFetch(...args) }))

const ORGS = [
  {
    id: "ws-1", name: "Demo User's Workspace", slug: "demo-cmrxjalm",
    created_at: "2026-07-23T10:00:00Z",
    _count_members: 2, _count_agents: 8, _count_crews: 4,
  },
]

const USERS = [
  {
    id: "u-1", email: "demo@crewship.ai", full_name: "Demo", role: "OWNER",
    created_at: "2026-07-23T10:00:00Z", workspace: { id: "ws-1", name: "Demo User's Workspace" },
  },
]

describe("Admin → Workspaces can create one", () => {
  beforeEach(() => { cleanup(); h.apiFetch.mockReset() })

  it("offers the action beside the list it acts on", () => {
    render(<WorkspacesTab orgs={ORGS} onRefresh={vi.fn()} />)
    expect(screen.getByRole("button", { name: /create workspace/i })).toBeInTheDocument()
  })

  it("opens the same dialog the workspace switcher uses", async () => {
    render(<WorkspacesTab orgs={ORGS} onRefresh={vi.fn()} />)
    fireEvent.click(screen.getByRole("button", { name: /create workspace/i }))

    const dialog = await screen.findByRole("dialog")
    expect(within(dialog).getByLabelText(/name/i)).toBeInTheDocument()
    expect(within(dialog).getByLabelText(/slug/i)).toBeInTheDocument()
  })

  it("creates the workspace and refreshes the list", async () => {
    const onRefresh = vi.fn()
    h.apiFetch.mockResolvedValue({
      ok: true, status: 201,
      json: async () => ({ id: "ws-2", name: "Acme Engineering" }),
      text: async () => "",
    })
    render(<WorkspacesTab orgs={ORGS} onRefresh={onRefresh} />)
    fireEvent.click(screen.getByRole("button", { name: /create workspace/i }))

    const dialog = await screen.findByRole("dialog")
    fireEvent.change(within(dialog).getByLabelText(/name/i), { target: { value: "Acme Engineering" } })
    fireEvent.click(within(dialog).getByRole("button", { name: /^create workspace$/i }))

    await waitFor(() => {
      const post = h.apiFetch.mock.calls.find(([, init]) => (init as RequestInit | undefined)?.method === "POST")
      expect(post).toBeTruthy()
      expect(String(post![0])).toContain("/api/v1/workspaces")
    })
    // The list the admin is looking at must catch up on its own — a created
    // workspace that does not appear reads as a failed create.
    await waitFor(() => expect(onRefresh).toHaveBeenCalled())
  })

  // The slug is what URLs and API calls use, so it is derived rather than
  // asked for twice — the same behaviour the switcher's dialog has.
  it("derives the slug from the name", async () => {
    render(<WorkspacesTab orgs={ORGS} onRefresh={vi.fn()} />)
    fireEvent.click(screen.getByRole("button", { name: /create workspace/i }))

    const dialog = await screen.findByRole("dialog")
    fireEvent.change(within(dialog).getByLabelText(/name/i), { target: { value: "Acme Engineering" } })
    expect((within(dialog).getByLabelText(/slug/i) as HTMLInputElement).value).toBe("acme-engineering")
  })
})

describe("Admin → Users can add one", () => {
  beforeEach(() => { cleanup(); h.apiFetch.mockReset() })

  it("offers the action beside the list it acts on", () => {
    render(<UsersTab users={USERS} workspaceId="ws-1" onRefresh={vi.fn()} />)
    expect(screen.getByRole("button", { name: /add member|invite/i })).toBeInTheDocument()
  })

  it("opens the same invite dialog Settings uses", async () => {
    render(<UsersTab users={USERS} workspaceId="ws-1" onRefresh={vi.fn()} />)
    fireEvent.click(screen.getByRole("button", { name: /add member|invite/i }))

    const dialog = await screen.findByRole("dialog")
    expect(within(dialog).getByLabelText(/email/i)).toBeInTheDocument()
  })

  // Without a workspace there is nothing to invite INTO, and a button that
  // can only fail is worse than no button.
  it("does not offer the action before a workspace is resolved", () => {
    render(<UsersTab users={USERS} workspaceId={null} onRefresh={vi.fn()} />)
    expect(screen.queryByRole("button", { name: /add member|invite/i })).toBeNull()
  })
})
