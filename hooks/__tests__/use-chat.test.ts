import { describe, it, expect, vi, beforeEach } from "vitest"

// Mock useWebSocket to avoid real WebSocket connections
const mockSend = vi.fn()
const mockStatus = { current: "connected" as string }

vi.mock("@/hooks/use-websocket", () => ({
  useWebSocket: vi.fn(({ onMessage }: { onMessage?: (msg: unknown) => void }) => {
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

  it("starts with empty messages and not streaming", () => {
    const { result } = renderHook(() =>
      useChat({ wsUrl: "ws://localhost:8080/ws", token: "test", sessionId: "s1" }),
    )
    expect(result.current.messages).toHaveLength(0)
    expect(result.current.isStreaming).toBe(false)
  })

  it("sendMessage adds user message and calls ws send", () => {
    const { result } = renderHook(() =>
      useChat({ wsUrl: "ws://localhost:8080/ws", token: "test", sessionId: "s1" }),
    )

    act(() => {
      result.current.sendMessage("hello")
    })

    expect(result.current.messages).toHaveLength(1)
    expect(result.current.messages[0].role).toBe("user")
    expect(result.current.messages[0].content).toBe("hello")
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

    expect(result.current.messages).toHaveLength(0)
    expect(mockSend).not.toHaveBeenCalled()
  })

  it("handles text event by creating/updating streaming message", () => {
    const { result } = renderHook(() =>
      useChat({ wsUrl: "ws://localhost:8080/ws", token: "test", sessionId: "s1" }),
    )
    const onMessage = getOnMessage()

    act(() => {
      onMessage({
        type: "chat_event",
        event_type: "text",
        content: "Hello ",
        session_id: "s1",
      })
    })

    expect(result.current.messages).toHaveLength(1)
    expect(result.current.messages[0].role).toBe("assistant")
    expect(result.current.messages[0].isStreaming).toBe(true)
  })

  it("handles done event by finalizing stream", () => {
    const { result } = renderHook(() =>
      useChat({ wsUrl: "ws://localhost:8080/ws", token: "test", sessionId: "s1" }),
    )
    const onMessage = getOnMessage()

    act(() => {
      result.current.sendMessage("hello")
    })

    act(() => {
      onMessage({ type: "chat_event", event_type: "text", content: "response", session_id: "s1" })
    })

    act(() => {
      onMessage({ type: "chat_event", event_type: "done", session_id: "s1" })
    })

    expect(result.current.isStreaming).toBe(false)
    const lastMsg = result.current.messages[result.current.messages.length - 1]
    expect(lastMsg.isStreaming).toBeFalsy()
  })

  it("handles error event", () => {
    const { result } = renderHook(() =>
      useChat({ wsUrl: "ws://localhost:8080/ws", token: "test", sessionId: "s1" }),
    )
    const onMessage = getOnMessage()

    act(() => {
      onMessage({
        type: "chat_event",
        event_type: "error",
        content: "Something went wrong",
        session_id: "s1",
      })
    })

    const errorMsg = result.current.messages.find((m) => m.eventType === "error")
    expect(errorMsg).toBeDefined()
    expect(errorMsg?.content).toBe("Something went wrong")
    expect(errorMsg?.role).toBe("system")
  })

  it("ignores events for different session", () => {
    const { result } = renderHook(() =>
      useChat({ wsUrl: "ws://localhost:8080/ws", token: "test", sessionId: "s1" }),
    )
    const onMessage = getOnMessage()

    act(() => {
      onMessage({
        type: "chat_event",
        event_type: "text",
        content: "wrong session",
        session_id: "s2",
      })
    })

    expect(result.current.messages).toHaveLength(0)
  })
})
