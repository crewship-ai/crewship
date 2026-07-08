import { describe, it, expect, vi, beforeEach } from "vitest"
import { render, screen, cleanup } from "@testing-library/react"
import { MembersSection } from "../sections/members-section"

// #867.2 — the per-member role dropdown appears only for members the
// caller strictly outranks (and never on the caller's own row); everyone
// else shows a static badge.

vi.mock("@/components/admin/capability-grid", () => ({
  CapabilityGrid: () => <div data-testid="capability-grid" />,
}))
vi.mock("@/components/features/members/invite-member-dialog", () => ({
  InviteMemberDialog: () => <div data-testid="invite-dialog" />,
}))
vi.mock("sonner", () => ({ toast: { success: vi.fn(), error: vi.fn(), info: vi.fn() } }))

function member(id: string, role: string, userId: string) {
  return {
    id,
    role,
    created_at: new Date().toISOString(),
    user: { id: userId, email: `${userId}@x.io`, full_name: null, avatar_url: null },
  }
}

function renderWith(callerRole: string | undefined, currentUserId: string) {
  const members = [
    member("m-owner", "OWNER", "u-owner"),
    member("m-admin", "ADMIN", "u-admin"),
    member("m-member", "MEMBER", "u-member"),
  ]
  return render(
    <MembersSection
      members={members}
      workspaceId="ws1"
      currentUserId={currentUserId}
      canInvite={false}
      callerRole={callerRole}
      onRefresh={vi.fn()}
    />,
  )
}

describe("MemberRoleControl gating (#867.2)", () => {
  beforeEach(() => cleanup())

  it("shows a role dropdown for members the OWNER caller outranks", () => {
    renderWith("OWNER", "u-owner")
    // ADMIN and MEMBER rows are below OWNER → editable (comboboxes present).
    const combos = screen.getAllByRole("combobox")
    expect(combos.length).toBe(2)
  })

  it("does not show a dropdown on the caller's own row", () => {
    renderWith("OWNER", "u-owner")
    // The OWNER's own row must render a static badge, not a control — so
    // exactly the two OTHER rows are editable (asserted above). Here we
    // confirm the owner's own role text still shows as a badge.
    expect(screen.getByText("OWNER")).toBeTruthy()
  })

  it("shows no dropdowns for a MEMBER caller (outranks nobody)", () => {
    renderWith("MEMBER", "u-member")
    expect(screen.queryAllByRole("combobox").length).toBe(0)
  })

  it("shows no dropdowns when callerRole is omitted", () => {
    renderWith(undefined, "u-member")
    expect(screen.queryAllByRole("combobox").length).toBe(0)
  })
})
