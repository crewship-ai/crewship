import { describe, it, expect, vi, beforeEach, afterEach } from "vitest"
import { render, screen, fireEvent, waitFor } from "@testing-library/react"

import { CreateAgentDialog } from "../create-agent-dialog"
import type { AgentRecord } from "../../agent-canvas-tabs/types"

// #3028 — the agent's restricted client execution profile lost its only UI
// when #3008 rewrote the config tab. It lives in Model and execution again,
// held as a draft like the provider account and the learning posture: nothing
// is sent until Save, and Save applies it after the agent itself.

vi.mock("next/navigation", () => ({ useRouter: () => ({ replace: vi.fn(), push: vi.fn() }) }))
vi.mock("sonner", () => ({ toast: { success: vi.fn(), error: vi.fn(), warning: vi.fn() } }))
vi.mock("@/hooks/use-abilities", () => ({
  useAbilities: () => ({ abilities: { can: () => true }, role: "OWNER", capabilities: [], hasCapability: () => false, loading: false }),
}))
vi.mock("@/lib/feature-gates", async (orig) => ({ ...(await orig<typeof import("@/lib/feature-gates")>()), AGENT_SELF_LEARNING: true }))

const CREWS = [{ id: "c1", slug: "engineering", name: "Engineering" }]

const agent = {
  id: "existing", workspace_id: "ws-1", crew_id: "c1", name: "Alice", slug: "alice",
  description: null, role_title: null, agent_role: "AGENT", lead_mode: null,
  status: "IDLE", cli_adapter: "CLAUDE_CODE", llm_provider: "ANTHROPIC", llm_model: "claude-sonnet-4-5",
  system_prompt: null, timeout_seconds: 1800, tool_profile: "CODING", memory_enabled: true,
  avatar_seed: null, avatar_style: null, updated_at: "2026-09-08T00:00:00Z", crew: null, pays_with: null,
  restricted_execution_profile: "disabled",
} as unknown as AgentRecord

type Call = { url: string; method: string; body?: string }
let calls: Call[] = []

function json(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } })
}

beforeEach(() => {
  calls = []
  vi.spyOn(global, "fetch").mockImplementation(async (input, init) => {
    const url = String(input)
    const method = init?.method ?? "GET"
    calls.push({ url, method, body: typeof init?.body === "string" ? init.body : undefined })
    if (url.includes("/restricted-execution") && method === "PUT") return json({ profile: JSON.parse(String(init?.body)).profile })
    if (url.includes("/api/v1/agents/existing") && method === "PATCH") return json({ ...agent })
    if (url.includes("/api/v1/integrations")) return json([])
    if (url.includes("/api/v1/notification-channels")) return json({ channels: [] })
    return json({})
  })
})
afterEach(() => vi.restoreAllMocks())

const writes = (part: string) => calls.filter((c) => c.url.includes(part) && c.method !== "GET")

function openModel() {
  render(<CreateAgentDialog workspaceId="ws-1" agent={agent} open onOpenChange={vi.fn()} defaultCrewSlug="engineering" crews={CREWS} onCreated={vi.fn()} />)
  fireEvent.click(screen.getByRole("button", { name: "Model and execution" }))
  return screen.getByLabelText("Restricted client execution") as HTMLSelectElement
}

describe("Edit agent: restricted client execution", () => {
  it("shows the agent's current profile", () => {
    expect(openModel().value).toBe("disabled")
  })

  it("choosing a profile sends nothing until Save", () => {
    fireEvent.change(openModel(), { target: { value: "responses_text" } })
    expect(writes("/restricted-execution")).toHaveLength(0)
  })

  it("Save writes the agent first, then the profile", async () => {
    fireEvent.change(openModel(), { target: { value: "native_api_key" } })
    fireEvent.click(screen.getByRole("button", { name: "Save changes" }))
    await waitFor(() => expect(writes("/restricted-execution")).toHaveLength(1))
    const order = calls.filter((c) => c.method !== "GET").map((c) => (c.url.includes("/restricted-execution") ? "profile" : c.url.includes("/api/v1/agents/existing") ? "agent" : "other"))
    expect(order.indexOf("agent")).toBeGreaterThanOrEqual(0)
    expect(order.indexOf("agent")).toBeLessThan(order.indexOf("profile"))
    const put = writes("/restricted-execution")[0]
    expect(put.method).toBe("PUT")
    expect(put.url).toContain("/api/v1/agents/existing/restricted-execution?workspace_id=ws-1")
    expect(JSON.parse(put.body!)).toEqual({ profile: "native_api_key" })
  })
})
