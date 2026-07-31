import { describe, it, expect, vi, beforeEach } from "vitest"
import { render, screen, cleanup, fireEvent } from "@testing-library/react"
import { QueryClient, QueryClientProvider } from "@tanstack/react-query"

import { MembersSection } from "../sections/members-section"

// #866.3 was a wiring bug: the per-member capability surface rendered for
// nobody because `callerRole` never reached the gate. #1517 folded that
// surface into the roster row, so the gate now decides whether a row carries
// capabilities at all — same regression, new shape. These tests follow it.

const apiFetch = vi.fn()
vi.mock("@/lib/api-fetch", () => ({ apiFetch: (...a: unknown[]) => apiFetch(...a) }))

vi.mock("@/components/features/members/invite-member-dialog", () => ({
  InviteMemberDialog: () => <div data-testid="invite-dialog" />,
}))

vi.mock("sonner", () => ({
  toast: { success: vi.fn(), error: vi.fn(), info: vi.fn(), warning: vi.fn() },
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
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(
    <QueryClientProvider client={qc}>
      <MembersSection
        members={members}
        workspaceId="ws1"
        currentUserId="u1"
        callerRole={callerRole}
        onRefresh={vi.fn()}
      />
    </QueryClientProvider>,
  )
}

describe("MembersSection capability gating (#866.3, #1517)", () => {
  beforeEach(() => {
    cleanup()
    apiFetch.mockReset()
    apiFetch.mockResolvedValue({
      ok: true,
      status: 200,
      json: async () => ({
        members: [{ user_id: "u2", role: "MEMBER", capabilities: ["chat", "issue.create"] }],
      }),
    })
  })

  it("surfaces per-member capabilities for OWNER/ADMIN callers", async () => {
    // Collapsed, the row carries the pip summary; that is the reliable
    // admin-gate signal because it renders without expanding anything.
    // Both OWNER and ADMIN must pass.
    renderSection("OWNER")
    expect(await screen.findByLabelText(/member@example.com: chat, issue.create/i)).toBeTruthy()

    cleanup()
    renderSection("ADMIN")
    expect(await screen.findByLabelText(/member@example.com: chat, issue.create/i)).toBeTruthy()
  })

  it("hides capabilities from non-admin callers, collapsed and expanded", () => {
    renderSection("MEMBER")
    expect(screen.queryByLabelText(/member@example.com: chat/i)).toBeNull()
    // Expanding a row must not become a back door into an admin-only read.
    fireEvent.click(screen.getByRole("button", { name: /expand permissions for member@example.com/i }))
    expect(screen.queryAllByRole("switch")).toHaveLength(0)
    // The role explanation is not admin-only, so the row still says something.
    expect(screen.getByText(/Own resource access only/i)).toBeTruthy()
  })

  it("hides capabilities when callerRole is omitted (the pre-fix regression)", () => {
    renderSection(undefined)
    expect(screen.queryByLabelText(/member@example.com: chat/i)).toBeNull()
    expect(apiFetch).not.toHaveBeenCalled()
  })
})
