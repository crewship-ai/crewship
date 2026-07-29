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
