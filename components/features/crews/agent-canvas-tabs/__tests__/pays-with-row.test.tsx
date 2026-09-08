// "Pays with" (PRD provider-logins §6.3, wireframe AgentPaysWith). The picker
// lists every seat, greys the wrong provider's, writes an AGENT binding for
// the chosen one, and warns when the choice is a seat at its limit with no
// pool partner.

import { describe, it, expect, vi, beforeEach } from "vitest"
import { render, screen, fireEvent, waitFor, within } from "@testing-library/react"
import { PaysWithRow } from "../pays-with-row"
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
    expect(wrong).toHaveTextContent("Codex CLI cannot pay with this")
    const right = options.find((o) => o.textContent?.includes("ChatGPT Plus · jana"))!
    expect(right).not.toBeDisabled()
    expect(right).toHaveTextContent(/subscription · ChatGPT Plus · expires in 2 d \(auto\)/)
    expect(within(right).getByTestId("login-status-at_limit")).toBeInTheDocument()
  })

  it("OpenCode takes any provider — nothing is greyed", async () => {
    serve([chatgpt, claude])
    renderRow({ cliAdapter: "OPENCODE" })
    fireEvent.click(await screen.findByRole("button", { name: /pays with/i }))
    const options = await screen.findAllByRole("option")
    for (const o of options) expect(o).not.toBeDisabled()
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
    if (!restore) expect(h.toast.error).toHaveBeenCalledWith(expect.stringContaining("could not be restored"))
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
    expect(screen.getByRole("status")).toHaveTextContent(/codex cli cannot pay with a anthropic seat/i)
  })
})
