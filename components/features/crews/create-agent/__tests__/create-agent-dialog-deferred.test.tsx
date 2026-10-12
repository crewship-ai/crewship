import { describe, it, expect, vi, beforeEach, afterEach } from "vitest"
import { render, screen, fireEvent, waitFor, within } from "@testing-library/react"

import { CreateAgentDialog } from "../create-agent-dialog"
import type { AgentRecord } from "../../agent-canvas-tabs/types"

// The Edit dialog promises "Changes are saved together when you choose Save
// changes". Two rows used to break that promise and write the moment they were
// touched: the provider account ("Pays with") and the learning posture. They
// are now part of the draft — nothing is sent until Save, Save applies them
// after the agent itself, and closing without saving leaves them as they were.

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
} as unknown as AgentRecord

const seat = {
  id: "c_claude", name: "Claude Max · pavel", provider: "ANTHROPIC", status: "ACTIVE",
  login: {
    mode: "subscription", provider: "ANTHROPIC", plan: "max", plan_label: "Max", owner_user_id: "u1", owner_email: "pavel@unify.cz",
    expires_at: null, refresh: { supported: false, status: "none", last_at: null, next_at: null, error: null },
    quota: null, delivery: { kind: "env", target: "CLAUDE_CODE_OAUTH_TOKEN" }, pays_for: { agents: 0, crews: 0 },
  },
}

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
    if (url.includes("/api/v1/integrations")) return json([])
    if (url.includes("/api/v1/notification-channels")) return json({ channels: [] })
    if (url.includes("kind=provider_login")) return json([seat])
    if (url.includes("/credentials/bindings") && method === "POST") return json({ id: "b_new" }, 201)
    if (url.includes("/credentials/bindings")) return json({ bindings: [] })
    if (url.includes("/learning") && method === "PATCH") return json({ agent_id: "existing", enabled: true })
    if (url.includes("/learning")) return json({ agent_id: "existing", enabled: false })
    if (url.includes("/api/v1/agents/existing") && method === "PATCH") return json({ ...agent })
    return json({})
  })
})
afterEach(() => vi.restoreAllMocks())

const writes = (part: string) => calls.filter((c) => c.url.includes(part) && c.method !== "GET")

function renderEdit(onOpenChange = vi.fn()) {
  render(<CreateAgentDialog workspaceId="ws-1" agent={agent} open onOpenChange={onOpenChange} defaultCrewSlug="engineering" crews={CREWS} onCreated={vi.fn()} />)
  return onOpenChange
}

async function pickSeat() {
  fireEvent.click(screen.getByRole("button", { name: "Permissions" }))
  fireEvent.click(screen.getByRole("button", { name: "Pays with" }))
  const list = await screen.findByRole("listbox", { name: "Provider logins" })
  fireEvent.click(await within(list).findByRole("option", { name: /Claude Max · pavel/ }))
}

describe("Edit agent: provider account is part of the draft", () => {
  it("picking a seat sends nothing until Save", async () => {
    renderEdit()
    await pickSeat()
    await waitFor(() => expect(screen.getByRole("button", { name: "Pays with" })).toHaveTextContent("Claude Max · pavel"))
    expect(writes("/credentials/bindings")).toHaveLength(0)
  })

  it("Save writes the agent first, then the binding", async () => {
    renderEdit()
    await pickSeat()
    fireEvent.click(screen.getByRole("button", { name: "Save changes" }))
    await waitFor(() => expect(writes("/credentials/bindings")).toHaveLength(1))
    const order = calls.filter((c) => c.method !== "GET").map((c) => (c.url.includes("/credentials/bindings") ? "binding" : c.url.includes("/api/v1/agents/existing") ? "agent" : "other"))
    expect(order.indexOf("agent")).toBeGreaterThanOrEqual(0)
    expect(order.indexOf("agent")).toBeLessThan(order.indexOf("binding"))
    expect(JSON.parse(writes("/credentials/bindings")[0].body!)).toMatchObject({ credential_id: "c_claude", scope: "AGENT", agent_id: "existing" })
  })

  it("a picked seat counts as unsaved: closing asks, and discarding leaves the seat as it was", async () => {
    const onOpenChange = renderEdit()
    await pickSeat()
    fireEvent.click(screen.getByRole("button", { name: "Cancel" }))
    expect(await screen.findByText(/Discard this agent\?/)).toBeInTheDocument()
    fireEvent.click(screen.getByRole("button", { name: "Discard" }))
    await waitFor(() => expect(onOpenChange).toHaveBeenCalledWith(false))
    expect(writes("/credentials/bindings")).toHaveLength(0)
  })

  it("no longer says billing applies immediately", () => {
    renderEdit()
    fireEvent.click(screen.getByRole("button", { name: "Permissions" }))
    expect(screen.queryByText(/applies immediately/i)).toBeNull()
  })
})

describe("Edit agent: learning posture is part of the draft", () => {
  it("flipping the switch and giving a reason sends nothing until Save, then PATCHes after the agent", async () => {
    renderEdit()
    fireEvent.click(screen.getByRole("button", { name: "Chat" }))
    fireEvent.click(await screen.findByRole("switch", { name: "Toggle self-improving mode" }))
    fireEvent.change(screen.getByPlaceholderText(/why grant autonomy/i), { target: { value: "pilot" } })
    expect(screen.queryByRole("button", { name: "Confirm" })).toBeNull()
    expect(writes("/learning")).toHaveLength(0)
    fireEvent.click(screen.getByRole("button", { name: "Save changes" }))
    await waitFor(() => expect(writes("/learning")).toHaveLength(1))
    expect(JSON.parse(writes("/learning")[0].body!)).toEqual({ enabled: true, reason: "pilot" })
    const methods = calls.filter((c) => c.method !== "GET").map((c) => c.url)
    expect(methods.findIndex((u) => u.includes("/api/v1/agents/existing?"))).toBeLessThan(methods.findIndex((u) => u.includes("/learning")))
  })
})
