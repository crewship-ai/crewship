// Admin → GDPR actions. Both buttons on this panel failed every time they
// were pressed: the admin API is workspace-scoped by middleware, and neither
// the export nor the delete carried `workspace_id`, so the server answered
// 400 "workspace_id is required" before either handler ran. The panel is the
// one place in the admin console that can erase a person's data, and it had
// never worked.

import { describe, it, expect, vi, beforeEach } from "vitest"
import { render, screen, fireEvent, waitFor, cleanup, within } from "@testing-library/react"

import { GdprActionsPanel } from "../gdpr-actions-panel"

const h = vi.hoisted(() => ({ apiFetch: vi.fn() }))

vi.mock("sonner", () => ({ toast: { success: vi.fn(), error: vi.fn(), info: vi.fn() } }))
vi.mock("@/lib/api-fetch", () => ({ apiFetch: (...args: unknown[]) => h.apiFetch(...args) }))

const USERS = [
  {
    id: "e6d059ab-ed29-4e0c-a438-e6b221f8e072",
    email: "pablosrbino@gmail.com",
    full_name: "Fredy",
    role: "MEMBER",
    created_at: "2026-07-23T10:00:00Z",
    workspace: { id: "ws-1", name: "Demo User's Workspace" },
  },
]

function renderPanel(props: Record<string, unknown> = {}) {
  return render(<GdprActionsPanel users={USERS} workspaceId="ws-1" {...props} />)
}

function selectUser() {
  fireEvent.click(screen.getByRole("button", { name: /select .*pablosrbino/i }))
}

describe("GDPR actions — the calls carry the workspace", () => {
  beforeEach(() => {
    cleanup()
    h.apiFetch.mockReset()
    // jsdom has no object URLs or real downloads.
    global.URL.createObjectURL = vi.fn(() => "blob:x")
    global.URL.revokeObjectURL = vi.fn()
  })

  it("exports with workspace_id, so the request reaches the handler", async () => {
    h.apiFetch.mockResolvedValue({ ok: true, status: 200, json: async () => ({ user: {} }), text: async () => "" })
    renderPanel()
    selectUser()

    fireEvent.click(screen.getByRole("button", { name: /export json/i }))

    await waitFor(() => expect(h.apiFetch).toHaveBeenCalled())
    const [url] = h.apiFetch.mock.calls[0] as [string]
    expect(url).toContain("/api/v1/admin/users/")
    expect(url).toContain("workspace_id=ws-1")
  })

  it("deletes with workspace_id too", async () => {
    h.apiFetch.mockResolvedValue({ ok: true, status: 200, json: async () => ({ action_id: "a1" }), text: async () => "" })
    renderPanel()
    selectUser()

    fireEvent.click(screen.getByRole("button", { name: /^delete user data$/i }))
    const dialog = await screen.findByRole("alertdialog")
    fireEvent.change(within(dialog).getByLabelText(/reason/i), { target: { value: "SAR #1234" } })
    fireEvent.click(within(dialog).getByRole("checkbox"))
    fireEvent.click(within(dialog).getByRole("button", { name: /delete/i }))

    await waitFor(() => {
      const del = h.apiFetch.mock.calls.find(([, init]) => (init as RequestInit | undefined)?.method === "DELETE")
      expect(del).toBeTruthy()
      expect(String(del![0])).toContain("workspace_id=ws-1")
    })
  })

  // Without a workspace there is nothing to scope the action to, and the
  // request would 400. Better to say so than to offer a button that fails.
  it("does not offer the actions before a workspace resolves", () => {
    renderPanel({ workspaceId: null })
    selectUser()
    expect(screen.queryByRole("button", { name: /export json/i })).toBeNull()
  })
})

describe("GDPR actions — the panel explains itself", () => {
  beforeEach(() => { cleanup(); h.apiFetch.mockReset() })

  it("says what this is for in words an operator recognises", () => {
    renderPanel()
    // Not "data subject actions … logged as action='export' in gdpr_actions".
    // Someone lands here holding a request from a person, not a schema.
    expect(screen.getByText(/right to (access|erasure)|asks for a copy|asks to be erased/i)).toBeInTheDocument()
  })
})
