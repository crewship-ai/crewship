// Role gates on Admin › Security. The route is behind AdminRouteGate (an
// instance administrator), and the instance-wide controls disable again on
// useIsInstanceAdmin() !== true, the way the settings cards always have.
// Allow/Deny and Try a request are different: their routes are `roleManage`
// on the server (an OWNER or ADMIN member of that workspace, no instance-admin
// exception), so they are offered only to an instance administrator who is an
// OWNER or ADMIN of the request's (or the current) workspace. What needs a
// workspace (Try a request) is also absent without one. Each row of the tables
// below is a role; each assertion is a control.
import { describe, it, expect, vi, beforeEach } from "vitest"
import { render, screen, fireEvent, cleanup, within, waitFor } from "@testing-library/react"

const h = vi.hoisted(() => ({
  apiFetch: vi.fn(), ws: "ws-a" as string | null, admin: true as boolean | null,
  /** The caller's role in ws-a ("Dess"); null: not a member. */
  role: "OWNER" as string | null, mode: "trusted" as "trusted" | "restricted",
}))
vi.mock("@/lib/api-fetch", () => ({ apiFetch: (...a: unknown[]) => h.apiFetch(...a) }))
vi.mock("@/hooks/use-workspace", () => ({
  useWorkspace: () => {
    const workspaces = h.role ? [{ id: "ws-a", name: "Dess", slug: "dess", currentUserRole: h.role, currentUserAccessMode: h.mode }] : []
    return { workspaceId: h.ws, loading: false, workspaces, workspace: workspaces.find((w) => w.id === h.ws) ?? null }
  },
}))
vi.mock("@/hooks/use-auth", () => ({ useIsInstanceAdmin: () => h.admin }))
vi.mock("@/hooks/use-mobile", () => ({ useIsMobile: () => false }))
vi.mock("sonner", () => ({ toast: { success: vi.fn(), warning: vi.fn(), error: vi.fn(), info: vi.fn() } }))
vi.mock("@/app/(dashboard)/admin/hooks/use-admin-websocket", () => ({ useAdminWebSocket: () => ({ keeperLiveEvents: [], keeperWsStatus: "connected" }) }))
vi.mock("@/components/features/admin/keeper-judge-card", () => ({ KeeperJudgeCard: () => null }))
vi.mock("@/components/features/admin/keeper-profile-card", () => ({ KeeperProfileCard: () => null }))
vi.mock("@/components/features/admin/judge-models-card", () => ({ JudgeModelsCard: () => null }))
vi.mock("@/components/features/admin/keeper-health-card", () => ({ KeeperHealthCard: () => null }))
vi.mock("@/components/features/admin/keeper-governance-panel", () => ({ KeeperGovernancePanel: () => null }))
vi.mock("@/components/features/mcp/hooks/use-credentials", () => ({ useCredentials: () => ({ credentials: [] }) }))

import { SecurityPage } from "../security-page"

const res = (body: unknown, status = 200) => ({ ok: status < 400, status, json: async () => body })
const ENTRY = {
  id: "k4", request_type: "access", decision: "ESCALATE", workspace_id: "ws-a", workspace_name: "Dess", agent_id: "a1", agent_name: "Scout", crew_id: "c1",
  credential_id: "cr1", credential_name: "STRIPE_KEY", intent: "pay", command: null, reason: "Needs a person", risk_score: 6, exit_code: null,
  ollama_prompt: null, ollama_raw_response: null, created_at: "2026-09-29T10:00:00Z", decided_at: null,
}
const GOV = {
  defaults: { configured: true, enabled: false, deny_notify_min_risk: 7 },
  workspaces: [{
    workspace_id: "ws-a", workspace_name: "Dess", workspace_slug: "dess", configured: true, enabled: true, security_contact_user_id: "", deny_notify_min_risk: 7,
    watch_spec: "", watch_presets: null, require_second_approver: false, gov_model_provider: "", gov_model_id: "", gov_model_credential_id: "", auto_lease_seconds: 0, behavior_sample_every: 5,
  }],
}

