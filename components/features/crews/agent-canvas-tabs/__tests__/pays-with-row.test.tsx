// "Pays with" (PRD provider-logins §6.3, wireframe AgentPaysWith). The picker
// lists every seat, greys the wrong provider's, writes an AGENT binding for
// the chosen one, and warns when the choice is a seat at its limit with no
// pool partner.

import { describe, it, expect, vi, beforeEach } from "vitest"
import { render, screen, fireEvent, waitFor, within } from "@testing-library/react"
import { PaysWithRow, applyPaysWith, releaseBedrockSeat, seatProvider } from "../pays-with-row"
import type { LoginCredential, ProviderLogin } from "@/lib/credentials/provider-logins"

const h = vi.hoisted(() => ({
  role: "OWNER" as string,
  apiFetch: vi.fn(),
  toast: { success: vi.fn(), error: vi.fn(), info: vi.fn() },
}))
vi.mock("@/lib/api-fetch", () => ({ apiFetch: (...args: unknown[]) => h.apiFetch(...args) }))
vi.mock("sonner", () => ({ toast: h.toast }))
vi.mock("@/hooks/use-abilities", async () => {
  const { defineAbilitiesFor } = await import("@/lib/permissions/abilities")
  return {
    useAbilities: () => ({ abilities: defineAbilitiesFor(h.role as never), role: h.role, capabilities: [], hasCapability: () => false, loading: false }),
  }
})

const DAY = 24 * 3600 * 1000

function login(over: Partial<ProviderLogin> = {}): ProviderLogin {
  return {
    mode: "subscription",
    provider: "OPENAI",
    plan: "plus",
    plan_label: "ChatGPT Plus",
    owner_user_id: "u2",
    owner_email: "jana@unify.cz",
    // A minute of slack: the fixture's clock runs a few ms ahead of the render's.
    expires_at: new Date(Date.now() + 2 * DAY + 60_000).toISOString(),
    refresh: { supported: true, status: "ok", last_at: null, next_at: null, error: null },
    quota: null,
    delivery: { kind: "file", target: ".codex/auth.json" },
    pays_for: { agents: 1, crews: 0 },
    ...over,
  }
}

const chatgpt: LoginCredential = { id: "c_gpt", name: "ChatGPT Plus · jana", provider: "OPENAI", status: "ACTIVE", login: login({
  quota: { window_5h_pct: 100, window_weekly_pct: 71, resets_at: new Date(Date.now() + 2 * 3600_000).toISOString() },
}) }
const openaiKey: LoginCredential = { id: "c_key", name: "OpenAI API · workspace", provider: "OPENAI", status: "ACTIVE", login: login({
  mode: "api_key", plan: null, plan_label: null, owner_email: null, delivery: { kind: "env", target: "OPENAI_API_KEY" }, refresh: { supported: false, status: "none", last_at: null, next_at: null, error: null },
}) }
const claude: LoginCredential = { id: "c_claude", name: "Claude Max · pavel", provider: "ANTHROPIC", status: "ACTIVE", login: login({
  provider: "ANTHROPIC", plan: "max", plan_label: "Max 20×", owner_email: "pavel@unify.cz", delivery: { kind: "env", target: "CLAUDE_CODE_OAUTH_TOKEN" }, refresh: { supported: false, status: "none", last_at: null, next_at: null, error: null },
}) }

const bedrockSeat: LoginCredential = { id: "c_bedrock", name: "Bedrock EU", provider: "AWS_BEDROCK", status: "ACTIVE", login: login({
  mode: "api_key", provider: "AWS_BEDROCK", plan: null, plan_label: null, owner_email: null, delivery: { kind: "sidecar", target: "AWS_BEARER_TOKEN_BEDROCK" }, refresh: { supported: false, status: "none", last_at: null, next_at: null, error: null },
  bedrock: { region: "eu-central-1", models: ["eu.anthropic.claude-sonnet-5-5", "eu.anthropic.claude-opus-5-5"], auth_method: "bedrock_api_key", eu_only: true },
}) }

function ok(body: unknown, status = 200) {
  return { ok: true, status, json: async () => body } as unknown as Response
}

