import { describe, it, expect, vi, beforeEach, afterEach } from "vitest"
import React from "react"

// Mock useWorkspace so the provider effect sees a known workspace id.
const workspaceState = { workspaceId: "ws-1" as string | null }
vi.mock("@/hooks/use-workspace", () => ({
  useWorkspace: () => ({ workspaceId: workspaceState.workspaceId }),
}))

// Mock useRealtimeEvent: store the latest callback per event type.
const realtimeCallbacks: Record<string, (event: unknown) => void> = {}
vi.mock("@/hooks/use-realtime", () => ({
  useRealtimeEvent: vi.fn(
    (eventType: string, cb: (event: unknown) => void) => {
      realtimeCallbacks[eventType] = cb
    },
  ),
}))

import { renderHook, act, waitFor } from "@testing-library/react"
import {
  AgentDetailProvider,
  useAgentDetail,
  type AgentDetail,
} from "@/hooks/use-agent-detail"

function sampleAgent(overrides: Partial<AgentDetail> = {}): AgentDetail {
  return {
    id: "agent-1",
    workspace_id: "ws-1",
    crew_id: "crew-eng",
    name: "Lucie",
    slug: "lucie",
    description: null,
    role_title: "LEAD",
    agent_role: "LEAD",
    lead_mode: "active",
    status: "idle",
    cli_adapter: "claude_code",
    llm_provider: "anthropic",
    llm_model: "claude-opus-4-7",
    system_prompt: null,
    avatar_seed: null,
    avatar_style: null,
    timeout_seconds: 600,
    tool_profile: "full",
    memory_enabled: true,
    created_at: "",
    updated_at: "",
    crew: null,
    _count: { skills: 0, credentials: 0, chats: 0 },
    ...overrides,
  }
}

function wrap(agentId: string) {
  return ({ children }: { children: React.ReactNode }) => (
    <AgentDetailProvider agentId={agentId}>{children}</AgentDetailProvider>
  )
}

describe("useAgentDetail", () => {
  let mockFetch: ReturnType<typeof vi.fn>

  beforeEach(() => {
    workspaceState.workspaceId = "ws-1"
    mockFetch = vi.fn()
    vi.stubGlobal("fetch", mockFetch)
    for (const k of Object.keys(realtimeCallbacks)) delete realtimeCallbacks[k]
  })

  afterEach(() => {
    vi.unstubAllGlobals()
  })

  it("returns safe defaults when used outside the provider", () => {
    const { result } = renderHook(() => useAgentDetail())
    expect(result.current.agent).toBeNull()
    expect(result.current.loading).toBe(false)
    expect(result.current.error).toBeNull()
    // refresh/setAgent are no-ops; calling them must not throw.
    result.current.refresh()
    result.current.setAgent(null)
  })

  it("fetches agent on mount and exposes the record", async () => {
    const agent = sampleAgent()
    mockFetch.mockResolvedValueOnce({
      ok: true,
      json: async () => agent,
    })

    const { result } = renderHook(() => useAgentDetail(), { wrapper: wrap("agent-1") })

    await waitFor(() => expect(result.current.loading).toBe(false))
    expect(mockFetch).toHaveBeenCalledWith(
      "/api/v1/agents/agent-1?workspace_id=ws-1",
      expect.objectContaining({ signal: expect.any(AbortSignal) }),
    )
    expect(result.current.agent?.slug).toBe("lucie")
    expect(result.current.error).toBeNull()
  })

  it("surfaces server error from JSON body", async () => {
    mockFetch.mockResolvedValueOnce({
      ok: false,
      json: async () => ({ error: "Agent not found" }),
    })

    const { result } = renderHook(() => useAgentDetail(), { wrapper: wrap("agent-missing") })

    await waitFor(() => expect(result.current.loading).toBe(false))
    expect(result.current.error).toBe("Agent not found")
    expect(result.current.agent).toBeNull()
  })

  it("falls back to a generic message when error body cannot be parsed", async () => {
    mockFetch.mockResolvedValueOnce({
      ok: false,
      json: async () => {
        throw new Error("not json")
      },
    })

    const { result } = renderHook(() => useAgentDetail(), { wrapper: wrap("agent-bad") })

    await waitFor(() => expect(result.current.loading).toBe(false))
    expect(result.current.error).toBe("Failed to load agent")
  })

  it("maps a network throw to a 'Network error' message", async () => {
    mockFetch.mockRejectedValueOnce(new Error("connection refused"))

    const { result } = renderHook(() => useAgentDetail(), { wrapper: wrap("agent-1") })

    await waitFor(() => expect(result.current.loading).toBe(false))
    expect(result.current.error).toMatch(/Network error/i)
  })

  it("does not fetch while workspaceId is null", async () => {
    workspaceState.workspaceId = null

    renderHook(() => useAgentDetail(), { wrapper: wrap("agent-1") })
    await new Promise((r) => setTimeout(r, 0))

    expect(mockFetch).not.toHaveBeenCalled()
  })

  it("refresh() triggers a second fetch", async () => {
    const first = sampleAgent({ status: "idle" })
    const second = sampleAgent({ status: "running" })
    mockFetch
      .mockResolvedValueOnce({ ok: true, json: async () => first })
      .mockResolvedValueOnce({ ok: true, json: async () => second })

    const { result } = renderHook(() => useAgentDetail(), { wrapper: wrap("agent-1") })

    await waitFor(() => expect(result.current.agent?.status).toBe("idle"))
    act(() => result.current.refresh())
    await waitFor(() => expect(result.current.agent?.status).toBe("running"))
    expect(mockFetch).toHaveBeenCalledTimes(2)
  })

  it("auto-refreshes on realtime events that match the agentId", async () => {
    const first = sampleAgent({ status: "idle" })
    const second = sampleAgent({ status: "running" })
    mockFetch
      .mockResolvedValueOnce({ ok: true, json: async () => first })
      .mockResolvedValueOnce({ ok: true, json: async () => second })

    const { result } = renderHook(() => useAgentDetail(), { wrapper: wrap("agent-1") })
    await waitFor(() => expect(result.current.agent?.status).toBe("idle"))

    act(() => {
      realtimeCallbacks["agent.status"]?.({ payload: { agent_id: "agent-1" } })
    })
    await waitFor(() => expect(result.current.agent?.status).toBe("running"))
    expect(mockFetch).toHaveBeenCalledTimes(2)
  })

  it("ignores realtime events for a different agent_id", async () => {
    mockFetch.mockResolvedValueOnce({ ok: true, json: async () => sampleAgent() })

    renderHook(() => useAgentDetail(), { wrapper: wrap("agent-1") })
    await waitFor(() => expect(mockFetch).toHaveBeenCalledTimes(1))

    act(() => {
      realtimeCallbacks["run.completed"]?.({ payload: { agent_id: "some-other" } })
      realtimeCallbacks["run.failed"]?.({ payload: { agent_id: "yet-another" } })
      realtimeCallbacks["agent.status"]?.({ payload: { agent_id: "wrong" } })
    })
    await new Promise((r) => setTimeout(r, 0))

    expect(mockFetch).toHaveBeenCalledTimes(1)
  })

  it("subscribes to agent.status, run.completed, and run.failed", () => {
    mockFetch.mockResolvedValueOnce({ ok: true, json: async () => sampleAgent() })
    renderHook(() => useAgentDetail(), { wrapper: wrap("agent-1") })

    for (const ev of ["agent.status", "run.completed", "run.failed"]) {
      expect(realtimeCallbacks[ev]).toBeTypeOf("function")
    }
  })
})
