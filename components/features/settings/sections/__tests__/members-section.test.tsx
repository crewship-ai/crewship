import { describe, it, expect, vi, beforeEach } from "vitest"
import { render, screen, cleanup } from "@testing-library/react"

import { MembersSection } from "../members-section"

// The server gates these members-section mutations at two DIFFERENT tiers
// (internal/api/router_crews.go): invite (POST) and remove (DELETE) are
// `roleManage` (ADMIN+); role-change (PATCH) is `roleCreate` (MANAGER+).
// The UI has to draw the line in the same place or it hands out buttons
// that 403 — these tests pin that per-tier boundary, not just "admin sees
// everything, everyone else sees nothing".

const apiFetch = vi.fn()
vi.mock("@/lib/api-fetch", () => ({ apiFetch: (...args: unknown[]) => apiFetch(...args) }))
vi.mock("sonner", () => ({ toast: { success: vi.fn(), error: vi.fn(), info: vi.fn() } }))
vi.mock("@/components/admin/capability-grid", () => ({
  CapabilityGrid: () => <div data-testid="capability-grid" />,
}))
vi.mock("@/components/features/members/invite-member-dialog", () => ({
  InviteMemberDialog: () => <div data-testid="invite-dialog" />,
}))

function member(id: string, role: string, userId: string, name: string) {
  return {
    id,
    role,
    created_at: new Date().toISOString(),
    user: { id: userId, email: `${userId}@x.io`, full_name: name, avatar_url: null },
  }
}

// Caller is never one of the rows below — keeps assertions about "self"
// exclusion (already covered by member-role-control.test.tsx) out of the
// way of the tier assertions these tests exist for.
const members = [
  member("m-owner", "OWNER", "u-owner", "Olive Owner"),
  member("m-manager", "MANAGER", "u-manager", "Mo Manager"),
  member("m-member", "MEMBER", "u-member", "Mel Member"),
]

function renderSection(opts: { callerRole?: string; canInvite?: boolean }) {
  return render(
    <MembersSection
      members={members}
      workspaceId="ws1"
      currentUserId="u-caller"
      canInvite={opts.canInvite ?? false}
      callerRole={opts.callerRole}
      onRefresh={vi.fn()}
    />,
  )
}

describe("MembersSection — role-tiered controls", () => {
  beforeEach(() => {
    cleanup()
    apiFetch.mockReset()
  })

  it("gives an ADMIN invite, remove and role-change", () => {
    // canInvite deliberately false here: an ADMIN caller must still see
    // Invite because the gate is isAdminTier(callerRole), not the prop.
    renderSection({ callerRole: "ADMIN", canInvite: false })

    expect(screen.getByTestId("invite-dialog")).toBeInTheDocument()
    // ADMIN (rank 4) outranks MANAGER and MEMBER but not OWNER — two
    // editable rows, matching the no-modify-superior ladder.
    expect(screen.getAllByRole("combobox")).toHaveLength(2)
    expect(screen.getAllByRole("button", { name: /remove member/i })).toHaveLength(2)
  })

  it("gives a MANAGER role-change but NOT invite or remove", () => {
    // canInvite deliberately true here: a MANAGER caller must still NOT
    // see Invite — proves the render decision ignores the (CASL-derived,
    // here wrong-on-purpose) prop and uses isAdminTier(callerRole) instead.
    renderSection({ callerRole: "MANAGER", canInvite: true })

    expect(screen.queryByTestId("invite-dialog")).toBeNull()
    expect(screen.queryAllByRole("button", { name: /remove member/i })).toHaveLength(0)
    // MANAGER (rank 3) outranks only MEMBER — one editable row.
    expect(screen.getAllByRole("combobox")).toHaveLength(1)
  })

  it("shows a MEMBER the roster with no mutating control at all", () => {
    renderSection({ callerRole: "MEMBER" })

    // The roster itself — the read surface — stays visible.
    expect(screen.getByText("Olive Owner")).toBeInTheDocument()
    expect(screen.getByText("Mo Manager")).toBeInTheDocument()
    expect(screen.getByText("Mel Member")).toBeInTheDocument()

    expect(screen.queryByTestId("invite-dialog")).toBeNull()
    expect(screen.queryAllByRole("combobox")).toHaveLength(0)
    expect(screen.queryAllByRole("button", { name: /remove member/i })).toHaveLength(0)
    // Explains the gap, once, quietly — no alert styling.
    expect(screen.getByText(/only managers and admins can make changes here/i)).toBeInTheDocument()
  })

  it("never sends a mutating request for a MEMBER caller — there is no control that could", () => {
    renderSection({ callerRole: "MEMBER" })
    expect(apiFetch).not.toHaveBeenCalled()
  })
})