function serve(seats: LoginCredential[], bindings: unknown[] = []) {
  h.apiFetch.mockImplementation(async (url: unknown, init?: { method?: string }) => {
    const u = String(url)
    if (u.startsWith("/api/v1/credentials?") && u.includes("kind=provider_login")) return ok(seats)
    if (u.startsWith("/api/v1/credentials/bindings") && init?.method === "POST") return ok({ id: "b_new" }, 201)
    if (u.startsWith("/api/v1/credentials/bindings?")) return ok({ bindings })
    if (u.startsWith("/api/v1/credentials/bindings/") && init?.method === "DELETE") return ok({})
    if (u.includes("/bedrock-assign") && init?.method === "POST") return ok({ assigned: 1 })
    throw new Error(`unexpected ${u}`)
  })
}

function renderRow(over: Partial<React.ComponentProps<typeof PaysWithRow>> = {}) {
  return render(
    <PaysWithRow workspaceId="ws1" agentId="a1" agentName="codex-reviewer" cliAdapter="CODEX_CLI" {...over} />,
  )
}

beforeEach(() => {
  h.role = "OWNER"
  h.apiFetch.mockReset()
  h.toast.success.mockReset()
  h.toast.error.mockReset()
})

it.each(["MANAGER", "MEMBER", "VIEWER"])("%s sees only provider identity, without account details or a picker", (role) => {
  h.role = role
  renderRow({ paysWith: { provider: "OPENAI", restricted: true } })
  expect(screen.getByText("OpenAI")).toBeInTheDocument()
  expect(screen.queryByRole("button", { name: /pays with/i })).not.toBeInTheDocument()
  expect(screen.queryByText(/jana|ChatGPT Plus/)).not.toBeInTheDocument()
  expect(h.apiFetch).not.toHaveBeenCalled()
})

