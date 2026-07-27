import { describe, it, expect, vi, beforeEach } from "vitest"
import { render, screen, cleanup, fireEvent } from "@testing-library/react"

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

// Progressive disclosure is right for reference material and wrong for live
// state. The section had both behind identical accordions: a role legend
// that never changes, and a capability grid whose checkboxes mutate
// permissions immediately. Collapsing the second meant "who can do what
// here?" — the question the screen exists to answer — needed extra clicks.
describe("MembersSection — disclosure", () => {
  beforeEach(() => cleanup())

  it("shows the capability grid without making an admin open it", async () => {
    renderSection({ callerRole: "OWNER" })
    // Live, immediately-applied permissions are the point of the screen.
    expect(await screen.findByTestId("capability-grid")).toBeTruthy()
  })

  it("keeps the role legend out of the way until asked for", () => {
    renderSection({ callerRole: "OWNER" })
    // Static reference: identical in every workspace, forever. It earns a
    // help affordance, not permanent screen space.
    expect(screen.queryByText(/All permissions except billing transfer/i)).toBeNull()
    expect(screen.getByRole("button", { name: /what do the roles mean/i })).toBeTruthy()
  })

  it("reveals the legend on request", async () => {
    renderSection({ callerRole: "OWNER" })
    fireEvent.click(screen.getByRole("button", { name: /what do the roles mean/i }))
    expect(await screen.findByText(/All permissions except billing transfer/i)).toBeTruthy()
  })

  it("does not show the capability grid to a non-admin", () => {
    // Unchanged gating — the grid writes to a roleManage route.
    renderSection({ callerRole: "MEMBER" })
    expect(screen.queryByTestId("capability-grid")).toBeNull()
  })
})

// Provisioned accounts arrive with no name. The roster rendered
// `full_name ?? email`, and ?? does not fall back on the empty string the
// endpoint used to store — so those rows showed neither a name nor an email,
// just an anonymous circle and a role.
describe("MembersSection — members without a name", () => {
  beforeEach(() => cleanup())

  const noName = [
    { id: "m1", role: "MEMBER", created_at: new Date().toISOString(),
      user: { id: "u1", email: "kolega@example.com", full_name: "", avatar_url: null } },
    { id: "m2", role: "MANAGER", created_at: new Date().toISOString(),
      user: { id: "u2", email: "pablo@example.com", full_name: null, avatar_url: null } },
  ]

  function renderNoName() {
    return render(
      <MembersSection
        members={noName}
        workspaceId="ws1"
        currentUserId="u-caller"
        callerRole="OWNER"
        onRefresh={vi.fn()}
      />,
    )
  }

  it("identifies a member by email when the name is an empty string", () => {
    renderNoName()
    expect(screen.getAllByText("kolega@example.com").length).toBeGreaterThan(0)
  })

  it("identifies a member by email when the name is null", () => {
    renderNoName()
    expect(screen.getAllByText("pablo@example.com").length).toBeGreaterThan(0)
  })

  it("never renders a row with no identifying text at all", () => {
    const { container } = renderNoName()
    // The failure mode was a row carrying only a coloured circle and a role
    // dropdown — unusable for deciding who to remove.
    for (const email of ["kolega@example.com", "pablo@example.com"]) {
      expect(container.textContent).toContain(email)
    }
  })
})
