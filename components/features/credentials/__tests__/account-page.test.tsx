// An AI provider account's "Pays for" tab (Harbor 2): agents by crew, each
// with a switch that is one AGENT binding under the account's slot; only the
// runners that can use the provider are offered.

import * as React from "react"
import { describe, it, expect, vi, beforeEach } from "vitest"
import { fireEvent, render, screen, waitFor, within } from "@testing-library/react"

import { PaysForPanel } from "../pays-for-panel"
import type { LoginCredential, ProviderLogin } from "@/lib/credentials/provider-logins"

const h = vi.hoisted(() => ({ apiFetch: vi.fn(), toast: { success: vi.fn(), error: vi.fn(), info: vi.fn() } }))
vi.mock("@/lib/api-fetch", () => ({ apiFetch: (...args: unknown[]) => h.apiFetch(...args) }))
vi.mock("sonner", () => ({ toast: h.toast }))

const SEAT: LoginCredential & { login: ProviderLogin } = {
  id: "seat1", name: "Claude Max · Pavel", provider: "ANTHROPIC", status: "ACTIVE",
  login: {
    mode: "subscription", provider: "ANTHROPIC", plan: null, plan_label: "Max", owner_user_id: "u1", owner_email: null,
    expires_at: null, refresh: { supported: false, status: "none", last_at: null, next_at: null, error: null }, quota: null,
    delivery: { kind: "env", target: "CLAUDE_CODE_OAUTH_TOKEN" }, pays_for: { agents: 1, crews: 0 },
  },
}
const AGENTS = [
  { id: "ag-scout", name: "Scout", cli_adapter: "CLAUDE_CODE", crew_id: "crew-r" },
  { id: "ag-writer", name: "Writer", cli_adapter: "OPENCODE", crew_id: "crew-r" },
  { id: "ag-codex", name: "Fixer", cli_adapter: "CODEX_CLI", crew_id: "crew-r" },
]
const CREWS = { "crew-r": { id: "crew-r", name: "Research", icon: null, color: null } }

function json(body: unknown, status = 200) {
  return { ok: status < 400, status, json: async () => body } as unknown as Response
}

let bindings: { id: string; credential_id: string; scope: string; crew_id: string | null; agent_id: string | null; slot: string }[]
beforeEach(() => {
  h.apiFetch.mockReset()
  h.toast.error.mockReset()
  bindings = [{ id: "b1", credential_id: "seat1", scope: "AGENT", crew_id: null, agent_id: "ag-scout", slot: "CLAUDE_CODE_OAUTH_TOKEN" }]
  h.apiFetch.mockImplementation(async (url: string, init?: RequestInit) => {
    if (url.startsWith("/api/v1/agents?")) return json(AGENTS)
    if (url.startsWith("/api/v1/credentials/bindings?") && !init?.method) return json({ bindings })
    if (init?.method === "POST") {
      const b = JSON.parse(String(init.body))
      bindings = [...bindings, { id: `b${bindings.length + 1}`, ...b, agent_id: b.agent_id || null, crew_id: b.crew_id || null }]
      return json({ id: "new" }, 201)
    }
    if (init?.method === "DELETE") return json({ success: true })
    return json({})
  })
})

function renderPanel(over: Partial<React.ComponentProps<typeof PaysForPanel>> = {}) {
  const onChanged = vi.fn()
  render(<PaysForPanel workspaceId="ws1" credential={SEAT} canBind crewsById={CREWS} onChanged={onChanged} {...over} />)
  return { onChanged }
}

describe("Pays for", () => {
  it("pays-for toggles create/delete AGENT bindings", async () => {
    const { onChanged } = renderPanel()
    const scout = await screen.findByRole("switch", { name: "Claude Max · Pavel pays for Scout" })
    expect(scout).toHaveAttribute("aria-checked", "true")
    const writer = screen.getByRole("switch", { name: "Claude Max · Pavel pays for Writer" })
    expect(writer).toHaveAttribute("aria-checked", "false")

    fireEvent.click(writer)
    await waitFor(() => expect(h.apiFetch).toHaveBeenCalledWith(expect.stringContaining("/api/v1/credentials/bindings?"), expect.objectContaining({ method: "POST" })))
    const post = h.apiFetch.mock.calls.find(([, i]) => i?.method === "POST")!
    expect(JSON.parse(post[1].body)).toEqual({ credential_id: "seat1", scope: "AGENT", crew_id: "", agent_id: "ag-writer", slot: "CLAUDE_CODE_OAUTH_TOKEN" })
    await waitFor(() => expect(screen.getByRole("switch", { name: "Claude Max · Pavel pays for Writer" })).toHaveAttribute("aria-checked", "true"))

    fireEvent.click(scout)
    await waitFor(() => expect(h.apiFetch).toHaveBeenCalledWith(expect.stringContaining("/api/v1/credentials/bindings/b1?"), expect.objectContaining({ method: "DELETE" })))
    await waitFor(() => expect(screen.getByRole("switch", { name: "Claude Max · Pavel pays for Scout" })).toHaveAttribute("aria-checked", "false"))
    expect(onChanged).toHaveBeenCalledTimes(2)
  })

  it("offers only runners that can use the provider, by crew", async () => {
    renderPanel()
    const crew = await screen.findByRole("region", { name: "Research" })
    expect(within(crew).queryByText("Fixer")).not.toBeInTheDocument()
    expect(screen.getByText(/1 other agent is not/)).toBeInTheDocument()
  })

  it("pays for the whole crew with one CREW binding", async () => {
    renderPanel()
    fireEvent.click(await screen.findByRole("button", { name: "Pay for the whole crew" }))
    await waitFor(() => expect(h.apiFetch).toHaveBeenCalledWith(expect.anything(), expect.objectContaining({ method: "POST" })))
    const post = h.apiFetch.mock.calls.find(([, i]) => i?.method === "POST")!
    expect(JSON.parse(post[1].body)).toMatchObject({ scope: "CREW", crew_id: "crew-r", agent_id: "" })
  })

  it("a refused switch says why and changes nothing", async () => {
    h.apiFetch.mockImplementation(async (url: string, init?: RequestInit) => {
      if (url.startsWith("/api/v1/agents?")) return json(AGENTS)
      if (url.startsWith("/api/v1/credentials/bindings?") && !init?.method) return json({ bindings })
      if (init?.method === "POST") return json({ error: "conflict" }, 409)
      return json({})
    })
    renderPanel()
    fireEvent.click(await screen.findByRole("switch", { name: "Claude Max · Pavel pays for Writer" }))
    await waitFor(() => expect(h.toast.error).toHaveBeenCalled())
    expect(screen.getByRole("switch", { name: "Claude Max · Pavel pays for Writer" })).toHaveAttribute("aria-checked", "false")
  })

  it("a reader who cannot bind sees the switches, locked", async () => {
    renderPanel({ canBind: false })
    expect(await screen.findByRole("switch", { name: "Claude Max · Pavel pays for Scout" })).toBeDisabled()
    expect(screen.queryByRole("button", { name: "Pay for the whole crew" })).not.toBeInTheDocument()
    expect(screen.getByRole("note")).toHaveTextContent(/Only workspace owners and admins/)
  })
})
