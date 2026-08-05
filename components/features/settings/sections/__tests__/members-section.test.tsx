import { describe, it, expect, vi, beforeEach } from "vitest"
import { render, screen, cleanup, fireEvent } from "@testing-library/react"
import { QueryClient, QueryClientProvider } from "@tanstack/react-query"

import { MembersSection } from "../members-section"

// The server gates these members-section mutations at two DIFFERENT tiers
// (internal/api/router_crews.go): invite (POST) and remove (DELETE) are
// `roleManage` (ADMIN+); role-change (PATCH) is `roleCreate` (MANAGER+).
// The UI has to draw the line in the same place or it hands out buttons
// that 403 — these tests pin that per-tier boundary, not just "admin sees
// everything, everyone else sees nothing".

const apiFetch = vi.fn()
vi.mock("@/lib/api-fetch", () => ({ apiFetch: (...args: unknown[]) => apiFetch(...args) }))
vi.mock("sonner", () => ({
  toast: { success: vi.fn(), error: vi.fn(), info: vi.fn(), warning: vi.fn() },
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

// The section now owns the bulk-capabilities query itself (the pips in the
// collapsed row need it whether or not any row is expanded), so every render
// needs a query client. Retries off so a stubbed failure surfaces at once.
function renderSection(opts: { callerRole?: string }) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(
    <QueryClientProvider client={qc}>
      <MembersSection
        members={members}
        workspaceId="ws1"
        currentUserId="u-caller"
        callerRole={opts.callerRole}
        onRefresh={vi.fn()}
      />
    </QueryClientProvider>,
  )
}

describe("MembersSection — role-tiered controls", () => {
  beforeEach(() => {
    cleanup()
    apiFetch.mockReset()
    apiFetch.mockResolvedValue({ ok: true, status: 200, json: async () => ({ members: [] }) })
  })

  it("gives an ADMIN invite, remove and role-change", () => {
    // The invite control follows callerRole alone now — the old canInvite
    // prop is gone, so there is nothing left to contradict it with.
    // Invite because the gate is isAdminTier(callerRole), not the prop.
    renderSection({ callerRole: "ADMIN" })

    expect(screen.getByTestId("invite-dialog")).toBeInTheDocument()
    // ADMIN (rank 4) outranks MANAGER and MEMBER but not OWNER — two
    // editable rows, matching the no-modify-superior ladder.
    expect(screen.getAllByRole("combobox")).toHaveLength(2)
    expect(screen.getAllByRole("button", { name: /remove member/i })).toHaveLength(2)
  })

  it("gives a MANAGER role-change but NOT invite or remove", () => {
    // A MANAGER must still NOT
    // see Invite — proves the render decision ignores the (CASL-derived,
    // here wrong-on-purpose) prop and uses isAdminTier(callerRole) instead.
    renderSection({ callerRole: "MANAGER" })

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
    // The capability read is admin-gated too, so a MEMBER caller issues no
    // request of any kind from this section.
    expect(apiFetch).not.toHaveBeenCalled()
  })
})

// Progressive disclosure is right for reference material and wrong for live
// state. #1517: the roster and the capability grid were two separate lists of
// the same people, so "what can this person do?" meant reading both. There is
// now one row per person, and expanding it is what reveals their grants.
describe("MembersSection — disclosure", () => {
  beforeEach(() => {
    cleanup()
    apiFetch.mockReset()
    apiFetch.mockResolvedValue({ ok: true, status: 200, json: async () => ({ members: [] }) })
  })

  it("gives every person exactly one row — never a second list of the same people", () => {
    renderSection({ callerRole: "OWNER" })
    // The defect was the same name appearing twice: once in the roster, once
    // as a row of the capability table.
    for (const name of ["Olive Owner", "Mo Manager", "Mel Member"]) {
      expect(screen.getAllByText(name)).toHaveLength(1)
    }
  })

  it("keeps the role ladder out of the way until asked for", () => {
    renderSection({ callerRole: "OWNER" })
    // The whole five-role ladder is reference you consult when *choosing* a
    // role: identical in every workspace, forever. It earns a help
    // affordance, not permanent screen space.
    expect(screen.queryByText(/All permissions except billing transfer/i)).toBeNull()
    expect(screen.getByRole("button", { name: /what do the roles mean/i })).toBeTruthy()
  })

  it("reveals the ladder on request", async () => {
    renderSection({ callerRole: "OWNER" })
    fireEvent.click(screen.getByRole("button", { name: /what do the roles mean/i }))
    expect(await screen.findByText(/All permissions except billing transfer/i)).toBeTruthy()
  })

  it("states what a member's own role grants inside their row, not only in the popover", () => {
    renderSection({ callerRole: "OWNER" })
    fireEvent.click(screen.getByRole("button", { name: /expand permissions for Mel Member/i }))
    // The half of "rules and permissions" that is about THIS person is no
    // longer reachable only through a popover.
    expect(screen.getByText(/Own resource access only/i)).toBeTruthy()
  })
})

// Provisioned accounts arrive with no name. The roster rendered
// `full_name ?? email`, and ?? does not fall back on the empty string the
// endpoint used to store — so those rows showed neither a name nor an email,
// just an anonymous circle and a role.
describe("MembersSection — members without a name", () => {
  beforeEach(() => {
    cleanup()
    apiFetch.mockReset()
    apiFetch.mockResolvedValue({ ok: true, status: 200, json: async () => ({ members: [] }) })
  })

  const noName = [
    { id: "m1", role: "MEMBER", created_at: new Date().toISOString(),
      user: { id: "u1", email: "kolega@example.com", full_name: "", avatar_url: null } },
    { id: "m2", role: "MANAGER", created_at: new Date().toISOString(),
      user: { id: "u2", email: "pablo@example.com", full_name: null, avatar_url: null } },
  ]

  function renderNoName() {
    const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
    return render(
      <QueryClientProvider client={qc}>
        <MembersSection
          members={noName}
          workspaceId="ws1"
          currentUserId="u-caller"
          callerRole="OWNER"
          onRefresh={vi.fn()}
        />
      </QueryClientProvider>,
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

// ── Deep link ──────────────────────────────────────────────────────────────
//
// The ⌘K palette can find a person by name or email, and used to drop the
// caller on the roster with no indication of which row they had picked — on a
// workspace of any size that is a second search by eye. `focusUserId` opens
// that person's row on arrival.

function renderFocused(focusUserId?: string) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(
    <QueryClientProvider client={qc}>
      <MembersSection
        members={members}
        workspaceId="ws1"
        currentUserId="u-caller"
        callerRole="ADMIN"
        focusUserId={focusUserId}
        onRefresh={vi.fn()}
      />
    </QueryClientProvider>,
  )
}

describe("MembersSection — arriving at one person", () => {
  beforeEach(() => {
    cleanup()
    apiFetch.mockReset()
    apiFetch.mockResolvedValue({ ok: true, status: 200, json: async () => ({ members: [] }) })
  })

  it("opens the named person's row and leaves the others shut", () => {
    renderFocused("u-manager")

    const rows = screen.getAllByRole("button", { name: /permissions for/i })
    const byName = (n: string) => rows.find((r) => r.textContent?.includes(n))!
    expect(byName("Mo Manager")).toHaveAttribute("aria-expanded", "true")
    expect(byName("Olive Owner")).toHaveAttribute("aria-expanded", "false")
    expect(byName("Mel Member")).toHaveAttribute("aria-expanded", "false")
  })

  it("leaves every row shut when the link names nobody", () => {
    renderFocused(undefined)
    for (const row of screen.getAllByRole("button", { name: /permissions for/i })) {
      expect(row).toHaveAttribute("aria-expanded", "false")
    }
  })

  it("leaves every row shut when the link names someone who is not a member", () => {
    // A stale link, or a person removed since — the roster still has to render.
    renderFocused("u-ghost")
    for (const row of screen.getAllByRole("button", { name: /permissions for/i })) {
      expect(row).toHaveAttribute("aria-expanded", "false")
    }
  })
})
