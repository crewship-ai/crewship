// Admin → Runtime showed "Could not load … (HTTP 400)" for both the security
// posture and the memory configuration. Same cause as the GDPR panel: the
// admin API is workspace-scoped by middleware, and these cards asked for
// their data without workspace_id, so the server refused before the handler
// ran. Three cards, one missing query parameter, and nothing caught it
// because nothing tested the URL they build.

import { describe, it, expect, vi, beforeEach } from "vitest"
import { render, waitFor, cleanup } from "@testing-library/react"

import { SecurityPostureCard } from "../security-posture-card"
import { MemoryConfigCard } from "../memory-config-card"

const h = vi.hoisted(() => ({ apiFetch: vi.fn() }))

vi.mock("sonner", () => ({ toast: { success: vi.fn(), error: vi.fn(), info: vi.fn() } }))
vi.mock("@/lib/api-fetch", () => ({ apiFetch: (...args: unknown[]) => h.apiFetch(...args) }))
vi.mock("@/hooks/use-abilities", () => ({
  useAbilities: () => ({ abilities: { can: () => true }, role: "OWNER", hasCapability: () => false, loading: false }),
}))
// The governance panel reads the credential list through this hook. Stubbed so
// the shaped-per-route mock below only has to describe the ADMIN routes, which
// are what this file is about.
vi.mock("@/components/features/mcp/hooks/use-credentials", () => ({
  useCredentials: () => ({ credentials: [], loading: false, error: null, refetch: vi.fn() }),
}))

beforeEach(() => {
  cleanup()
  h.apiFetch.mockReset()
  h.apiFetch.mockResolvedValue({
    ok: true,
    status: 200,
    json: async () => ({ warnings: [], retention_days: 30 }),
    text: async () => "",
  })
})

describe("admin cards are workspace-scoped", () => {
  it("security posture asks within a workspace", async () => {
    render(<SecurityPostureCard workspaceId="ws-1" />)
    await waitFor(() => expect(h.apiFetch).toHaveBeenCalled())
    expect(String(h.apiFetch.mock.calls[0][0])).toContain("workspace_id=ws-1")
  })

  it("memory configuration asks within a workspace", async () => {
    render(<MemoryConfigCard workspaceId="ws-1" />)
    await waitFor(() => expect(h.apiFetch).toHaveBeenCalled())
    expect(String(h.apiFetch.mock.calls[0][0])).toContain("workspace_id=ws-1")
  })

  // Before the id resolves there is nothing to scope to, and firing the
  // request anyway is how you get a 400 rendered as "could not load".
  it("neither asks before the workspace is known", () => {
    render(<SecurityPostureCard workspaceId={null} />)
    render(<MemoryConfigCard workspaceId={null} />)
    expect(h.apiFetch).not.toHaveBeenCalled()
  })
})

// The Keeper cards, which made the same mistake a fourth time — and made it
// worse, because each of them swallowed the 400 differently: the evaluator card
// silently fell back to read-only (so five settings looked deliberately
// unchangeable), and the model picker rendered the raw server message,
// "workspace_id is required", inside a search dialog.
//
// The fix is structural — every admin call goes through adminFetch, which cannot
// build a URL without a workspace — so this is the net under it: whatever a card
// asks for, EVERY admin request it makes carries the scope. Asserting on all
// calls rather than the first is the whole point; the earlier bugs were all in a
// second or third request that nothing looked at.
describe("keeper admin cards scope every request, not just the first", () => {
  it("no /api/v1/admin request escapes without a workspace", async () => {
    // Shaped per route. The blanket payload above is enough for the two simple
    // cards; the Keeper cards read typed fields, and feeding them the wrong shape
    // would test the mock rather than the URLs.
    h.apiFetch.mockImplementation((url: string) => {
      const u = String(url)
      const body =
        u.includes("/keeper/config") ? {
          enabled: { value: false, source: "env", editable: true },
          judge_provider: { value: "ollama", source: "default", editable: false },
          judge_endpoint_url: { value: "", source: "default", editable: true },
          judge_wire: { value: "ollama", source: "default", editable: false },
          judge_model: { value: "", source: "default", editable: true },
          judge_timeout_ms: { value: 20000, source: "default", editable: true },
          overridden: false, judge_configured: false,
        }
        : u.includes("/keeper/aux") ? { slots: [], providers: [], judge_provider: "", judge_model: "", any_overridden: false }
        : u.includes("/keeper/governance") ? { configured: false, enabled: false, deny_notify_min_risk: 7 }
        : u.includes("/keeper/judge/models") ? { endpoint: "", models: [] }
        : u.includes("aux-status") ? { subsystems: [] }
        : { warnings: [], retention_days: 30, credentials: [], members: [] }
      return Promise.resolve({ ok: true, status: 200, json: async () => body, text: async () => "" })
    })

    const { KeeperJudgeCard } = await import("../keeper-judge-card")
    const { JudgeModelsCard } = await import("../judge-models-card")
    const { KeeperGovernancePanel } = await import("../keeper-governance-panel")

    render(<KeeperJudgeCard workspaceId="ws-1" />)
    render(<JudgeModelsCard workspaceId="ws-1" />)
    render(<KeeperGovernancePanel workspaceId="ws-1" serverEnabled={true} />)
    render(<KeeperGovernancePanel workspaceId="ws-1" serverEnabled={true} section="judge" />)

    await waitFor(() => expect(h.apiFetch).toHaveBeenCalled())

    const unscoped = h.apiFetch.mock.calls
      .map((c) => String(c[0]))
      .filter((u) => u.includes("/api/v1/admin/") && !u.includes("workspace_id="))
    expect(unscoped).toEqual([])
  })

  it("adminFetch refuses rather than firing a request that must 400", async () => {
    const { adminFetch } = await import("@/lib/admin-api")
    // A rejection names the caller and the route. A 400 from the server looks
    // like a server problem and sends whoever is debugging it to the backend —
    // which is where the last four hours of this went.
    await expect(adminFetch("/api/v1/admin/keeper/aux", null)).rejects.toThrow(/needs a workspace/i)
    expect(h.apiFetch).not.toHaveBeenCalled()
  })

  it("adminFetch keeps a query string the caller already built", async () => {
    const { withWorkspace } = await import("@/lib/admin-api")
    // The judge model discovery passes ?endpoint=…; losing it would silently
    // inventory the wrong server.
    const url = withWorkspace("/api/v1/admin/keeper/judge/models?endpoint=http%3A%2F%2Fx%3A11434", "ws-1")
    expect(url).toContain("endpoint=http%3A%2F%2Fx%3A11434")
    expect(url).toContain("workspace_id=ws-1")
  })
})
