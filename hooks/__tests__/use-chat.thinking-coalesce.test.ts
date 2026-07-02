import { describe, it, expect, vi, beforeEach } from "vitest"

// Mock useWebSocket to avoid real WebSocket connections
const mockSend = vi.fn()

interface UseWebSocketArgs {
  onMessage?: (msg: unknown) => void
}

vi.mock("@/hooks/use-websocket", () => ({
  useWebSocket: vi.fn(({ onMessage }: UseWebSocketArgs) => {
    if (onMessage) {
      ;(globalThis as Record<string, unknown>).__testOnMessage = onMessage
    }
    return {
      status: "connected",
      send: mockSend,
      disconnect: vi.fn(),
      reconnect: vi.fn(),
    }
  }),
}))

vi.stubGlobal("crypto", {
  randomUUID: () => "test-uuid-" + Math.random().toString(36).slice(2, 8),
})

import { renderHook, act } from "@testing-library/react"
import { useChat } from "@/hooks/use-chat"

// One reasoning pass must render as ONE thinking block even when transient
// progress events (status lines, non-init system logs) land between two
// thinking deltas. The backend PartAccumulator ignores status/system when
// persisting, so a reloaded turn shows a single merged block — the live
// stream must match, otherwise the user sees "Thought for 1 seconds" stubs
// fragmenting a single sentence while streaming and one block after reload.
describe("useChat thinking coalescence across transient parts", () => {
  beforeEach(() => {
    vi.clearAllMocks()
  })

  function getOnMessage(): (msg: unknown) => void {
    return (globalThis as Record<string, unknown>).__testOnMessage as (msg: unknown) => void
  }

  function emit(payload: Record<string, unknown>) {
    act(() => {
      getOnMessage()({ type: "chat_event", channel: "session:s1", payload })
    })
  }

  it("does not split a thinking block on an interleaved status event", () => {
    const { result } = renderHook(() =>
      useChat({ wsUrl: "ws://x/ws", getToken: async () => "t", sessionId: "s1" }),
    )

    emit({ type: "thinking", content: "The user just" })
    emit({ type: "status", content: "thinking_tokens: 42" })
    emit({ type: "thinking", content: " greeted me with Ahoj." })

    expect(result.current.turns).toHaveLength(1)
    const parts = result.current.turns[0].parts
    const thinkingParts = parts.filter((p) => p.type === "thinking")
    expect(thinkingParts).toHaveLength(1)
    expect(thinkingParts[0].content).toBe("The user just greeted me with Ahoj.")
    // The status line stays a single quiet indicator, not a block splitter.
    expect(parts.filter((p) => p.type === "status").length).toBeLessThanOrEqual(1)
  })

  it("does not split a thinking block on an interleaved non-init system event", () => {
    const { result } = renderHook(() =>
      useChat({ wsUrl: "ws://x/ws", getToken: async () => "t", sessionId: "s1" }),
    )

    emit({ type: "thinking", content: "should respond warmly" })
    emit({ type: "system", content: "sidecar: scrub pass", metadata: { subtype: "log" } })
    emit({ type: "thinking", content: " and professionally." })

    const thinkingParts = result.current.turns[0].parts.filter((p) => p.type === "thinking")
    expect(thinkingParts).toHaveLength(1)
    expect(thinkingParts[0].content).toBe("should respond warmly and professionally.")
  })

  it("still splits genuinely separate reasoning passes (think → text → think)", () => {
    const { result } = renderHook(() =>
      useChat({ wsUrl: "ws://x/ws", getToken: async () => "t", sessionId: "s1" }),
    )

    emit({ type: "thinking", content: "first pass" })
    emit({ type: "text", content: "partial answer" })
    emit({ type: "thinking", content: "second pass" })

    const thinkingParts = result.current.turns[0].parts.filter((p) => p.type === "thinking")
    expect(thinkingParts).toHaveLength(2)
    expect(thinkingParts[0].content).toBe("first pass")
    expect(thinkingParts[1].content).toBe("second pass")
  })

  it("prunes the transient status line when the run errors", () => {
    const { result } = renderHook(() =>
      useChat({ wsUrl: "ws://x/ws", getToken: async () => "t", sessionId: "s1" }),
    )

    emit({ type: "thinking", content: "working on it" })
    emit({ type: "status", content: "thinking_tokens: 42" })
    emit({ type: "error", content: "boom" })

    const parts = result.current.turns[0].parts
    expect(parts.some((p) => p.type === "status")).toBe(false)
    expect(parts.some((p) => p.type === "error")).toBe(true)
  })

  it("prunes the transient status line on local stop", () => {
    const { result } = renderHook(() =>
      useChat({ wsUrl: "ws://x/ws", getToken: async () => "t", sessionId: "s1" }),
    )

    emit({ type: "thinking", content: "working on it" })
    emit({ type: "status", content: "thinking_tokens: 42" })
    act(() => {
      result.current.stopGeneration()
    })

    const parts = result.current.turns[0].parts
    expect(parts.some((p) => p.type === "status")).toBe(false)
    // The partial thinking is kept (never lose content), just finalized.
    expect(parts.some((p) => p.type === "thinking" && !p.isStreaming)).toBe(true)
  })

  it("still splits reasoning passes separated by a tool call", () => {
    const { result } = renderHook(() =>
      useChat({ wsUrl: "ws://x/ws", getToken: async () => "t", sessionId: "s1" }),
    )

    emit({ type: "thinking", content: "let me check" })
    emit({ type: "tool_call", content: "Read", metadata: { tool_id: "t1" } })
    emit({ type: "thinking", content: "now I know" })

    const thinkingParts = result.current.turns[0].parts.filter((p) => p.type === "thinking")
    expect(thinkingParts).toHaveLength(2)
  })
})
