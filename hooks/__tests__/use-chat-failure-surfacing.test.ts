import { describe, it, expect, vi, beforeEach } from "vitest"

// Failure surfacing (issue #545 + crash recovery): a chat turn must
// never end in total silence. These tests pin the two visible halves:
//  - an error event renders as an error bubble on an ASSISTANT turn so
//    the existing retry (Regenerate) affordance appears under it;
//  - a `done` that arrives without any reply turn (zero-output run on a
//    server that couldn't say more) surfaces an explicit no-output
//    error instead of leaving the transcript empty;
//  - persisted system/error turns (bridge no-output turn, boot-recovery
//    "interrupted by restart" turn) reload as error bubbles, not plain
//    text.

// Mock useWebSocket to avoid real WebSocket connections
const mockSend = vi.fn()
const mockStatus = { current: "connected" as string }

interface UseWebSocketArgs {
  onMessage?: (msg: unknown) => void
}

vi.mock("@/hooks/use-websocket", () => ({
  useWebSocket: vi.fn(({ onMessage }: UseWebSocketArgs) => {
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

vi.stubGlobal("crypto", {
  randomUUID: () => "test-uuid-" + Math.random().toString(36).slice(2, 8),
})

import { renderHook, act } from "@testing-library/react"
import { useChat } from "@/hooks/use-chat"

const NO_OUTPUT_COPY = "The agent returned no output — try again"

describe("useChat failure surfacing", () => {
  let rafQueue: Array<FrameRequestCallback | undefined>
  beforeEach(() => {
    vi.clearAllMocks()
    mockStatus.current = "connected"
    rafQueue = []
    vi.stubGlobal("requestAnimationFrame", (cb: FrameRequestCallback) => {
      rafQueue.push(cb)
      return rafQueue.length
    })
    vi.stubGlobal("cancelAnimationFrame", (id: number) => {
      rafQueue[id - 1] = undefined
    })
  })

  function getOnMessage(): (msg: unknown) => void {
    return (globalThis as Record<string, unknown>).__testOnMessage as (msg: unknown) => void
  }

  function setup() {
    return renderHook(() =>
      useChat({ wsUrl: "ws://localhost:8080/ws", getToken: async () => "test", sessionId: "s1" }),
    )
  }

  function emit(payload: Record<string, unknown>) {
    act(() => getOnMessage()({ type: "chat_event", channel: "session:s1", payload }))
  }

  it("standalone error event renders an error bubble on an assistant turn (retry affordance)", () => {
    const { result } = setup()
    act(() => {
      result.current.sendMessage("hi")
    })

    // Zero-output run: the server streams NO text/tools, only the
    // explicit error, then done.
    emit({ type: "error", content: NO_OUTPUT_COPY, metadata: { reason: "no_output" } })
    emit({ type: "done" })

    expect(result.current.turns).toHaveLength(2)
    const errTurn = result.current.turns[1]
    // Assistant role, so the chat's existing Regenerate/retry affordance
    // (rendered under the last assistant turn) appears with the bubble.
    expect(errTurn.role).toBe("assistant")
    expect(errTurn.isStreaming).toBe(false)
    expect(errTurn.parts).toHaveLength(1)
    expect(errTurn.parts[0].type).toBe("error")
    expect(errTurn.parts[0].content).toBe(NO_OUTPUT_COPY)
    expect(result.current.isStreaming).toBe(false)
  })

  it("done with no reply turn surfaces an explicit no-output error instead of an empty transcript", () => {
    const { result } = setup()
    act(() => {
      result.current.sendMessage("hi")
    })

    // Server finished the run without emitting ANYTHING (legacy server /
    // lost error frame). The transcript must not stay user-message-only.
    emit({ type: "done" })

    expect(result.current.isStreaming).toBe(false)
    expect(result.current.turns).toHaveLength(2)
    const fallback = result.current.turns[1]
    expect(fallback.role).toBe("assistant")
    expect(fallback.parts[0].type).toBe("error")
    expect(fallback.parts[0].content).toContain("no output")
  })

  it("does not duplicate the error when the explicit error event already rendered", () => {
    const { result } = setup()
    act(() => {
      result.current.sendMessage("hi")
    })
    emit({ type: "error", content: NO_OUTPUT_COPY, metadata: { reason: "no_output" } })
    emit({ type: "done" })

    const errorParts = result.current.turns.flatMap((t) => t.parts).filter((p) => p.type === "error")
    expect(errorParts).toHaveLength(1)
  })

  it("done marked no_reply (group chat, agent not mentioned) adds nothing", () => {
    const { result } = setup()
    act(() => {
      result.current.sendMessage("hello everyone")
    })

    emit({ type: "done", metadata: { no_reply: true } })

    expect(result.current.turns).toHaveLength(1)
    expect(result.current.turns[0].role).toBe("user")
    expect(result.current.isStreaming).toBe(false)
  })

  it("unsolicited done (no local send pending) does not fabricate an error turn", () => {
    const { result } = setup()
    // Another tab's run finished — we never sent anything here.
    emit({ type: "done" })
    expect(result.current.turns).toHaveLength(0)
  })

  it("done after a normal streamed reply stays a plain assistant turn", () => {
    const { result } = setup()
    act(() => {
      result.current.sendMessage("hi")
    })
    emit({ type: "text", content: "a real answer" })
    act(() => {
      const pending = rafQueue
      rafQueue = []
      for (const cb of pending) cb?.(0)
    })
    emit({ type: "done" })

    expect(result.current.turns).toHaveLength(2)
    const reply = result.current.turns[1]
    expect(reply.parts.map((p) => p.type)).toEqual(["text"])
  })

  it("done with no reply surfaces the fallback error even when a teammate's message landed last (group chat)", () => {
    const { result } = setup()
    act(() => {
      result.current.sendMessage("hi")
    })

    // A teammate's broadcast lands after our send but before any reply —
    // it becomes the tail turn, carrying an authorUserId. A genuine
    // zero-output done must still be caught; it must not be mistaken for
    // "someone else already replied to our run" (issue #545 regression).
    emit({ type: "user_message", content: "me too", metadata: { author_user_id: "teammate-1" } })
    emit({ type: "done" })

    expect(result.current.isStreaming).toBe(false)
    expect(result.current.turns).toHaveLength(3)
    const fallback = result.current.turns[2]
    expect(fallback.role).toBe("assistant")
    expect(fallback.parts[0].type).toBe("error")
    expect(fallback.parts[0].content).toContain("no output")
  })

  it("does not fabricate an error when an assistant reply already streamed before a teammate's later message", () => {
    const { result } = setup()
    act(() => {
      result.current.sendMessage("hi")
    })
    emit({ type: "text", content: "a real answer" })
    act(() => {
      const pending = rafQueue
      rafQueue = []
      for (const cb of pending) cb?.(0)
    })
    // Assistant reply already streamed and finalized before the teammate's
    // message arrives — no synthetic error should be added.
    emit({ type: "user_message", content: "thanks!", metadata: { author_user_id: "teammate-1" } })
    emit({ type: "done" })

    expect(result.current.isStreaming).toBe(false)
    const errorParts = result.current.turns.flatMap((t) => t.parts).filter((p) => p.type === "error")
    expect(errorParts).toHaveLength(0)
  })

  it("loadHistory renders a persisted system error turn as an error bubble", () => {
    const { result } = setup()
    const ts = new Date()
    act(() => {
      result.current.loadHistory([
        { id: "u1", role: "user", content: "hi", timestamp: ts },
        {
          id: "e1",
          role: "system",
          content: "The agent's reply was interrupted by a server restart — try again",
          parts: [{ type: "error", content: "The agent's reply was interrupted by a server restart — try again" }],
          timestamp: ts,
        },
      ])
    })

    expect(result.current.turns).toHaveLength(2)
    const errTurn = result.current.turns[1]
    expect(errTurn.role).toBe("system")
    expect(errTurn.parts[0].type).toBe("error")
    expect(errTurn.parts[0].content).toContain("interrupted by a server restart")
  })
})
