// The agent↔integration binding toggle must not claim a mutation the
// server refused (#1594).
//
// apiFetch resolves on 4xx/5xx rather than throwing, so a bare
// `await apiFetch(...)` followed by a refetch reports nothing: the
// refetch quietly restores the old state and the operator is never told
// their click did not take. handleDelete in the same file already
// checks res.ok, so this was an inconsistency within one component, not
// a missing convention.
//
// Driven through the button the user actually clicks — expand a server
// row, click an agent chip — rather than by calling the handler. A
// review of #1568 found tests exercising a helper while the deciding
// code sat untested inside a 1140-line component, which is how making
// "Retry" destroy a task stayed invisible to the suite.

import { describe, it, expect, vi, beforeEach } from "vitest"
import { render, screen, fireEvent, waitFor } from "@testing-library/react"
import { toast } from "sonner"

import IntegrationsPage from "../page"

const h = vi.hoisted(() => ({ apiFetch: vi.fn() }))

vi.mock("sonner", () => ({
  toast: { success: vi.fn(), error: vi.fn(), info: vi.fn() },
}))

vi.mock("@/lib/api-fetch", () => ({
  apiFetch: (...args: unknown[]) => h.apiFetch(...args),
}))

// The legacy self-hosted MCP UI is behind NEXT_PUBLIC_LEGACY_MCP_INTEGRATIONS
// (default off) — the toggle only exists there.
vi.mock("@/lib/feature-flags", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/lib/feature-flags")>()),
  legacyMcpIntegrations: () => true,
}))

vi.mock("@/hooks/use-workspace", () => ({
  useWorkspace: () => ({ workspaceId: "ws-1", loading: false }),
}))

vi.mock("@/hooks/use-abilities", () => ({
  useAbilities: () => ({ abilities: { can: () => true } }),
}))

const SERVER = {
  id: "srv-1",
  crew_id: "crew-1",
  crew_name: "Engineering",
  crew_slug: "engineering",
  name: "github",
  display_name: "GitHub",
  transport: "http",
  endpoint: "https://example.invalid/mcp",
  command: null,
  args_json: null,
  env_json: null,
  icon: null,
  enabled: true,
  created_at: "2026-07-31T00:00:00Z",
  updated_at: "2026-07-31T00:00:00Z",
  agent_binding_count: 0,
  auth_status: "connected" as const,
}

const AGENT = { id: "agent-1", name: "Scout", slug: "scout" }

function json(body: unknown, status = 200) {
  return Promise.resolve({
    ok: status >= 200 && status < 300,
    status,
    json: () => Promise.resolve(body),
  })
}

/** Routes the page's read traffic; `onBind` answers the binding write. */
function seedFetch(onBind: () => Promise<unknown>) {
  h.apiFetch.mockImplementation((url: string, init?: { method?: string }) => {
    if (init?.method === "POST" || init?.method === "DELETE") return onBind()
    if (url.startsWith("/api/v1/integrations/crews")) return json([SERVER])
    if (url.startsWith("/api/v1/crews")) return json([{ id: "crew-1", name: "Engineering", slug: "engineering" }])
    if (url.startsWith("/api/v1/agents?")) return json([AGENT])
    if (url.startsWith(`/api/v1/agents/${AGENT.id}/integrations`)) return json([])
    return json([])
  })
}

/** Renders, expands the server row, and returns the agent chip. */
async function openAgentChip() {
  render(<IntegrationsPage />)
  const row = await screen.findByText("GitHub")
  fireEvent.click(row)
  return screen.findByRole("button", { name: /Scout/ })
}

describe("agent↔integration binding toggle", () => {
  beforeEach(() => {
    vi.clearAllMocks()
  })

  it("surfaces the server's refusal instead of claiming success", async () => {
    seedFetch(() => json({ detail: "crew is not linked to this integration" }, 409))
    fireEvent.click(await openAgentChip())

    await waitFor(() => {
      expect(toast.error).toHaveBeenCalledWith("crew is not linked to this integration")
    })
    expect(toast.success).not.toHaveBeenCalled()
  })

  it("falls back to a readable message when the error body carries none", async () => {
    seedFetch(() => json(null, 500))
    fireEvent.click(await openAgentChip())

    await waitFor(() => {
      expect(toast.error).toHaveBeenCalledWith(expect.stringMatching(/access/i))
    })
  })

  // The success path must stay quiet-and-correct: a refetch, no error.
  it("does not report an error when the server accepts the binding", async () => {
    seedFetch(() => json({ id: "bind-1", mcp_server_id: SERVER.id }, 201))
    fireEvent.click(await openAgentChip())

    await waitFor(() => {
      expect(h.apiFetch).toHaveBeenCalledWith(
        expect.stringContaining(`/api/v1/agents/${AGENT.id}/integrations`),
        expect.objectContaining({ method: "POST" }),
      )
    })
    expect(toast.error).not.toHaveBeenCalled()
  })
})
