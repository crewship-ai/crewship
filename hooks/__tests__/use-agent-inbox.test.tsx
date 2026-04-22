import { describe, it, expect, vi, beforeEach, afterEach } from "vitest"
import { renderHook, waitFor, act } from "@testing-library/react"

vi.mock("@/hooks/use-workspace", () => ({
  useWorkspace: () => ({ workspaceId: "ws-test", loading: false }),
}))

import { useAgentInbox } from "@/hooks/use-agent-inbox"

describe("useAgentInbox", () => {
  beforeEach(() => {
    vi.restoreAllMocks()
  })

  afterEach(() => {
    vi.restoreAllMocks()
  })

  it("returns null inbox when agentId is missing", () => {
    const { result } = renderHook(() => useAgentInbox(null))
    expect(result.current.inbox).toBeNull()
    expect(result.current.loading).toBe(false)
  })

  it("fetches inbox data for an agent", async () => {
    const payload = {
      approvals_pending: 2,
      assignments_open: 1,
      escalations_open: 0,
      peer_messages: [],
      cost_usd_this_month: 1.23,
      llm_calls_this_month: 10,
      tokens_used_this_month: 5000,
    }
    const fetchSpy = vi.spyOn(globalThis, "fetch").mockResolvedValue(
      new Response(JSON.stringify(payload), { status: 200 }),
    )
    const { result } = renderHook(() => useAgentInbox("agent-1"))
    await waitFor(() => expect(result.current.loading).toBe(false))
    expect(fetchSpy).toHaveBeenCalledWith(
      "/api/v1/agents/agent-1/inbox?workspace_id=ws-test",
      expect.any(Object),
    )
    expect(result.current.inbox).toEqual(payload)
    expect(result.current.error).toBeNull()
  })

  it("treats 404 as an empty inbox (agent temporarily gone)", async () => {
    vi.spyOn(globalThis, "fetch").mockResolvedValue(
      new Response(JSON.stringify({ error: "not found" }), { status: 404 }),
    )
    const { result } = renderHook(() => useAgentInbox("agent-gone"))
    await waitFor(() => expect(result.current.loading).toBe(false))
    expect(result.current.inbox?.approvals_pending).toBe(0)
    expect(result.current.inbox?.peer_messages).toEqual([])
    expect(result.current.error).toBeNull()
  })

  it("surfaces non-404 errors", async () => {
    vi.spyOn(globalThis, "fetch").mockResolvedValue(
      new Response("server broken", { status: 500 }),
    )
    const { result } = renderHook(() => useAgentInbox("agent-x"))
    await waitFor(() => expect(result.current.loading).toBe(false))
    expect(result.current.error).toContain("500")
    expect(result.current.inbox).toBeNull()
  })

  it("refetches on refresh()", async () => {
    const fetchSpy = vi.spyOn(globalThis, "fetch").mockResolvedValue(
      new Response(JSON.stringify({
        approvals_pending: 0,
        assignments_open: 0,
        escalations_open: 0,
        peer_messages: [],
        cost_usd_this_month: 0,
        llm_calls_this_month: 0,
        tokens_used_this_month: 0,
      }), { status: 200 }),
    )
    const { result } = renderHook(() => useAgentInbox("agent-r"))
    await waitFor(() => expect(result.current.loading).toBe(false))
    const initialCalls = fetchSpy.mock.calls.length
    act(() => result.current.refresh())
    await waitFor(() => expect(fetchSpy.mock.calls.length).toBeGreaterThan(initialCalls))
  })
})
