import { describe, it, expect, vi, beforeEach, afterEach } from "vitest"
import { renderHook, act } from "@testing-library/react"

let mockInstances: MockWebSocket[] = []

class MockWebSocket {
  url: string
  readyState = 0
  onopen: (() => void) | null = null
  onmessage: ((event: { data: string }) => void) | null = null
  onerror: (() => void) | null = null
  onclose: (() => void) | null = null
  sent: string[] = []

  static CONNECTING = 0
  static OPEN = 1
  static CLOSING = 2
  static CLOSED = 3

  constructor(url: string) {
    this.url = url
    mockInstances.push(this)
  }

  send(data: string) {
    this.sent.push(data)
  }

  close() {
    this.readyState = MockWebSocket.CLOSED
    this.onclose?.()
  }

  simulateOpen() {
    this.readyState = MockWebSocket.OPEN
    this.onopen?.()
  }

  simulateMessage(data: unknown) {
    this.onmessage?.({ data: JSON.stringify(data) })
  }

  simulateError() {
    this.onerror?.()
  }
}

vi.stubGlobal("WebSocket", MockWebSocket)

import { useWebSocket } from "@/hooks/use-websocket"

describe("useWebSocket", () => {
  beforeEach(() => {
    mockInstances = []
    vi.useFakeTimers()
  })

  afterEach(() => {
    vi.useRealTimers()
  })

  it("starts disconnected without a token", () => {
    const { result } = renderHook(() =>
      useWebSocket({ url: "ws://localhost:8080/ws", token: null }),
    )
    expect(result.current.status).toBe("disconnected")
    expect(mockInstances).toHaveLength(0)
  })

  it("connects when token is provided", () => {
    const { result } = renderHook(() =>
      useWebSocket({ url: "ws://localhost:8080/ws", token: "test-token" }),
    )
    expect(mockInstances).toHaveLength(1)
    expect(mockInstances[0].url).toContain("token=test-token")
    expect(result.current.status).toBe("connecting")
  })

  it("sets connected status on open", () => {
    const { result } = renderHook(() =>
      useWebSocket({ url: "ws://localhost:8080/ws", token: "test-token" }),
    )

    act(() => {
      mockInstances[0].simulateOpen()
    })

    expect(result.current.status).toBe("connected")
  })

  it("calls onMessage with valid parsed messages", () => {
    const onMessage = vi.fn()
    renderHook(() =>
      useWebSocket({ url: "ws://localhost:8080/ws", token: "test-token", onMessage }),
    )

    act(() => {
      mockInstances[0].simulateOpen()
    })

    act(() => {
      mockInstances[0].simulateMessage({ type: "chat_event", channel: "session:1", payload: "hello" })
    })

    expect(onMessage).toHaveBeenCalledWith(
      expect.objectContaining({ type: "chat_event", channel: "session:1" }),
    )
  })

  it("ignores invalid JSON messages", () => {
    const onMessage = vi.fn()
    renderHook(() =>
      useWebSocket({ url: "ws://localhost:8080/ws", token: "test-token", onMessage }),
    )

    act(() => {
      mockInstances[0].simulateOpen()
    })

    act(() => {
      mockInstances[0].onmessage?.({ data: "not-json" })
    })

    expect(onMessage).not.toHaveBeenCalled()
  })

  it("ignores messages that fail Zod validation", () => {
    const onMessage = vi.fn()
    renderHook(() =>
      useWebSocket({ url: "ws://localhost:8080/ws", token: "test-token", onMessage }),
    )

    act(() => {
      mockInstances[0].simulateOpen()
    })

    // Missing required 'type' field
    act(() => {
      mockInstances[0].simulateMessage({ channel: "test" })
    })

    expect(onMessage).not.toHaveBeenCalled()
  })

  it("sets error status on WebSocket error", () => {
    const { result } = renderHook(() =>
      useWebSocket({ url: "ws://localhost:8080/ws", token: "test-token" }),
    )

    act(() => {
      mockInstances[0].simulateError()
    })

    expect(result.current.status).toBe("error")
  })

  it("reconnects on close up to maxReconnectAttempts", () => {
    renderHook(() =>
      useWebSocket({
        url: "ws://localhost:8080/ws",
        token: "test-token",
        reconnectInterval: 1000,
        maxReconnectAttempts: 2,
      }),
    )
    expect(mockInstances).toHaveLength(1)

    // First close -> reconnect
    act(() => {
      mockInstances[0].close()
    })
    act(() => {
      vi.advanceTimersByTime(1000)
    })
    expect(mockInstances).toHaveLength(2)

    // Second close -> reconnect
    act(() => {
      mockInstances[1].close()
    })
    act(() => {
      vi.advanceTimersByTime(1000)
    })
    expect(mockInstances).toHaveLength(3)

    // Third close -> no more reconnects (max 2 attempts reached)
    act(() => {
      mockInstances[2].close()
    })
    act(() => {
      vi.advanceTimersByTime(1000)
    })
    expect(mockInstances).toHaveLength(3)
  })

  it("send sends JSON message when connected", () => {
    const { result } = renderHook(() =>
      useWebSocket({ url: "ws://localhost:8080/ws", token: "test-token" }),
    )

    act(() => {
      mockInstances[0].simulateOpen()
    })

    act(() => {
      result.current.send({ type: "ping" })
    })

    expect(mockInstances[0].sent).toHaveLength(1)
    expect(JSON.parse(mockInstances[0].sent[0])).toEqual({ type: "ping" })
  })

  it("send does nothing when not connected", () => {
    const { result } = renderHook(() =>
      useWebSocket({ url: "ws://localhost:8080/ws", token: "test-token" }),
    )

    act(() => {
      result.current.send({ type: "ping" })
    })

    expect(mockInstances[0].sent).toHaveLength(0)
  })

  it("calls onStatusChange callback", () => {
    const onStatusChange = vi.fn()
    renderHook(() =>
      useWebSocket({
        url: "ws://localhost:8080/ws",
        token: "test-token",
        onStatusChange,
      }),
    )

    expect(onStatusChange).toHaveBeenCalledWith("connecting")

    act(() => {
      mockInstances[0].simulateOpen()
    })

    expect(onStatusChange).toHaveBeenCalledWith("connected")
  })

  it("disconnect stops reconnection", () => {
    const { result } = renderHook(() =>
      useWebSocket({
        url: "ws://localhost:8080/ws",
        token: "test-token",
        reconnectInterval: 1000,
        maxReconnectAttempts: 5,
      }),
    )

    act(() => {
      result.current.disconnect()
    })

    act(() => {
      vi.advanceTimersByTime(5000)
    })

    // Only the initial connection, no reconnects after disconnect
    expect(mockInstances).toHaveLength(1)
  })

  it("resets reconnect counter on successful connection", () => {
    renderHook(() =>
      useWebSocket({
        url: "ws://localhost:8080/ws",
        token: "test-token",
        reconnectInterval: 100,
        maxReconnectAttempts: 2,
      }),
    )

    // Open and close
    act(() => { mockInstances[0].simulateOpen() })
    act(() => { mockInstances[0].close() })
    act(() => { vi.advanceTimersByTime(100) })
    expect(mockInstances).toHaveLength(2)

    // Open again (resets counter) and close
    act(() => { mockInstances[1].simulateOpen() })
    act(() => { mockInstances[1].close() })
    act(() => { vi.advanceTimersByTime(100) })
    // Should still reconnect because counter was reset
    expect(mockInstances).toHaveLength(3)
  })
})
