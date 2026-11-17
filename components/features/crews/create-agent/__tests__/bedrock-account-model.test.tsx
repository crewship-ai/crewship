// New agent › Model on Amazon Bedrock: the account comes first and its
// allowlist is the model list; the runner is OpenCode and not a choice.

import { useState } from "react"
import { fireEvent, render, screen, waitFor, within } from "@testing-library/react"
import { beforeEach, describe, expect, it, vi } from "vitest"

import { AgentModelSettings } from "../agent-model-settings"
import { initialAgentDraft, type AgentDraft } from "../types"
import type { LoginCredential } from "@/lib/credentials/provider-logins"

const h = vi.hoisted(() => ({ apiFetch: vi.fn() }))
vi.mock("@/lib/api-fetch", () => ({ apiFetch: (...args: unknown[]) => h.apiFetch(...args) }))
vi.mock("../../canvas/config-model", () => ({ ConfigModel: ({ value }: { value: string }) => <div data-testid="model">{value}</div> }))

function account(id: string, name: string, models: string[]): LoginCredential {
  return {
    id, name, provider: "AWS_BEDROCK", status: "ACTIVE",
    login: {
      mode: "api_key", provider: "AWS_BEDROCK", plan: null, plan_label: null, owner_user_id: null, owner_email: null, expires_at: null,
      refresh: { supported: false, status: "none", last_at: null, next_at: null, error: null }, quota: null,
      delivery: { kind: "sidecar", target: "AWS_BEARER_TOKEN_BEDROCK" }, pays_for: { agents: 0, crews: 0 },
      bedrock: { region: "eu-central-1", models, auth_method: "bedrock_api_key", eu_only: true },
    },
  } as LoginCredential
}

const eu = account("c_eu", "Bedrock EU", ["eu.anthropic.claude-sonnet-5-5", "eu.anthropic.claude-opus-5-5"])
const lab = account("c_lab", "Bedrock lab", ["eu.amazon.nova-pro-v1:0"])
const claude = { id: "c_claude", name: "Claude Max", provider: "ANTHROPIC", status: "ACTIVE", login: { ...eu.login!, provider: "ANTHROPIC", bedrock: undefined } } as LoginCredential

function serve(rows: LoginCredential[]) {
  h.apiFetch.mockImplementation(async (url: unknown) => {
    if (String(url).startsWith("/api/v1/credentials?")) return { ok: true, status: 200, json: async () => rows } as unknown as Response
    throw new Error(`unexpected ${String(url)}`)
  })
}

function Harness({ initial, boundSeatId = null }: { initial?: Partial<AgentDraft>; boundSeatId?: string | null }) {
  const [draft, setDraft] = useState<AgentDraft>({ ...initialAgentDraft(null), ...initial })
  const [seat, setSeat] = useState<LoginCredential | null>(null)
  return <>
    <AgentModelSettings draft={draft} setDraft={setDraft} workspaceId="ws1" seat={seat} boundSeatId={boundSeatId} onSeat={setSeat} />
    <output data-testid="draft">{JSON.stringify({ ...draft, seat: seat?.id ?? null })}</output>
  </>
}
const state = () => JSON.parse(screen.getByTestId("draft").textContent || "{}")

beforeEach(() => h.apiFetch.mockReset())

describe("Bedrock account and model", () => {
  it("lists only Bedrock accounts, then that account's models, with OpenCode fixed", async () => {
    serve([eu, lab, claude])
    render(<Harness />)
    fireEvent.click(within(screen.getByRole("radiogroup", { name: "Model provider" })).getByRole("radio", { name: "Amazon Bedrock" }))
    const accounts = await screen.findByRole("radiogroup", { name: "Bedrock account" })
    expect(within(accounts).getAllByRole("radio").map((r) => r.textContent)).toEqual([expect.stringContaining("Bedrock EU"), expect.stringContaining("Bedrock lab")])
    expect(screen.queryByRole("radiogroup", { name: "Agent runner" })).toBeNull()
    expect(screen.getByText(/set by Amazon Bedrock/)).toBeInTheDocument()

    fireEvent.click(within(accounts).getByRole("radio", { name: /Bedrock EU/ }))
    expect(state()).toMatchObject({ llmProvider: "AWS_BEDROCK", cliAdapter: "OPENCODE", llmModel: "amazon-bedrock/eu.anthropic.claude-sonnet-5-5", seat: "c_eu" })

    const models = screen.getByRole("radiogroup", { name: "Bedrock model" })
    expect(within(models).getAllByRole("radio")).toHaveLength(2)
    fireEvent.click(within(models).getByRole("radio", { name: /Claude Opus 5.5 · EU/ }))
    expect(state()).toMatchObject({ llmModel: "amazon-bedrock/eu.anthropic.claude-opus-5-5", seat: "c_eu" })

    fireEvent.click(within(accounts).getByRole("radio", { name: /Bedrock lab/ }))
    expect(state()).toMatchObject({ llmModel: "amazon-bedrock/eu.amazon.nova-pro-v1:0", seat: "c_lab" })
  })

  it("takes the only account by itself", async () => {
    serve([eu])
    render(<Harness initial={{ llmProvider: "AWS_BEDROCK", cliAdapter: "OPENCODE", llmModel: "" }} />)
    await waitFor(() => expect(state()).toMatchObject({ seat: "c_eu", llmModel: "amazon-bedrock/eu.anthropic.claude-sonnet-5-5" }))
  })

  it("starts on the account the agent already pays with and does not re-pick it", async () => {
    serve([eu])
    render(<Harness boundSeatId="c_eu" initial={{ llmProvider: "AWS_BEDROCK", cliAdapter: "OPENCODE", llmModel: "amazon-bedrock/eu.anthropic.claude-opus-5-5" }} />)
    const models = await screen.findByRole("radiogroup", { name: "Bedrock model" })
    expect(within(models).getByRole("radio", { name: /Claude Opus 5.5/ })).toHaveAttribute("aria-checked", "true")
    fireEvent.click(within(models).getByRole("radio", { name: /Claude Sonnet 5.5/ }))
    expect(state()).toMatchObject({ seat: null, llmModel: "amazon-bedrock/eu.anthropic.claude-sonnet-5-5" })
  })

  it("points to Vault when no Bedrock account is connected", async () => {
    serve([claude])
    render(<Harness initial={{ llmProvider: "AWS_BEDROCK", cliAdapter: "OPENCODE", llmModel: "" }} />)
    expect(await screen.findByRole("link", { name: /Connect Amazon Bedrock in Vault/ })).toHaveAttribute("href", "/credentials?tab=providers")
  })

  it("drops the Bedrock account when the provider changes", async () => {
    serve([eu])
    render(<Harness initial={{ llmProvider: "AWS_BEDROCK", cliAdapter: "OPENCODE", llmModel: "" }} />)
    await waitFor(() => expect(state().seat).toBe("c_eu"))
    fireEvent.click(within(screen.getByRole("radiogroup", { name: "Model provider" })).getByRole("radio", { name: "Anthropic" }))
    expect(state()).toMatchObject({ llmProvider: "ANTHROPIC", seat: null })
  })
})
