// Part C: the Admin console works for an instance administrator who belongs to
// no workspace. The instance-wide tabs load without one (no workspace_id, no
// endless skeleton); what shows one workspace's data says to choose one.
import { describe, it, expect, vi, beforeEach } from "vitest"
import { render, screen, waitFor, cleanup, renderHook } from "@testing-library/react"

const h = vi.hoisted(() => ({ apiFetch: vi.fn() }))
vi.mock("@/lib/api-fetch", () => ({ apiFetch: (...a: unknown[]) => h.apiFetch(...a) }))
vi.mock("sonner", () => ({ toast: { success: vi.fn(), error: vi.fn(), info: vi.fn(), warning: vi.fn() } }))

import { RateLimitsTab } from "../tabs/rate-limits-tab"
import { NotificationsTab } from "../tabs/notifications-tab"
import { useAdminOverview } from "../hooks/use-admin-overview"

const ok = (body: unknown) => ({ ok: true, status: 200, json: async () => body }) as unknown as Response
const urls = () => h.apiFetch.mock.calls.map(([u]) => String(u))

beforeEach(() => {
  cleanup()
  h.apiFetch.mockReset()
  h.apiFetch.mockImplementation(async (u: string) => {
    const url = String(u)
    if (url.startsWith("/api/v1/admin/rate-limits")) return ok({ limiters: [{ key: "http.auth_per_min", group: "HTTP (per-IP)", display_name: "Auth endpoints", description: "", unit: "req/min", default: 10, value: 10, min: 1, max: 100, overridden: false }] })
    if (url.startsWith("/api/v1/notification-providers")) return ok([{ provider: "slack", name: "Slack", enabled: true }])
    if (url.startsWith("/api/v1/admin/security-posture")) return ok({ warnings: [], email_configured: false })
    return ok({})
  })
})

describe("with no workspace", () => {
  it("Limits loads the instance's limits, without a workspace_id", async () => {
    render(<RateLimitsTab workspaceId={null} />)
    expect(await screen.findByText("Auth endpoints")).toBeInTheDocument()
    expect(urls()).toContain("/api/v1/admin/rate-limits")
  })

  it("Notifications loads the providers and does not ask for one workspace's channels", async () => {
    render(<NotificationsTab workspaceId={null} />)
    await waitFor(() => expect(urls()).toContain("/api/v1/notification-providers"))
    expect(urls().some((u) => u.startsWith("/api/v1/notification-channels"))).toBe(false)
    expect(urls().some((u) => u.includes("workspace_id="))).toBe(false)
  })

  it("Overview reads the instance-wide cards and leaves the per-workspace ones", async () => {
    const r = renderHook(() => useAdminOverview(null, true))
    await waitFor(() => expect(urls()).toContain("/api/v1/admin/health"))
    for (const u of ["/api/v1/admin/security-posture", "/api/v1/system/aux-status"]) expect(urls()).toContain(u)
    expect(urls().some((u) => u.includes("workspace_id="))).toBe(false)
    expect(urls().some((u) => u.startsWith("/api/v1/crewshipd") || u.startsWith("/api/v1/metrics/timeseries"))).toBe(false)
    r.unmount()
  })
})
