import { describe, it, expect, vi, beforeEach } from "vitest"
import { render, screen, cleanup, waitFor } from "@testing-library/react"

// ODCH-SET-10: /settings?tab=audit for a role below Admin used to fall back to
// Profile without a word, while /settings/audit says "You can't open Audit
// log". A moved section forwards to its own page whatever the role, so the
// deep link shows the same no-access state as the page itself.

const h = vi.hoisted(() => ({ role: "MANAGER" as string, replace: vi.fn() }))
vi.mock("@/hooks/use-mobile", () => ({ useIsMobile: () => false }))
vi.mock("next/navigation", () => ({
  useRouter: () => ({ replace: h.replace, push: vi.fn(), prefetch: vi.fn(), back: vi.fn(), forward: vi.fn(), refresh: vi.fn() }),
  useSearchParams: () => new URLSearchParams(window.location.search),
  usePathname: () => "/settings",
}))
vi.mock("@/hooks/use-workspace", () => ({
  useWorkspace: () => ({ workspaceId: "w1", role: h.role, loading: false }),
}))
vi.mock("@/hooks/use-auth", () => ({
  useAuth: () => ({
    session: { user: { id: "u1", name: "Ada", email: "ada@example.com" } },
    signOut: vi.fn().mockResolvedValue(undefined),
    refresh: vi.fn(),
  }),
}))
vi.mock("@/hooks/use-abilities", () => ({
  useAbilities: () => ({ abilities: { can: () => false }, hasCapability: () => false }),
}))
vi.mock("@/lib/api-fetch", () => ({
  apiFetch: vi.fn(async (url: string) => ({
    ok: true,
    status: 200,
    json: async () => url.includes("/members") ? [] : url.includes("/preferences") ? {} : ({ id: "w1", name: "Acme", slug: "acme", preferred_language: null, _count: { crews: 1, agents: 2, members: 3 } }),
  })),
}))
vi.mock("../sections/profile-section", () => ({ ProfileSection: () => <div data-testid="profile-section" /> }))

import { SettingsLayout } from "../settings-layout"

beforeEach(() => {
  cleanup()
  h.replace.mockReset()
  h.role = "MANAGER"
  window.history.replaceState(null, "", "/settings?tab=audit")
})

describe("/settings?tab=audit below Admin", () => {
  it("forwards to /settings/audit (which shows no access) instead of opening Profile", async () => {
    render(<SettingsLayout />)
    await waitFor(() => expect(h.replace).toHaveBeenCalledWith("/settings/audit"))
    expect(screen.queryByTestId("profile-section")).toBeNull()
  })

  it("still falls back to Profile for a hidden section that has no page of its own", async () => {
    h.role = "MEMBER"
    window.history.replaceState(null, "", "/settings?tab=access-secrets")
    render(<SettingsLayout />)
    expect(await screen.findByTestId("profile-section")).toBeInTheDocument()
    expect(h.replace).not.toHaveBeenCalled()
  })
})
