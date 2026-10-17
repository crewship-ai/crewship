import { describe, it, expect, vi, beforeEach } from "vitest"
import { render, screen, fireEvent, cleanup, waitFor } from "@testing-library/react"

const menuAccess = vi.hoisted(() => ({ mode: "trusted" as "trusted" | "restricted" | "loading", api: vi.fn() }))
vi.mock("@/hooks/use-access-mode", () => ({
  useAccessMode: () => menuAccess.mode,
  useRestrictedSurfaces: () => menuAccess.mode === "trusted" ? null : menuAccess.mode === "restricted" ? ["account_security"] : [],
  useTrustedWorkspaceId: () => menuAccess.mode === "trusted" ? "ws-test" : null,
}))

// Radix DropdownMenu relies on pointer-capture + scrollIntoView, which
// happy-dom doesn't implement. Polyfill them so the menu can open in the
// test environment.
beforeEach(() => {
  if (!Element.prototype.hasPointerCapture) {
    Element.prototype.hasPointerCapture = () => false
  }
  if (!Element.prototype.releasePointerCapture) {
    Element.prototype.releasePointerCapture = () => {}
  }
  if (!Element.prototype.scrollIntoView) {
    Element.prototype.scrollIntoView = () => {}
  }
})

// usePathname is globally mocked to "/" in vitest.setup.ts.

vi.mock("@/hooks/use-auth", () => ({
  useIsInstanceAdmin: () => true,
  useAuth: () => ({
    session: { user: { name: "Demo User", email: "demo@crewship.ai" } },
    signOut: vi.fn().mockResolvedValue(undefined),
  }),
}))
// The menu reads the session list and the build when it opens; neither is
// what these tests are about.
vi.mock("@/lib/api-fetch", () => ({ apiFetch: menuAccess.api }))
vi.mock("@/hooks/use-realtime", () => ({ useRealtime: () => ({ status: "connected" }) }))
vi.mock("@/hooks/use-engine-status", () => ({ useEngineStatus: () => ({ status: "connected" }) }))
vi.mock("@/hooks/use-crews-status", () => ({ useCrewsStatus: () => null }))
vi.mock("@/hooks/use-provisioning-status", () => ({ useProvisioningStatus: () => null }))
vi.mock("@/hooks/use-workspace", () => ({ useWorkspace: () => ({ workspaceId: "ws-test", role: "VIEWER", workspace: { name: "Example workspace" } }) }))
vi.mock("@/hooks/use-mobile", () => ({ useIsMobile: () => false }))
vi.mock("@/hooks/use-abilities", () => ({ useAbilities: () => ({ role: "OWNER" }) }))
vi.mock("@/lib/store", () => ({
  useAppStore: (selector: (s: { settingsTab: string | null; breadcrumbs: unknown[] }) => unknown) =>
    selector({ settingsTab: null, breadcrumbs: [] }),
}))

// Stub child widgets that pull their own data hooks — irrelevant to the menu.
vi.mock("@/components/features/inbox/inbox-bell", () => ({ InboxBell: () => null }))
vi.mock("@/components/features/activity/activity-bell", () => ({ ActivityBell: () => null }))
vi.mock("@/components/command-palette", () => ({ CommandPalette: () => null }))
vi.mock("../app-toolbar-provisioning", () => ({ ProvisioningBadge: () => null }))
vi.mock("@/components/ui/wifi", () => ({ WifiIcon: () => null }))

import { TooltipProvider } from "@/components/ui/tooltip"
import { AppToolbar } from "../app-toolbar"

function openUserMenu() {
  render(
    <TooltipProvider>
      <AppToolbar />
    </TooltipProvider>,
  )
  // Radix DropdownMenu opens on pointerdown (primary button), not click.
  const trigger = screen.getByRole("button", { name: /user menu/i })
  fireEvent.pointerDown(trigger, { button: 0, ctrlKey: false })
}

