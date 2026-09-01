// Pins a CodeRabbit finding on #2254: useWorkspace().role is null while the
// workspace snapshot loads, and isAdminTier(null) is false — so without a
// check on useWorkspace().loading, an OWNER/ADMIN would flash "Approvals is
// an admin surface" on every load, before their real role resolves.

import { describe, it, expect, vi } from "vitest"
import { render, screen } from "@testing-library/react"

const h = vi.hoisted(() => ({
  workspaceId: "ws_1" as string | null,
  role: null as string | null,
  workspaceLoading: true,
}))

vi.mock("@/hooks/use-workspace", () => ({
  useWorkspace: () => ({
    workspaceId: h.workspaceId,
    role: h.role,
    loading: h.workspaceLoading,
  }),
}))

vi.mock("@/hooks/use-approvals", () => ({
  useApprovals: () => ({
    rows: [],
    loading: false,
    error: null,
    notConfigured: false,
    refresh: vi.fn(),
    patchRow: vi.fn(),
  }),
}))

vi.mock("next/navigation", () => ({
  useSearchParams: () => new URLSearchParams(),
}))

import ApprovalsPage from "../page"

describe("ApprovalsPage — workspace-loading gate", () => {
  it("shows a loading state, not the admin-only denial, while the workspace is still loading", () => {
    h.role = null
    h.workspaceLoading = true

    render(<ApprovalsPage />)

    expect(screen.queryByText(/admin surface/i)).not.toBeInTheDocument()
  })

  it("shows the admin-only denial once loading finishes and the role is not admin", () => {
    h.role = "MEMBER"
    h.workspaceLoading = false

    render(<ApprovalsPage />)

    expect(screen.getByText(/admin surface/i)).toBeInTheDocument()
  })

  it("renders the approvals list once loading finishes and the role is an admin tier", () => {
    h.role = "OWNER"
    h.workspaceLoading = false

    render(<ApprovalsPage />)

    expect(screen.queryByText(/admin surface/i)).not.toBeInTheDocument()
    expect(screen.getByText("Approvals")).toBeInTheDocument()
  })
})
