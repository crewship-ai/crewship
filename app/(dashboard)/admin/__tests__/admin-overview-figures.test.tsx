// PAR-093: the Overview's instance figures must not fail because the
// workspace directory is still loading or the remembered workspace is no
// longer one of the person's own. The figures are instance-wide.
import { describe, it, expect, vi, beforeEach } from "vitest"
import { render, cleanup, waitFor, screen } from "@testing-library/react"

const h = vi.hoisted(() => ({
  ws: { workspaceId: "ws-gone" as string | null, loading: false, workspaceUnavailable: false, role: null as string | null },
  apiFetch: vi.fn(),
}))
vi.mock("@/hooks/use-mobile", () => ({ useIsMobile: () => false }))
vi.mock("next/navigation", () => ({
  useRouter: () => ({ push: vi.fn(), replace: vi.fn(), refresh: vi.fn() }),
  usePathname: () => "/admin",
  useSearchParams: () => new URLSearchParams(),
}))
vi.mock("@/hooks/use-auth", () => ({ useIsInstanceAdmin: () => true }))
vi.mock("@/hooks/use-workspace", () => ({ useWorkspace: () => h.ws }))
vi.mock("@/lib/api-fetch", () => ({ apiFetch: (...a: unknown[]) => h.apiFetch(...a) }))
vi.mock("../hooks/use-admin-websocket", () => ({
  useAdminWebSocket: () => ({ keeperLiveEvents: [], keeperWsStatus: "idle" }),
}))
vi.mock("../tabs/overview-tab", async (importOriginal) => ({
  ...(await importOriginal<typeof import("../tabs/overview-tab")>()),
  OverviewTab: () => <p>overview body</p>,
}))

import AdminPage from "../page"

const json = (b: unknown) => new Response(JSON.stringify(b), { status: 200, headers: { "content-type": "application/json" } })
const urls = () => h.apiFetch.mock.calls.map(([u]) => String(u))

beforeEach(() => {
  cleanup()
  h.apiFetch.mockReset()
  h.apiFetch.mockImplementation(async () => json({}))
  window.history.replaceState(null, "", "/admin")
})

describe("Admin Overview figures", () => {
  it("asks for nothing while the workspace directory is still loading", async () => {
    h.ws = { workspaceId: "ws-1", loading: true, workspaceUnavailable: false, role: null }
    render(<AdminPage />)
    await new Promise((r) => setTimeout(r, 20))
    expect(urls().some((u) => u.startsWith("/api/v1/admin/stats"))).toBe(false)
  })

  it("reads the instance figures without a workspace when the remembered one is unavailable", async () => {
    h.ws = { workspaceId: "ws-gone", loading: false, workspaceUnavailable: true, role: null }
    render(<AdminPage />)
    await screen.findByText("overview body")
    await waitFor(() => expect(urls()).toContain("/api/v1/admin/stats"))
    expect(urls().some((u) => u.includes("workspace_id=ws-gone"))).toBe(false)
  })
})
