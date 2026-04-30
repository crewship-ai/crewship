import { describe, it, expect, vi, beforeEach } from "vitest"

// Mock useWebSocket to avoid real WebSocket connections
const mockSend = vi.fn()
const mockStatus = { current: "connected" as string }

interface UseWebSocketArgs {
  onMessage?: (msg: unknown) => void
}

vi.mock("@/hooks/use-websocket", () => ({
  useWebSocket: vi.fn(({ onMessage }: UseWebSocketArgs) => {
    // Expose onMessage for testing
    if (onMessage) {
      ;(globalThis as Record<string, unknown>).__testOnMessage = onMessage
    }
    return {
      status: mockStatus.current,
      send: mockSend,
      disconnect: vi.fn(),
      reconnect: vi.fn(),
    }
  }),
}))

// Must mock crypto.randomUUID for test environment
vi.stubGlobal("crypto", {
  randomUUID: () => "test-uuid-" + Math.random().toString(36).slice(2, 8),
})

import { renderHook, act } from "@testing-library/react"
import { useChat } from "@/hooks/use-chat"

describe("useChat", () => {
  beforeEach(() => {
    vi.clearAllMocks()
    mockStatus.current = "connected"
  })

  function getOnMessage(): (msg: unknown) => void {
    return (globalThis as Record<string, unknown>).__testOnMessage as (msg: unknown) => void
  }

  it("starts with empty turns and not streaming", () => {
    const { result } = renderHook(() =>
      useChat({ wsUrl: "ws://localhost:8080/ws", token: "test", sessionId: "s1" }),
    )
    expect(result.current.turns).toHaveLength(0)
    expect(result.current.messages).toHaveLength(0)
    expect(result.current.isStreaming).toBe(false)
  })

  it("sendMessage adds user turn and calls ws send", () => {
    const { result } = renderHook(() =>
      useChat({ wsUrl: "ws://localhost:8080/ws", token: "test", sessionId: "s1" }),
    )

    act(() => {
      result.current.sendMessage("hello")
    })

    expect(result.current.turns).toHaveLength(1)
    expect(result.current.turns[0].role).toBe("user")
    expect(result.current.turns[0].parts[0].content).toBe("hello")
    expect(result.current.isStreaming).toBe(true)
    expect(mockSend).toHaveBeenCalledWith(
      expect.objectContaining({ type: "send_message" }),
    )
  })

  it("ignores empty messages", () => {
    const { result } = renderHook(() =>
      useChat({ wsUrl: "ws://localhost:8080/ws", token: "test", sessionId: "s1" }),
    )

    act(() => {
      result.current.sendMessage("")
    })

    expect(result.current.turns).toHaveLength(0)
    expect(mockSend).not.toHaveBeenCalled()
  })

  it("groups text events into single assistant turn", () => {
    const { result } = renderHook(() =>
      useChat({ wsUrl: "ws://localhost:8080/ws", token: "test", sessionId: "s1" }),
    )
    const onMessage = getOnMessage()

    act(() => {
      onMessage({
        type: "chat_event",
        channel: "session:s1",
        payload: { type: "text", content: "Hello " },
      })
    })

    act(() => {
      onMessage({
        type: "chat_event",
        channel: "session:s1",
        payload: { type: "text", content: "world" },
      })
    })

    // Should be ONE assistant turn with ONE text part (accumulated)
    expect(result.current.turns).toHaveLength(1)
    expect(result.current.turns[0].role).toBe("assistant")
    expect(result.current.turns[0].parts).toHaveLength(1)
    expect(result.current.turns[0].parts[0].type).toBe("text")
    expect(result.current.turns[0].parts[0].content).toBe("Hello world")
    expect(result.current.turns[0].isStreaming).toBe(true)
  })

  it("groups thinking + text into one assistant turn", () => {
    const { result } = renderHook(() =>
      useChat({ wsUrl: "ws://localhost:8080/ws", token: "test", sessionId: "s1" }),
    )
    const onMessage = getOnMessage()

    // First: thinking event
    act(() => {
      onMessage({
        type: "chat_event",
        channel: "session:s1",
        payload: { type: "thinking", content: "Let me analyze..." },
      })
    })

    // Then: text event
    act(() => {
      onMessage({
        type: "chat_event",
        channel: "session:s1",
        payload: { type: "text", content: "Here is the answer" },
      })
    })

    expect(result.current.turns).toHaveLength(1)
    expect(result.current.turns[0].role).toBe("assistant")
    expect(result.current.turns[0].parts).toHaveLength(2)
    expect(result.current.turns[0].parts[0].type).toBe("thinking")
    expect(result.current.turns[0].parts[0].content).toBe("Let me analyze...")
    expect(result.current.turns[0].parts[1].type).toBe("text")
    expect(result.current.turns[0].parts[1].content).toBe("Here is the answer")
  })

  it("separates multiple complete thinking blocks into distinct parts", () => {
    const { result } = renderHook(() =>
      useChat({ wsUrl: "ws://localhost:8080/ws", token: "test", sessionId: "s1" }),
    )
    const onMessage = getOnMessage()

    // First complete thinking block (no streaming metadata = complete)
    act(() => {
      onMessage({
        type: "chat_event",
        channel: "session:s1",
        payload: { type: "thinking", content: "First thinking block" },
      })
    })

    // Second complete thinking block
    act(() => {
      onMessage({
        type: "chat_event",
        channel: "session:s1",
        payload: { type: "thinking", content: "Second thinking block" },
      })
    })

    // Should have ONE turn with TWO separate thinking parts
    expect(result.current.turns).toHaveLength(1)
    expect(result.current.turns[0].parts).toHaveLength(2)
    expect(result.current.turns[0].parts[0].type).toBe("thinking")
    expect(result.current.turns[0].parts[0].content).toBe("First thinking block")
    expect(result.current.turns[0].parts[1].type).toBe("thinking")
    expect(result.current.turns[0].parts[1].content).toBe("Second thinking block")
  })

  it("accumulates streaming thinking deltas into one part", () => {
    const { result } = renderHook(() =>
      useChat({ wsUrl: "ws://localhost:8080/ws", token: "test", sessionId: "s1" }),
    )
    const onMessage = getOnMessage()

    // Streaming delta (metadata.streaming = true)
    act(() => {
      onMessage({
        type: "chat_event",
        channel: "session:s1",
        payload: { type: "thinking", content: "analyzing", metadata: { streaming: true } },
      })
    })

    act(() => {
      onMessage({
        type: "chat_event",
        channel: "session:s1",
        payload: { type: "thinking", content: " the code", metadata: { streaming: true } },
      })
    })

    // Should be ONE part with accumulated content
    expect(result.current.turns).toHaveLength(1)
    expect(result.current.turns[0].parts).toHaveLength(1)
    expect(result.current.turns[0].parts[0].content).toBe("analyzing the code")
  })

  it("handles tool_call + tool_result parts in one turn", () => {
    const { result } = renderHook(() =>
      useChat({ wsUrl: "ws://localhost:8080/ws", token: "test", sessionId: "s1" }),
    )
    const onMessage = getOnMessage()

    act(() => {
      onMessage({
        type: "chat_event",
        channel: "session:s1",
        payload: { type: "tool_call", content: "Read", metadata: { tool_name: "Read", tool_id: "t1" } },
      })
    })

    act(() => {
      onMessage({
        type: "chat_event",
        channel: "session:s1",
        payload: { type: "tool_result", content: "file contents", metadata: { tool_use_id: "t1" } },
      })
    })

    expect(result.current.turns).toHaveLength(1)
    expect(result.current.turns[0].parts).toHaveLength(2)
    expect(result.current.turns[0].parts[0].type).toBe("tool_call")
    expect(result.current.turns[0].parts[1].type).toBe("tool_result")
  })

  it("status events appear before text", () => {
    const { result } = renderHook(() =>
      useChat({ wsUrl: "ws://localhost:8080/ws", token: "test", sessionId: "s1" }),
    )
    const onMessage = getOnMessage()

    act(() => {
      onMessage({
        type: "chat_event",
        channel: "session:s1",
        payload: { type: "status", content: "Starting container..." },
      })
    })

    expect(result.current.turns).toHaveLength(1)
    expect(result.current.turns[0].parts[0].type).toBe("status")
    expect(result.current.turns[0].parts[0].content).toBe("Starting container...")

    act(() => {
      onMessage({
        type: "chat_event",
        channel: "session:s1",
        payload: { type: "text", content: "Response" },
      })
    })

    // Status part is removed when text arrives (transient indicator)
    expect(result.current.turns).toHaveLength(1)
    expect(result.current.turns[0].parts).toHaveLength(1)
    expect(result.current.turns[0].parts[0].type).toBe("text")
  })

  it("done event marks turn as not streaming and removes status parts", () => {
    const { result } = renderHook(() =>
      useChat({ wsUrl: "ws://localhost:8080/ws", token: "test", sessionId: "s1" }),
    )
    const onMessage = getOnMessage()

    act(() => {
      result.current.sendMessage("hello")
    })

    act(() => {
      onMessage({ type: "chat_event", channel: "session:s1", payload: { type: "status", content: "Setting up..." } })
    })

    act(() => {
      onMessage({ type: "chat_event", channel: "session:s1", payload: { type: "text", content: "response" } })
    })

    act(() => {
      onMessage({ type: "chat_event", channel: "session:s1", payload: { type: "done" } })
    })

    expect(result.current.isStreaming).toBe(false)
    const assistantTurn = result.current.turns[result.current.turns.length - 1]
    expect(assistantTurn.isStreaming).toBe(false)
    // Status parts should be removed after done
    const statusParts = assistantTurn.parts.filter((p) => p.type === "status")
    expect(statusParts).toHaveLength(0)
  })

  it("handles error event", () => {
    const { result } = renderHook(() =>
      useChat({ wsUrl: "ws://localhost:8080/ws", token: "test", sessionId: "s1" }),
    )
    const onMessage = getOnMessage()

    act(() => {
      onMessage({
        type: "chat_event",
        channel: "session:s1",
        payload: { type: "error", content: "Something went wrong" },
      })
    })

    // Error creates a system turn
    const lastTurn = result.current.turns[result.current.turns.length - 1]
    expect(lastTurn).toBeDefined()
    expect(lastTurn.parts[0].type).toBe("error")
    expect(lastTurn.parts[0].content).toBe("Something went wrong")
  })

  it("ignores events for different session", () => {
    const { result } = renderHook(() =>
      useChat({ wsUrl: "ws://localhost:8080/ws", token: "test", sessionId: "s1" }),
    )
    const onMessage = getOnMessage()

    act(() => {
      onMessage({
        type: "chat_event",
        channel: "session:s2",
        payload: { type: "text", content: "wrong session" },
      })
    })

    expect(result.current.turns).toHaveLength(0)
  })

  it("stopGeneration sends cancel_message", () => {
    const { result } = renderHook(() =>
      useChat({ wsUrl: "ws://localhost:8080/ws", token: "test", sessionId: "s1" }),
    )

    act(() => {
      result.current.stopGeneration()
    })

    expect(mockSend).toHaveBeenCalledWith(
      expect.objectContaining({ type: "cancel_message" }),
    )
  })

  it("stopGeneration clears part-level isStreaming flags on the open turn", () => {
    const { result } = renderHook(() =>
      useChat({ wsUrl: "ws://localhost:8080/ws", token: "test", sessionId: "s1" }),
    )
    const onMessage = getOnMessage()

    // Open an assistant turn with a streaming text part.
    act(() => {
      onMessage({
        type: "chat_event",
        channel: "session:s1",
        payload: { type: "text", content: "Hello" },
      })
    })
    expect(result.current.turns[0].isStreaming).toBe(true)
    expect(result.current.turns[0].parts[0].isStreaming).toBe(true)

    act(() => {
      result.current.stopGeneration()
    })

    // Turn AND every streaming part are flipped off in one update.
    expect(result.current.turns[0].isStreaming).toBe(false)
    expect(result.current.turns[0].parts[0].isStreaming).toBe(false)
  })

  it("stopGeneration drops late deltas so cancelled stream cannot resurrect", () => {
    const { result } = renderHook(() =>
      useChat({ wsUrl: "ws://localhost:8080/ws", token: "test", sessionId: "s1" }),
    )
    const onMessage = getOnMessage()

    // Open an assistant turn.
    act(() => {
      onMessage({
        type: "chat_event",
        channel: "session:s1",
        payload: { type: "text", content: "Hello" },
      })
    })

    act(() => {
      result.current.stopGeneration()
    })

    // After cancel, late deltas race against the server's cancel ack.
    // They must NOT extend the cancelled turn or create a new one.
    act(() => {
      onMessage({
        type: "chat_event",
        channel: "session:s1",
        payload: { type: "text", content: " — late delta" },
      })
    })

    expect(result.current.turns).toHaveLength(1)
    expect(result.current.turns[0].parts).toHaveLength(1)
    expect(result.current.turns[0].parts[0].content).toBe("Hello")
    expect(result.current.turns[0].isStreaming).toBe(false)

    // sendMessage clears the cancelled gate so the next stream flows again.
    act(() => {
      result.current.sendMessage("again")
    })
    act(() => {
      onMessage({
        type: "chat_event",
        channel: "session:s1",
        payload: { type: "text", content: "fresh reply" },
      })
    })
    const lastTurn = result.current.turns[result.current.turns.length - 1]
    expect(lastTurn.role).toBe("assistant")
    expect(lastTurn.parts[0].content).toBe("fresh reply")
  })

  it("loadHistory converts flat messages to turns", () => {
    const { result } = renderHook(() =>
      useChat({ wsUrl: "ws://localhost:8080/ws", token: "test", sessionId: "s1" }),
    )

    act(() => {
      result.current.loadHistory([
        { id: "1", role: "user", content: "hello", timestamp: new Date() },
        { id: "2", role: "assistant", content: "hi there", timestamp: new Date() },
      ])
    })

    expect(result.current.turns).toHaveLength(2)
    expect(result.current.turns[0].role).toBe("user")
    expect(result.current.turns[1].role).toBe("assistant")
    // flat messages should also work
    expect(result.current.messages).toHaveLength(2)
  })
})
