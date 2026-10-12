import { describe, it, expect, vi, beforeEach } from "vitest"
import { render, screen, fireEvent, cleanup } from "@testing-library/react"

beforeEach(() => {
  cleanup()
  if (!Element.prototype.hasPointerCapture) Element.prototype.hasPointerCapture = () => false
  if (!Element.prototype.releasePointerCapture) Element.prototype.releasePointerCapture = () => {}
  if (!Element.prototype.scrollIntoView) Element.prototype.scrollIntoView = () => {}
})

let role = "OWNER"
vi.mock("@/hooks/use-workspace", () => ({
  useWorkspace: () => ({
    workspace: { id: "ws-1", name: "Demo User's Workspace", logo_url: null },
    workspaces: [
      { id: "ws-1", name: "Demo User's Workspace", slug: "demo", currentUserRole: "OWNER" },
      { id: "ws-2", name: "Acme", slug: "acme", currentUserRole: "MEMBER" },
    ],
    role,
    loading: false,
    setWorkspaceId: vi.fn(),
    refresh: vi.fn(),
  }),
}))

import { MobileWorkspaceSwitcher } from "../workspace-switcher"

function open() {
  render(<MobileWorkspaceSwitcher />)
  fireEvent.pointerDown(screen.getByRole("button", { name: /current workspace/i }), { button: 0, ctrlKey: false })
}

describe("MobileWorkspaceSwitcher", () => {
  it("names the real workspace instead of a placeholder", () => {
    render(<MobileWorkspaceSwitcher />)
    expect(screen.getByRole("button", { name: "Current workspace: Demo User's Workspace" })).toBeInTheDocument()
    expect(screen.queryByText("Unify Technology")).not.toBeInTheDocument()
  })

  it("lists every workspace plus its settings and people", async () => {
    role = "OWNER"
    open()
    expect(await screen.findByText("Acme")).toBeInTheDocument()
    expect(screen.getByRole("menuitem", { name: "Workspace settings" })).toHaveAttribute("href", "/settings?tab=general")
    expect(screen.getByRole("menuitem", { name: "Invite members" })).toHaveAttribute("href", "/settings?tab=members")
    expect(screen.getByRole("menuitem", { name: "Create workspace" })).toBeInTheDocument()
  })

  it("promises an invite only to roles that can send one", async () => {
    role = "MEMBER"
    open()
    expect(await screen.findByRole("menuitem", { name: "Members" })).toHaveAttribute("href", "/settings?tab=members")
    expect(screen.queryByRole("menuitem", { name: "Invite members" })).not.toBeInTheDocument()
  })
})
