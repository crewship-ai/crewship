import { describe, it, expect, vi, beforeAll, beforeEach } from "vitest"
import { render, screen, cleanup, fireEvent, within } from "@testing-library/react"
import { QueryClient, QueryClientProvider } from "@tanstack/react-query"

import { MembersSection } from "../members-section"

/**
 * #2878 — Settings › Members showed a restricted member exactly like a trusted
 * one. The roster now carries `access_mode` (sent only to a trusted
 * OWNER/ADMIN) and the row shows it as its own fact next to the role, in the
 * collapsed row and in the expanded panel.
 */

const apiFetch = vi.fn()
vi.mock("@/lib/api-fetch", () => ({ apiFetch: (...a: unknown[]) => apiFetch(...a) }))
vi.mock("sonner", () => ({ toast: { success: vi.fn(), error: vi.fn(), info: vi.fn(), warning: vi.fn() } }))
vi.mock("@/components/features/members/invite-member-dialog", () => ({
  InviteMemberDialog: () => <div data-testid="invite-dialog" />,
}))

beforeAll(() => {
  Element.prototype.scrollIntoView = vi.fn()
})

type Mode = "trusted" | "restricted" | undefined

function member(id: string, name: string, role: string, access_mode: Mode) {
  return {
    id: `m-${id}`,
    role,
    created_at: new Date().toISOString(),
    ...(access_mode ? { access_mode } : {}),
    user: { id: `u-${id}`, email: `${id}@x.io`, full_name: name, avatar_url: null },
  }
}

function renderSection(members: ReturnType<typeof member>[], callerRole = "ADMIN") {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(
    <QueryClientProvider client={qc}>
      <MembersSection
        members={members}
        workspaceId="ws1"
        currentUserId="u-caller"
        callerRole={callerRole}
        onRefresh={vi.fn()}
      />
    </QueryClientProvider>,
  )
}

/** The collapsed row: the flex line holding the disclosure trigger. */
function rowOf(name: string): HTMLElement {
  const trigger = screen.getByRole("button", { name: new RegExp(`expand permissions for ${name}`, "i") })
  return trigger.parentElement as HTMLElement
}

beforeEach(() => {
  cleanup()
  apiFetch.mockReset()
  apiFetch.mockResolvedValue({ ok: true, status: 200, json: async () => ({ members: [] }) })
})

describe("MembersSection — access mode", () => {
  it("marks a restricted member's row and leaves a trusted member's row unmarked", () => {
    renderSection([
      member("rita", "Rita Restricted", "MEMBER", "restricted"),
      member("tom", "Tom Trusted", "MEMBER", "trusted"),
    ])
    const rita = rowOf("Rita Restricted")
    expect(within(rita).getByText("Restricted")).toBeTruthy()
    // Role and access are two facts: the role chip still reads MEMBER.
    expect(within(rita).getByText("MEMBER")).toBeTruthy()

    const tom = rowOf("Tom Trusted")
    expect(within(tom).queryByText("Restricted")).toBeNull()
    expect(within(tom).getByText("MEMBER")).toBeTruthy()
  })

  it("names the access mode in the expanded panel", () => {
    renderSection([
      member("rita", "Rita Restricted", "MEMBER", "restricted"),
      member("tom", "Tom Trusted", "MEMBER", "trusted"),
    ])
    fireEvent.click(screen.getByRole("button", { name: /expand permissions for Rita Restricted/i }))
    expect(screen.getByTestId("member-access-mode").textContent).toMatch(/^Restricted/)

    fireEvent.click(screen.getByRole("button", { name: /collapse permissions for Rita Restricted/i }))
    fireEvent.click(screen.getByRole("button", { name: /expand permissions for Tom Trusted/i }))
    expect(screen.getByTestId("member-access-mode").textContent).toMatch(/^Trusted/)
  })

  it("does not guess an access mode the server withheld", () => {
    // A MEMBER caller gets no access_mode; absence is not "trusted".
    renderSection([member("rita", "Rita Restricted", "MEMBER", undefined)], "MEMBER")
    expect(within(rowOf("Rita Restricted")).queryByText("Restricted")).toBeNull()
    fireEvent.click(screen.getByRole("button", { name: /expand permissions for Rita Restricted/i }))
    expect(screen.queryByTestId("member-access-mode")).toBeNull()
  })
})