describe("AppToolbar — user menu", () => {
  beforeEach(() => {
    cleanup()
    menuAccess.mode = "trusted"
    menuAccess.api.mockReset().mockImplementation((path: string) => Promise.resolve(path === "/api/v1/auth/pair/start"
      ? { ok: true, json: () => Promise.resolve({ code: "TEST-PAIR", expires_at: new Date(Date.now() + 600_000).toISOString() }) }
      : { ok: false, json: () => Promise.resolve(null) }))
  })

  it.each(["trusted", "restricted"] as const)("%s account can open ordinary CLI pairing", async mode => {
    menuAccess.mode = mode
    openUserMenu()
    fireEvent.click(screen.getByRole("menuitem", { name: /connect cli/i }))
    expect(await screen.findByRole("dialog", { name: /connect the cli/i })).toBeVisible()
    await waitFor(() => expect(menuAccess.api).toHaveBeenCalledWith("/api/v1/auth/pair/start", expect.objectContaining({ method: "POST" })))
    await waitFor(() => expect(screen.getByTestId("pair-command")).toHaveTextContent("TEST-PAIR"))
    if (mode === "restricted") expect(menuAccess.api.mock.calls.some(([path]) => path === "/api/v1/system/version")).toBe(false)
  })

  it("does not start pairing while account access is loading", () => {
    menuAccess.mode = "loading"
    openUserMenu()
    expect(screen.queryByRole("menuitem", { name: /connect cli/i })).not.toBeInTheDocument()
    expect(menuAccess.api.mock.calls.some(([path]) => path === "/api/v1/auth/pair/start")).toBe(false)
  })

  it("shows the actual membership and workspace in the profile header", () => {
    openUserMenu()
    expect(screen.getByText("Viewer")).toBeVisible()
    expect(screen.getByText("in Example workspace")).toBeVisible()
    expect(screen.queryByText("Owner")).not.toBeInTheDocument()
    expect(screen.queryByText("Unify Technology")).not.toBeInTheDocument()
  })

  it("states the workspace but does not switch it — the rail owns that", () => {
    openUserMenu()
    expect(screen.queryByRole("menuitem", { name: /create workspace/i })).not.toBeInTheDocument()
    expect(screen.queryByRole("menuitem", { name: /example workspace/i })).not.toBeInTheDocument()
  })

  it("keeps a compact theme switch and moves accent to Settings", () => {
    openUserMenu()
    expect(screen.getByRole("radiogroup", { name: "Theme" })).toBeInTheDocument()
    expect(screen.queryByRole("radiogroup", { name: /accent/i })).not.toBeInTheDocument()
  })

  it.each([
    [/^your profile: demo user$/i, "/settings?tab=profile"],
    [/^profile & account$/i, "/settings?tab=profile"],
    [/^sessions & cli tokens/i, "/settings?tab=sessions"],
  ])("%s routes to %s", async (name, href) => {
    openUserMenu()
    expect(await screen.findByRole("menuitem", { name })).toHaveAttribute("href", href)
  })

  it("collects the external links under one Help & resources submenu", async () => {
    openUserMenu()
    expect(screen.queryByRole("menuitem", { name: /documentation/i })).not.toBeInTheDocument()
    const sub = screen.getByRole("menuitem", { name: /help & resources/i })
    fireEvent.pointerMove(sub)
    fireEvent.keyDown(sub, { key: "ArrowRight" })
    for (const [label, href] of [
      ["Documentation", "https://docs.crewship.ai"],
      ["What's new", "https://github.com/crewship-ai/crewship/releases"],
      ["Report an issue", "https://github.com/crewship-ai/crewship/issues"],
      ["GitHub", "https://github.com/crewship-ai/crewship"],
    ]) {
      expect(await screen.findByRole("menuitem", { name: label })).toHaveAttribute("href", href)
    }
  })

  it("Log out stays actionable", async () => {
    openUserMenu()
    expect(await screen.findByRole("menuitem", { name: /log out/i })).toBeInTheDocument()
  })
})