beforeEach(() => {
  cleanup()
  h.ws = "ws-a"; h.admin = true; h.role = "OWNER"; h.mode = "trusted"
  window.history.replaceState(null, "", "/admin/security")
  h.apiFetch.mockReset()
  h.apiFetch.mockImplementation(async (u: string) => {
    const url = String(u)
    if (url.startsWith("/api/v1/system/keeper")) return res({ enabled: true, model: "m", ollama_online: true, total_requests: 1, allow_count: 0, deny_count: 0 })
    if (url.startsWith("/api/v1/admin/security-posture")) return res({ warnings: [] })
    if (url.startsWith("/api/v1/admin/instance/keeper/governance")) return res(GOV)
    if (url.startsWith("/api/v1/admin/instance/keeper/requests")) return res({ items: [ENTRY], total: 1, counts: { allow: 0, deny: 0, escalate: 1, pending: 0 }, by_workspace: [], by_type: { access: 1 } })
    if (url.startsWith("/api/v1/admin/instance/keeper/health")) return res({ workspaces: [] })
    if (/\/events/.test(url)) return res([])
    return res({})
  })
})

const ROLES: [string, boolean | null, boolean][] = [
  ["an instance administrator", true, true],
  ["a workspace administrator who is not an instance administrator", false, false],
  ["a person whose role is not known yet", null, false],
]

describe.each(ROLES)("%s", (_name, admin, may) => {
  it(`${may ? "may" : "may not"} tick workspaces for a change together`, async () => {
    h.admin = admin
    window.history.replaceState(null, "", "/admin/security?view=workspaces")
    render(<SecurityPage />)
    const box = within(await screen.findByRole("table", { name: "Workspaces" })).getByRole("checkbox", { name: "Select Dess" })
    expect(box.hasAttribute("disabled")).toBe(!may)
  })

})

// [who, instance admin, role in Dess (null: not a member), access mode, may]
const DECIDERS: [string, boolean | null, string | null, "trusted" | "restricted", boolean][] = [
  ["an instance administrator who is an Owner of the workspace", true, "OWNER", "trusted", true],
  ["an instance administrator who is an Admin of the workspace", true, "ADMIN", "trusted", true],
  ["an instance administrator who is a Manager of the workspace", true, "MANAGER", "trusted", false],
  ["an instance administrator who is a Member of the workspace", true, "MEMBER", "trusted", false],
  ["an instance administrator who is a Viewer of the workspace", true, "VIEWER", "trusted", false],
  ["an instance administrator who is not a member of the workspace", true, null, "trusted", false],
  ["an instance administrator who is a Guest Admin of the workspace", true, "ADMIN", "restricted", false],
  ["a workspace Owner who is not an instance administrator", false, "OWNER", "trusted", false],
  ["a person whose role is not known yet", null, "OWNER", "trusted", false],
]

describe.each(DECIDERS)("%s", (_name, admin, role, mode, may) => {
  it(`${may ? "may" : "may not"} Try a request`, async () => {
    h.admin = admin; h.role = role; h.mode = mode
    window.history.replaceState(null, "", "/admin/security?view=activity")
    render(<SecurityPage />)
    const button = await screen.findByRole("button", { name: /Try a request/ })
    await waitFor(() => expect(button.hasAttribute("disabled")).toBe(!may))
    if (may) expect(screen.queryByTestId("ask-not-allowed")).toBeNull()
    else expect(screen.getByTestId("ask-not-allowed")).toHaveTextContent("Only an Owner or Admin of Dess can try a request")
  })

  it(`${may ? "may" : "may not"} allow or deny a request that waits`, async () => {
    h.admin = admin; h.role = role; h.mode = mode
    window.history.replaceState(null, "", "/admin/security?view=activity")
    render(<SecurityPage />)
    const table = await screen.findByRole("region", { name: "Activity" })
    await waitFor(() => expect(within(table).getAllByText("STRIPE_KEY").length).toBeGreaterThan(0))
    fireEvent.click(within(table).getAllByText("STRIPE_KEY")[0])
    const decide = await screen.findByRole("region", { name: "Decide" })
    expect(within(decide).queryByRole("button", { name: "Allow" }) !== null).toBe(may)
    expect(within(decide).queryByRole("button", { name: "Deny" }) !== null).toBe(may)
    expect(decide.textContent).toContain("Waiting for a person")
    if (!may) expect(decide.textContent).toContain("Only an Owner or Admin of Dess can decide this request.")
    expect(decide.textContent).not.toContain("instance administrator")
  })
})

describe("what needs a workspace", () => {
  it("Try a request is left out for an administrator in no workspace, with the reason, whatever the role", async () => {
    h.ws = null
    window.history.replaceState(null, "", "/admin/security?view=activity")
    render(<SecurityPage />)
    expect(await screen.findByTestId("ask-unavailable")).toHaveTextContent("needs a workspace")
    expect(screen.queryByRole("button", { name: /Try a request/ })).toBeNull()
  })
})
