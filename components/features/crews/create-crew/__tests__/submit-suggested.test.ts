import { describe, it, expect, vi, beforeEach, afterEach } from "vitest"

const toasts = vi.hoisted(() => ({ warning: vi.fn(), error: vi.fn(), success: vi.fn(), info: vi.fn() }))
vi.mock("@/lib/toast", () => ({ toast: toasts }))
vi.mock("@/lib/agent-avatar-persist", () => ({ backfillAgentAvatars: vi.fn(async () => undefined) }))

import { submitCrew } from "../submit"
import { INITIAL_STATE, modeForStart, type WizardState } from "../types"

const calls: Array<{ url: string; method: string; body: Record<string, unknown> | undefined }> = []
let refuseAgent: string | null = null

beforeEach(() => {
  calls.length = 0
  refuseAgent = null
  toasts.warning.mockClear()
  vi.stubGlobal("fetch", vi.fn(async (url: string | URL, init?: RequestInit) => {
    const u = String(url)
    const body = init?.body ? JSON.parse(String(init.body)) : undefined
    calls.push({ url: u, method: init?.method ?? "GET", body })
    if (u.includes("/api/v1/crews")) return { ok: true, status: 201, json: async () => ({ id: "c1", slug: "research", name: "Research" }) } as Response
    if (u.includes("/api/v1/agents") && body?.slug === refuseAgent) return { ok: false, status: 409, json: async () => ({ error: "slug taken" }), text: async () => "" } as Response
    return { ok: true, status: 201, json: async () => ({ id: `a-${body?.slug}`, slug: body?.slug }) } as Response
  }))
})
afterEach(() => vi.unstubAllGlobals())

const state = (over: Partial<WizardState> = {}): WizardState => ({
  ...INITIAL_STATE,
  mode: "suggest",
  provider: "ANTHROPIC",
  name: "Research",
  slug: "research",
  suggested: [
    { name: "Scout", slug: "scout", role_title: "Lead researcher", agent_role: "LEAD", system_prompt: "Lead it." },
    { name: "Writer", slug: "writer", role_title: "Writer", agent_role: "LEAD", system_prompt: "Write it." },
  ],
  ...over,
})

describe("New crew — Start with", () => {
  it("each way to start picks its lineup mode", () => {
    expect(modeForStart("suggest")).toBe("suggest")
    expect(modeForStart("template")).toBe("browse")
    expect(modeForStart("yaml")).toBe("empty")
    expect(modeForStart("empty")).toBe("empty")
  })
})

describe("submitCrew — suggested team", () => {
  it("creates the crew, then each suggested agent in it with the provider's runner; one lead only", async () => {
    const r = await submitCrew("ws", state())
    expect(r).toMatchObject({ id: "c1", slug: "research" })
    const agents = calls.filter((c) => c.url.startsWith("/api/v1/agents") && c.method === "POST")
    expect(agents.map((c) => [c.body?.slug, c.body?.agent_role, c.body?.crew_id, c.body?.llm_provider, c.body?.cli_adapter])).toEqual([
      ["scout", "LEAD", "c1", "ANTHROPIC", "CLAUDE_CODE"],
      ["writer", "AGENT", "c1", "ANTHROPIC", "CLAUDE_CODE"],
    ])
    expect(r.partial).toBeFalsy()
  })

  it("an agent the server refuses leaves the crew and says how many were made", async () => {
    refuseAgent = "writer"
    const r = await submitCrew("ws", state())
    expect(r.partial).toBe(true)
    expect(toasts.warning).toHaveBeenCalledWith("Research created · 1 of 2 agents", expect.anything())
  })

  it("refuses to start without a provider", async () => {
    await expect(submitCrew("ws", state({ provider: null }))).rejects.toThrow(/provider/)
    expect(calls).toHaveLength(0)
  })
})
