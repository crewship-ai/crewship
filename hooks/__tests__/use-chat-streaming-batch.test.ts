import { describe, it, expect, vi, beforeEach, afterEach } from "vitest"
import { renderHook, act } from "@testing-library/react"

// Capture the onMessage handler useChat registers so the test can drive
// stream events directly, and stub the socket so nothing touches the network.
let captured: ((msg: unknown) => void) | undefined
vi.mock("@/hooks/use-websocket", () => ({
  useWebSocket: (opts: { onMessage?: (m: unknown) => void }) => {
    captured = opts.onMessage
    return { status: "connected", send: vi.fn() }
  },
}))

import { useChat } from "@/hooks/use-chat"

const SESSION = "sess-1"

function textEvent(content: string) {
  return { type: "chat_event", channel: `session:${SESSION}`, payload: { type: "text", content } }
}
function doneEvent() {
  return { type: "chat_event", channel: `session:${SESSION}`, payload: { type: "done" } }
}

let rafCbs: Array<FrameRequestCallback | undefined>
beforeEach(() => {
  rafCbs = []
  vi.stubGlobal("requestAnimationFrame", (cb: FrameRequestCallback) => {
    rafCbs.push(cb)
    return rafCbs.length
  })
  vi.stubGlobal("cancelAnimationFrame", (id: number) => {
    rafCbs[id - 1] = undefined
  })
})
afterEach(() => {
  vi.unstubAllGlobals()
  captured = undefined
})

function dispatch(msg: unknown) {
  act(() => {
    captured?.(msg)
  })
}
function flushFrames() {
  act(() => {
    const pending = rafCbs.slice()
    rafCbs = []
    for (const cb of pending) cb?.(0)
  })
}

function setup() {
  return renderHook(() =>
    useChat({ wsUrl: "ws://x/ws", getToken: async () => "tok", sessionId: SESSION }),
  )
}

describe("useChat streaming RAF batching", () => {
  it("defers text rendering to an animation frame instead of per-token setTurns", () => {
    const { result } = setup()

    dispatch(textEvent("a"))

    // Pre-fix: setTurns fired synchronously and the turn would already
    // exist. Batched: the token is buffered and a frame is scheduled.
    expect(result.current.turns).toHaveLength(0)
    expect(rafCbs.filter(Boolean)).toHaveLength(1)
  })

  it("coalesces several tokens in one frame into a single committed text part", () => {
    const { result } = setup()

    dispatch(textEvent("Hel"))
    dispatch(textEvent("lo "))
    dispatch(textEvent("world"))

    // One frame scheduled for the whole burst, not three.
    expect(rafCbs.filter(Boolean)).toHaveLength(1)

    flushFrames()

    expect(result.current.turns).toHaveLength(1)
    const turn = result.current.turns[0]
    expect(turn.role).toBe("assistant")
    const text = turn.parts.find((p) => p.type === "text")
    expect(text?.content).toBe("Hello world")
  })

  it("flushes pending text before a non-text event so ordering is preserved", () => {
    const { result } = setup()

    dispatch(textEvent("partial"))
    // 'done' is a non-text event: it must flush the buffered text first,
    // so the committed turn carries the text AND is finalized.
    dispatch(doneEvent())

    const turn = result.current.turns[0]
    expect(turn.parts.find((p) => p.type === "text")?.content).toBe("partial")
    expect(turn.isStreaming).toBe(false)
  })
})
