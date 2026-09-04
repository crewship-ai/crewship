import { render, screen } from "@testing-library/react"
import { describe, expect, it, vi } from "vitest"

import type { BreadcrumbItem } from "@/lib/store"

// The chat route's breadcrumb: names from the store when the chat page has
// published them, the URL slug only while the roster loads, and never a slug
// that starts with an underscore.
let pathname = "/chat/riley"
let breadcrumbs: BreadcrumbItem[] = []

vi.mock("next/navigation", () => ({
  useRouter: () => ({ push: vi.fn(), replace: vi.fn(), back: vi.fn(), prefetch: vi.fn(), refresh: vi.fn() }),
  useSearchParams: () => new URLSearchParams(),
  usePathname: () => pathname,
}))
vi.mock("@/hooks/use-auth", () => ({
  useAuth: () => ({ session: { user: { name: "Demo User", email: "demo@crewship.ai" } }, signOut: vi.fn().mockResolvedValue(undefined) }),
}))
vi.mock("@/hooks/use-realtime", () => ({ useRealtime: () => ({ status: "connected" }) }))
vi.mock("@/hooks/use-engine-status", () => ({ useEngineStatus: () => ({ status: "connected" }) }))
vi.mock("@/hooks/use-crews-status", () => ({ useCrewsStatus: () => null }))
vi.mock("@/hooks/use-provisioning-status", () => ({ useProvisioningStatus: () => null }))
vi.mock("@/hooks/use-workspace", () => ({ useWorkspace: () => ({ workspaceId: "ws-test" }) }))
vi.mock("@/hooks/use-mobile", () => ({ useIsMobile: () => false }))
vi.mock("@/hooks/use-abilities", () => ({ useAbilities: () => ({ role: "OWNER" }) }))
vi.mock("@/lib/store", () => ({
  useAppStore: (selector: (s: { settingsTab: string | null; breadcrumbs: BreadcrumbItem[] }) => unknown) =>
    selector({ settingsTab: null, breadcrumbs }),
}))
vi.mock("@/components/features/inbox/inbox-bell", () => ({ InboxBell: () => null }))
vi.mock("@/components/features/activity/activity-bell", () => ({ ActivityBell: () => null }))
vi.mock("@/components/command-palette", () => ({ CommandPalette: () => null }))
vi.mock("../app-toolbar-provisioning", () => ({ ProvisioningBadge: () => null }))
vi.mock("@/components/ui/wifi", () => ({ WifiIcon: () => null }))

import { TooltipProvider } from "@/components/ui/tooltip"
import { AppToolbar } from "../app-toolbar"

describe("the chat breadcrumb", () => {
  it("renders the names the chat page published, each a link", () => {
    breadcrumbs = [{ label: "Crews", href: "/crews" }, { label: "Ops", href: "/crews?crew=ops" }, { label: "Riley", href: "/crews?agent=riley" }]
    render(<TooltipProvider><AppToolbar /></TooltipProvider>)
    expect(screen.getByRole("link", { name: "Ops" }).getAttribute("href")).toBe("/crews?crew=ops")
    expect(screen.getByRole("link", { name: "Riley" }).getAttribute("href")).toBe("/crews?agent=riley")
    expect(screen.getByText("Chat")).toBeInTheDocument()
    expect(screen.queryByText("riley")).not.toBeInTheDocument()
  })

  it("falls back to the slug only while nothing is published, and never to an internal one", () => {
    breadcrumbs = []
    pathname = "/chat/_crewship-setup-guide"
    render(<TooltipProvider><AppToolbar /></TooltipProvider>)
    expect(screen.queryByText("_crewship-setup-guide")).not.toBeInTheDocument()
    expect(screen.getByRole("link", { name: "Crews" })).toBeInTheDocument()
  })
})