describe("the picker", () => {
  it("lists every seat; the other provider's are visible and disabled as 'wrong provider'", async () => {
    serve([chatgpt, openaiKey, claude])
    renderRow()
    fireEvent.click(await screen.findByRole("button", { name: /pays with/i }))
    const list = await screen.findByRole("listbox", { name: /provider logins/i })
    const options = within(list).getAllByRole("option")
    expect(options).toHaveLength(3)
    expect(within(list).getByText("OpenAI seats in scope".replace("OpenAI seats in scope", "ChatGPT Plus · jana"))).toBeInTheDocument()
    const wrong = options.find((o) => o.textContent?.includes("Claude Max · pavel"))!
    expect(wrong).toBeDisabled()
    expect(wrong).toHaveTextContent("wrong provider")
    expect(wrong).toHaveTextContent("OpenAI models cannot run on this")
    const right = options.find((o) => o.textContent?.includes("ChatGPT Plus · jana"))!
    expect(right).not.toBeDisabled()
    expect(right).toHaveTextContent(/subscription · ChatGPT Plus · expires in 2 d \(auto\)/)
    expect(within(right).getByTestId("login-status-at_limit")).toBeInTheDocument()
  })

  it("OpenCode with no known model provider takes any provider's login — nothing is greyed", async () => {
    serve([chatgpt, claude])
    renderRow({ cliAdapter: "OPENCODE" })
    fireEvent.click(await screen.findByRole("button", { name: /pays with/i }))
    const options = await screen.findAllByRole("option")
    for (const o of options) expect(o).not.toBeDisabled()
  })

  it("OpenCode on Anthropic models offers only Anthropic logins", async () => {
    serve([chatgpt, claude])
    renderRow({ cliAdapter: "OPENCODE", llmProvider: "ANTHROPIC" })
    fireEvent.click(await screen.findByRole("button", { name: /pays with/i }))
    const options = await screen.findAllByRole("option")
    expect(options.find((o) => o.textContent?.includes("ChatGPT Plus · jana"))).toBeDisabled()
    expect(options.find((o) => o.textContent?.includes("Claude Max · pavel"))).not.toBeDisabled()
  })

  it("OpenCode on a model of a provider without its own llm_provider (xai/…) takes any login but Bedrock", async () => {
    const xai: LoginCredential = { ...openaiKey, id: "c_xai", name: "xAI · workspace", provider: "XAI", login: { ...openaiKey.login!, provider: "XAI" } }
    serve([xai, claude, bedrockSeat])
    renderRow({ cliAdapter: "OPENCODE", llmProvider: "OPENCODE", llmModel: "xai/grok-5" })
    fireEvent.click(await screen.findByRole("button", { name: /pays with/i }))
    const options = await screen.findAllByRole("option")
    expect(options.find((o) => o.textContent?.includes("xAI · workspace"))).not.toBeDisabled()
    expect(options.find((o) => o.textContent?.includes("Bedrock EU"))).toBeDisabled()
  })

  it("a Bedrock account pays only for a Bedrock agent", async () => {
    serve([chatgpt, bedrockSeat])
    renderRow({ cliAdapter: "OPENCODE" })
    fireEvent.click(await screen.findByRole("button", { name: /pays with/i }))
    const options = await screen.findAllByRole("option")
    expect(options.find((o) => o.textContent?.includes("Bedrock EU"))).toBeDisabled()
    expect(options.find((o) => o.textContent?.includes("ChatGPT Plus · jana"))).not.toBeDisabled()
  })

  it("assigns a Bedrock account through bedrock-assign, keeping a permitted model", async () => {
    serve([bedrockSeat])
    renderRow({ cliAdapter: "OPENCODE", llmProvider: "AWS_BEDROCK", llmModel: "amazon-bedrock/eu.anthropic.claude-opus-5-5" })
    fireEvent.click(await screen.findByRole("button", { name: /pays with/i }))
    fireEvent.click(await screen.findByRole("option", { name: /Bedrock EU/ }))
    await waitFor(() => expect(h.toast.success).toHaveBeenCalled())
    const call = h.apiFetch.mock.calls.find(([u]) => String(u).includes("/bedrock-assign"))!
    expect(String(call[0])).toContain("/api/v1/credentials/c_bedrock/bedrock-assign?workspace_id=ws1")
    expect(JSON.parse((call[1] as { body: string }).body)).toEqual({ model: "eu.anthropic.claude-opus-5-5", agent_ids: ["a1"], crew_ids: [] })
    expect(h.apiFetch.mock.calls.some(([u, i]) => String(u).startsWith("/api/v1/credentials/bindings?") && (i as { method?: string })?.method === "POST")).toBe(false)
  })

  it("shows the server's pays_with when the agent carries one", async () => {
    serve([chatgpt, claude])
    renderRow({ paysWith: { credential_id: "c_gpt", name: "ChatGPT Plus · jana", login: chatgpt.login! } })
    expect(await screen.findByRole("button", { name: /pays with/i })).toHaveTextContent("ChatGPT Plus · jana")
  })

  it("reads nothing until opened — the tab already carries pays_with", async () => {
    serve([chatgpt])
    renderRow()
    expect(screen.getByRole("button", { name: /pays with/i })).toHaveTextContent(/inherits from the crew or workspace/i)
    expect(h.apiFetch).not.toHaveBeenCalled()
  })

  it("once opened, the agent's own binding stands in for a server that sends no pays_with", async () => {
    serve([chatgpt, claude], [{ id: "b1", credential_id: "c_gpt", scope: "AGENT", agent_id: "a1", slot: "OPENAI_API_KEY" }])
    renderRow()
    fireEvent.click(screen.getByRole("button", { name: /pays with/i }))
    await waitFor(() => expect(screen.getByRole("button", { name: /pays with/i })).toHaveTextContent("ChatGPT Plus · jana"))
  })

  it("a reader who cannot bind sees the value and no control", async () => {
    h.role = "MEMBER"
    serve([chatgpt])
    renderRow()
    expect(screen.queryByRole("button", { name: /pays with/i })).not.toBeInTheDocument()
    expect(screen.getByText("No provider reported")).toBeInTheDocument()
  })
})

