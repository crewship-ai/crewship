// Part C: Admin › Security for an instance admin who belongs to no workspace.
// The instance-wide cards get INSTANCE_SCOPE (not null, which means "not known
// yet"), and the Keeper's state and the server's posture load without one.
import { describe, it, expect, vi, beforeEach } from "vitest"
import { render, screen, waitFor, cleanup, fireEvent } from "@testing-library/react"

const h = vi.hoisted(() => ({ apiFetch: vi.fn() }))
vi.mock("@/lib/api-fetch", () => ({ apiFetch: (...a: unknown[]) => h.apiFetch(...a) }))
vi.mock("@/hooks/use-workspace", () => ({ useWorkspace: () => ({ workspaceId: null, loading: false }) }))
vi.mock("@/hooks/use-mobile", () => ({ useIsMobile: () => false }))
vi.mock("sonner", () => ({ toast: { success: vi.fn(), warning: vi.fn(), error: vi.fn(), info: vi.fn() } }))
vi.mock("@/app/(dashboard)/admin/hooks/use-admin-websocket", () => ({ useAdminWebSocket: () => ({ keeperLiveEvents: [], keeperWsStatus: "connected" }) }))
vi.mock("@/components/features/admin/keeper-judge-card", () => ({ KeeperJudgeCard: ({ workspaceId }: { workspaceId: string }) => <div data-testid="card-judge" data-ws={workspaceId} /> }))
vi.mock("@/components/features/admin/keeper-profile-card", () => ({ KeeperProfileCard: () => <div data-testid="card-rules" /> }))
vi.mock("@/components/features/admin/judge-models-card", () => ({ JudgeModelsCard: () => <div data-testid="card-background" /> }))
vi.mock("@/components/features/admin/keeper-health-card", () => ({ KeeperHealthCard: () => null }))

import { SecurityPage } from "../security-page"
import { INSTANCE_SCOPE } from "@/lib/admin-api"

const ok = (body: unknown) => ({ ok: true, status: 200, json: async () => body })

beforeEach(() => {
  cleanup()
  window.history.replaceState(null, "", "/admin/security")
  h.apiFetch.mockReset()
  h.apiFetch.mockImplementation(async (u: string) => {
    const url = String(u)
    if (url.startsWith("/api/v1/system/keeper")) return ok({ enabled: true, model: "qwen", ollama_online: true, total_requests: 0, allow_count: 0, deny_count: 0 })
    if (url.startsWith("/api/v1/admin/security-posture")) return ok({ warnings: [] })
    if (url.startsWith("/api/v1/admin/instance/keeper/governance")) return ok({ defaults: { configured: false, enabled: false, deny_notify_min_risk: 7 }, workspaces: [] })
    if (url.startsWith("/api/v1/admin/instance/keeper/requests")) return ok({ items: [], total: 0, counts: { allow: 0, deny: 0, escalate: 0, pending: 0 }, by_workspace: [], by_type: {} })
    if (url.startsWith("/api/v1/admin/instance/keeper/health")) return ok({ workspaces: [] })
    return ok({})
  })
})

describe("Security with no workspace", () => {
  it("reads the Keeper's state and the posture without a workspace", async () => {
    render(<SecurityPage />)
    await waitFor(() => expect(h.apiFetch).toHaveBeenCalledWith("/api/v1/system/keeper"))
    expect(h.apiFetch).toHaveBeenCalledWith("/api/v1/admin/security-posture")
  })

  it("gives the instance-wide cards the instance scope", async () => {
    window.history.replaceState(null, "", "/admin/security?section=judge")
    render(<SecurityPage />)
    expect(await screen.findByTestId("card-judge")).toHaveAttribute("data-ws", INSTANCE_SCOPE)
    fireEvent.click(screen.getByRole("button", { name: /^Background checks/ }))
    expect(await screen.findByTestId("card-background")).toBeInTheDocument()
  })
})
