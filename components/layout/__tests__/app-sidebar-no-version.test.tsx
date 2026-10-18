// Harbor 2 (v67): the sidebar carries navigation only. "Community Edition /
// dev · <sha>" is not part of it; the build lives in the user menu's foot
// (`pu-ver`). Nothing here mocks the old SidebarVersion on purpose: the rail
// must not render a version row at any width.
import { describe, it, expect, vi, beforeEach } from "vitest"
import { render, screen, cleanup } from "@testing-library/react"

const mobile = vi.hoisted(() => ({ value: false }))
const ok = (b: unknown) => ({ ok: true, json: async () => b })
const api = vi.hoisted(() => vi.fn())

vi.mock("next/navigation", () => ({
  useRouter: () => ({ push: vi.fn(), replace: vi.fn(), prefetch: vi.fn() }),
  usePathname: () => "/chat",
  useSearchParams: () => new URLSearchParams(),
}))
vi.mock("@/lib/api-fetch", () => ({ apiFetch: (...a: unknown[]) => api(...a) }))
vi.mock("@/hooks/use-workspace", () => ({ useWorkspace: () => ({ workspaceId: "ws-test" }) }))
vi.mock("@/hooks/use-abilities", () => ({ useAbilities: () => ({ role: "OWNER" }) }))
vi.mock("@/hooks/use-inbox", () => ({ useInboxUnreadCount: () => 0 }))
vi.mock("@/hooks/use-access-mode", () => ({
  useAccessMode: () => "trusted",
  useRestrictedSurfaces: () => null,
  useTrustedWorkspaceId: () => "ws-test",
}))
vi.mock("@/hooks/use-auth", () => ({ useIsInstanceAdmin: () => true, useAuthAvailable: () => true }))
vi.mock("@/components/layout/workspace-switcher", () => ({ WorkspaceSwitcher: () => null }))
vi.mock("@/hooks/use-mobile", () => ({ useIsMobile: () => mobile.value }))
vi.mock("@/components/layout/user-menu", () => ({ UserMenu: () => <div data-testid="user-menu" /> }))

import { AppSidebar } from "@/components/layout/app-sidebar"
import { SidebarProvider } from "@/components/ui/sidebar"

beforeEach(() => {
  cleanup()
  mobile.value = false
  api.mockReset()
  api.mockImplementation(async (u: string) =>
    u.includes("version") ? ok({ current: "dev", commit: "d43bfd1f95636cbc" }) : ok({ edition: "community" }))
})

describe.each([["1440 and 1024 (open panel and collapsed rail share one DOM)", false], ["390 (the sidebar is a closed sheet; the More sheet has its own test)", true]] as const)("the sidebar at %s", (_name, isMobile) => {
  it("shows no edition or build row and no instance-overview link", async () => {
    mobile.value = isMobile
    render(<SidebarProvider><AppSidebar /></SidebarProvider>)
    await new Promise((r) => setTimeout(r, 0))
    expect(screen.queryByText(/Community Edition/)).toBeNull()
    expect(document.querySelector("[data-slot=sidebar-build]")).toBeNull()
    expect(document.querySelector('a[href="/admin?tab=overview"]')).toBeNull()
    expect(api).not.toHaveBeenCalledWith("/api/v1/system/version")
  })
})