describe("choosing a seat", () => {
  it.each([true, false])("failed swap restores the previous binding when possible (restore=%s)", async (restore) => {
    let bound = true
    const changed = vi.fn()
    h.apiFetch.mockImplementation(async (url: unknown, init?: { method?: string; body?: string }) => {
      const u = String(url)
      if (u.includes("kind=provider_login")) return ok([chatgpt, openaiKey])
      if (init?.method === "DELETE") { bound = false; return ok({}) }
      if (init?.method === "POST") {
        const payload = JSON.parse(init.body!)
        if (payload.credential_id === "c_gpt" && restore) { bound = true; return ok({id:"restored"},201) }
        return {ok:false,status:500,json:async()=>({error:"Assignment failed"})}
      }
      return ok({bindings:bound ? [{id:"old",credential_id:"c_gpt",scope:"AGENT",agent_id:"a1",slot:"OPENAI_API_KEY"}] : []})
    })
    renderRow({ paysWith: {credential_id:"c_gpt",name:chatgpt.name,login:chatgpt.login!}, onChanged:changed })
    fireEvent.click(screen.getByRole("button",{name:/pays with/i}))
    const options = await screen.findAllByRole("option")
    fireEvent.click(options.find(o=>o.textContent?.includes(openaiKey.name))!)
    await waitFor(()=>expect(changed).toHaveBeenCalled())
    expect(h.toast.success).not.toHaveBeenCalled()
    expect(h.apiFetch.mock.calls.filter(([,init])=>init?.method === "POST")).toHaveLength(2)
    await waitFor(()=>expect(screen.getByRole("button",{name:/pays with/i}).textContent?.includes(chatgpt.name)).toBe(restore))
    if (!restore) expect(h.toast.error).toHaveBeenCalledWith(expect.stringContaining("could not be restored"), { duration: Infinity })
  })
  it("writes an AGENT binding under the seat's slot", async () => {
    serve([chatgpt, openaiKey])
    renderRow()
    fireEvent.click(await screen.findByRole("button", { name: /pays with/i }))
    const options = await screen.findAllByRole("option")
    fireEvent.click(options.find((o) => o.textContent?.includes("OpenAI API · workspace"))!)
    await waitFor(() => expect(h.toast.success).toHaveBeenCalled())
    const post = h.apiFetch.mock.calls.find(([u, i]) => String(u).startsWith("/api/v1/credentials/bindings?") && (i as { method?: string })?.method === "POST")!
    expect(JSON.parse(String((post[1] as { body?: string }).body))).toEqual({
      credential_id: "c_key", scope: "AGENT", crew_id: "", agent_id: "a1", slot: "OPENAI_API_KEY",
    })
  })

  it("swapping seats releases the old binding first, then claims the new one", async () => {
    serve([chatgpt, openaiKey], [{ id: "b_old", credential_id: "c_gpt", scope: "AGENT", agent_id: "a1", slot: "OPENAI_API_KEY" }])
    renderRow({ paysWith: { credential_id: "c_gpt", name: "ChatGPT Plus · jana", login: chatgpt.login! } })
    fireEvent.click(screen.getByRole("button", { name: /pays with/i }))
    const options = await screen.findAllByRole("option")
    fireEvent.click(options.find((o) => o.textContent?.includes("OpenAI API · workspace"))!)
    await waitFor(() => expect(h.toast.success).toHaveBeenCalled())
    const verbs = h.apiFetch.mock.calls
      .filter(([u]) => String(u).startsWith("/api/v1/credentials/bindings"))
      .map(([u, i]) => `${(i as { method?: string })?.method ?? "GET"} ${String(u).split("?")[0]}`)
    expect(verbs.indexOf("DELETE /api/v1/credentials/bindings/b_old")).toBeLessThan(verbs.indexOf("POST /api/v1/credentials/bindings"))
  })
})

describe("the warning", () => {
  const paysWithGpt = { credential_id: "c_gpt", name: "ChatGPT Plus · jana", login: chatgpt.login! }

  it("a seat at its limit, before the list is read: the limit, and the condition", async () => {
    serve([chatgpt, claude])
    renderRow({ paysWith: paysWithGpt })
    const status = screen.getByRole("status")
    expect(status).toHaveTextContent(/at its limit until \d{1,2}:\d{2}\. If no other OpenAI seat is in scope, the next run of codex-reviewer waits/)
    expect(h.apiFetch).not.toHaveBeenCalled()
  })

  it("a seat at its limit with no pool partner, once the list is read: the next run waits", async () => {
    serve([chatgpt, claude], [{ id: "b1", credential_id: "c_gpt", scope: "AGENT", agent_id: "a1", slot: "OPENAI_API_KEY" }])
    renderRow({ paysWith: paysWithGpt })
    fireEvent.click(screen.getByRole("button", { name: /pays with/i }))
    await waitFor(() =>
      expect(screen.getByRole("status")).toHaveTextContent(/at its limit until \d{1,2}:\d{2} and has no pool partner, so the next run of codex-reviewer waits/),
    )
    expect(screen.getByRole("status")).toHaveTextContent(/add a second openai seat/i)
  })

  it("no warning when a second seat of the provider exists", async () => {
    serve([chatgpt, openaiKey], [{ id: "b1", credential_id: "c_gpt", scope: "AGENT", agent_id: "a1", slot: "OPENAI_API_KEY" }])
    renderRow({ paysWith: paysWithGpt })
    fireEvent.click(screen.getByRole("button", { name: /pays with/i }))
    await screen.findAllByRole("option")
    expect(screen.queryByRole("status")).not.toBeInTheDocument()
  })

  it("a seat of the wrong provider is said before the run says 401", async () => {
    serve([chatgpt, claude])
    renderRow({ paysWith: { credential_id: "c_claude", name: "Claude Max · pavel", login: claude.login! } })
    expect(screen.getByRole("status")).toHaveTextContent(/openai models cannot be paid with a anthropic seat/i)
  })
})

