import { afterEach, expect, it, vi } from "vitest"
import { cleanup, render, screen } from "@testing-library/react"
import { AppSidebar } from "../app-sidebar"
import { MobileTabBar } from "../mobile-tab-bar"
import { SidebarProvider } from "@/components/ui/sidebar"

const state = vi.hoisted(() => ({ workspaceId: "alex", workspaces: [
  { id: "alex", currentUserAccessMode: "restricted", restricted_session: true, restricted_surfaces: ["chat", "account_security"] },
  { id: "none", currentUserAccessMode: "trusted", restricted_session: true, restricted_surfaces: ["account_security"] },
] }))
vi.mock("next/navigation", () => ({ useRouter: () => ({ push: vi.fn(), replace: vi.fn(), prefetch: vi.fn() }), usePathname: () => "/chat", useSearchParams: () => new URLSearchParams() }))
vi.mock("@/hooks/use-workspace", () => ({ useWorkspace: () => ({ ...state, loading: false, error: false }) }))
vi.mock("@/hooks/use-abilities", () => ({ useAbilities: () => ({ role: "VIEWER" }) }))
vi.mock("@/hooks/use-inbox", () => ({ useInboxUnreadCount: () => 0 }))
vi.mock("@/hooks/use-auth", () => ({ useIsInstanceAdmin: () => false }))
vi.mock("@/components/layout/workspace-switcher", () => ({ WorkspaceSwitcher: () => null }))
vi.mock("@/components/layout/sidebar-version", () => ({ SidebarVersion: () => null }))
vi.mock("@/hooks/use-mobile", () => ({ useIsMobile: () => false }))
afterEach(() => { cleanup(); state.workspaceId="alex"; state.workspaces[0].restricted_surfaces=["chat","account_security"] })

function shell() { return <SidebarProvider><AppSidebar /><MobileTabBar /></SidebarProvider> }
it("desktop and phone tabs keep granted Chat and remove it on workspace switch", () => {
  const view=render(shell())
  expect(screen.getAllByRole("link",{name:/^Chat$/})).toHaveLength(2)
  expect(screen.queryByRole("link",{name:/^Routines$/})).toBeNull()
  state.workspaceId="none"; view.rerender(shell())
  expect(screen.queryByRole("link",{name:/^Chat$/})).toBeNull()
  expect(screen.getByRole("link",{name:/^Settings$/})).toBeVisible()
  state.workspaceId="alex"; view.rerender(shell())
  expect(screen.getAllByRole("link",{name:/^Chat$/})).toHaveLength(2)
})
it("removes stale Chat navigation after same-session grant revocation without changing role", () => {
  const view=render(shell())
  expect(screen.getAllByRole("link",{name:/^Chat$/})).toHaveLength(2)
  state.workspaces[0].restricted_surfaces=["account_security"]; view.rerender(shell())
  expect(screen.queryByRole("link",{name:/^Chat$/})).toBeNull()
  expect(screen.queryByRole("link",{name:/^Inbox$/})).toBeNull()
})
