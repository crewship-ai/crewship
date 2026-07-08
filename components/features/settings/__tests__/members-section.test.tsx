import { describe, it, expect, vi, beforeEach } from "vitest"
import { render, screen, cleanup } from "@testing-library/react"
import { MembersSection } from "../sections/members-section"

// The capability grid pulls in react-query + fetches on mount. We only
// care *whether* it renders here (the #866.3 wiring bug), so stub it with
// a probe element and assert on that.
vi.mock("@/components/admin/capability-grid", () => ({
  CapabilityGrid: () => <div data-testid="capability-grid" />,
}))

vi.mock("@/components/features/members/invite-member-dialog", () => ({
  InviteMemberDialog: () => <div data-testid="invite-dialog" />,
}))

vi.mock("sonner", () => ({
  toast: { success: vi.fn(), error: vi.fn(), info: vi.fn() },
}))

const members = [
  {
    id: "m1",
    role: "OWNER",
    created_at: new Date().toISOString(),
    user: { id: "u1", email: "owner@example.com", full_name: "Ada Owner", avatar_url: null },
  },
  {
    id: "m2",
    role: "MEMBER",
    created_at: new Date().toISOString(),
    user: { id: "u2", email: "member@example.com", full_name: null, avatar_url: null },
  },
]

function renderSection(callerRole: string | undefined) {
  return render(
    <MembersSection
      members={members}
      workspaceId="ws1"
      currentUserId="u1"
      canInvite={false}
      callerRole={callerRole}
      onRefresh={vi.fn()}
    />,
  )
}

describe("MembersSection capability grid gating (#866.3)", () => {
  beforeEach(() => cleanup())

  it("surfaces the per-member capability section for OWNER/ADMIN callers", () => {
    // The collapsible trigger renders only when isAdmin — its content
    // (CapabilityGrid) mounts lazily on expand, so the trigger label is
    // the reliable admin-gate signal. Both OWNER and ADMIN must pass.
    renderSection("OWNER")
    expect(screen.getByText(/Per-member capabilities/i)).toBeTruthy()

    cleanup()
    renderSection("ADMIN")
    expect(screen.getByText(/Per-member capabilities/i)).toBeTruthy()
  })

  it("hides the capability section for non-admin callers", () => {
    renderSection("MEMBER")
    expect(screen.queryByText(/Per-member capabilities/i)).toBeNull()
  })

  it("hides the capability section when callerRole is omitted (the pre-fix regression)", () => {
    renderSection(undefined)
    expect(screen.queryByText(/Per-member capabilities/i)).toBeNull()
  })
})