describe("seat scoping helpers", () => {
  it.each([
    ["CODEX_CLI", "OPENAI", "", "OPENAI"],
    ["OPENCODE", "ANTHROPIC", "claude-sonnet-5-5", "ANTHROPIC"],
    ["OPENCODE", "OPENCODE", "anthropic/claude-sonnet-5-5", "ANTHROPIC"],
    ["OPENCODE", "OPENCODE", "openrouter/openai/gpt-6", null],
    ["OPENCODE", "AWS_BEDROCK", "amazon-bedrock/eu.anthropic.claude-opus-5-5", "AWS_BEDROCK"],
    ["OPENCODE", "OLLAMA", "ollama/qwen3", null],
  ])("%s + %s + %s → %s", (adapter, provider, model, want) => {
    expect(seatProvider(adapter, provider, model)).toBe(want)
  })

  it("releases only the agent's own Bedrock binding", async () => {
    h.apiFetch.mockImplementation(async (url: unknown, init?: { method?: string }) => {
      const u = String(url)
      if (u.startsWith("/api/v1/credentials/bindings?")) return ok({ bindings: [
        { id: "b1", credential_id: "c_bedrock", scope: "AGENT", agent_id: "a1", slot: "AWS_BEARER_TOKEN_BEDROCK" },
        { id: "b2", credential_id: "c_claude", scope: "AGENT", agent_id: "a1", slot: "CLAUDE_CODE_OAUTH_TOKEN" },
      ] })
      if (init?.method === "DELETE") return ok({})
      throw new Error(`unexpected ${u}`)
    })
    await releaseBedrockSeat("ws1", "a1")
    const deletes = h.apiFetch.mock.calls.filter(([, i]) => (i as { method?: string })?.method === "DELETE").map(([u]) => String(u).split("?")[0])
    expect(deletes).toEqual(["/api/v1/credentials/bindings/b1"])
  })
})

describe("applyPaysWith with a crew account rule", () => {
  it("replaces the Bedrock binding the crew rule materialized, not another login", async () => {
    const other = { ...bedrockSeat, id: "c_lab", name: "Bedrock lab" }
    h.apiFetch.mockImplementation(async (url: unknown, init?: { method?: string }) => {
      const u = String(url)
      if (u.startsWith("/api/v1/credentials?")) return ok([claude, bedrockSeat, other])
      if (u.startsWith("/api/v1/credentials/bindings?")) return ok({ bindings: [
        { id: "b_claude", credential_id: claude.id, scope: "AGENT", agent_id: "a1", slot: "CLAUDE_CODE_OAUTH_TOKEN" },
        { id: "b_rule", credential_id: bedrockSeat.id, scope: "AGENT", agent_id: "a1", slot: "AWS_BEARER_TOKEN_BEDROCK", inherited_from_binding_id: "crew_rule" },
      ] })
      if (init?.method === "DELETE") return ok({})
      if (u.includes("/bedrock-assign")) return ok({ assigned: 1 })
      throw new Error(`unexpected ${u}`)
    })
    await applyPaysWith("ws1", "a1", other, "")
    const writes = h.apiFetch.mock.calls.filter(([, i]) => (i as { method?: string })?.method).map(([u, i]) => `${(i as { method: string }).method} ${String(u).split("?")[0]}`)
    expect(writes).toEqual(["DELETE /api/v1/credentials/bindings/b_rule", "POST /api/v1/credentials/c_lab/bedrock-assign"])
  })
})
