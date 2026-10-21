import { describe, it, expect, vi, beforeEach } from "vitest"

// The legacy (unversioned) chat stream: Stop must leave a visible, cancelled
// assistant turn so the turn end note can say the reply was stopped.
const mockSend = vi.fn()

vi.mock("@/hooks/use-websocket", () => ({
  WS_MAX_OUTBOUND_FRAME_BYTES: 60 * 1024,
  encodedByteLength: (value: string) => new TextEncoder().encode(value).length,
  useWebSocket: vi.fn(({ onMessage }: { onMessage?: (msg: unknown) => void }) => {
    if (onMessage) (globalThis as Record<string, unknown>).__legacyStopOnMessage = onMessage
    return { status: "connected", send: mockSend, disconnect: vi.fn(), reconnect: vi.fn() }
  }),
}))

vi.stubGlobal("crypto", { randomUUID: () => "uuid-" + Math.random().toString(36).slice(2, 10) })

import { renderHook, act } from "@testing-library/react"
import { useChat } from "@/hooks/use-chat"

const getToken = vi.fn(async () => "test")
let rafQueue: Array<FrameRequestCallback | undefined>

beforeEach(() => {
  vi.clearAllMocks()
  rafQueue = []
  vi.stubGlobal("requestAnimationFrame", (cb: FrameRequestCallback) => { rafQueue.push(cb); return rafQueue.length })
  vi.stubGlobal("cancelAnimationFrame", (id: number) => { rafQueue[id - 1] = undefined })
})

function setup() {
  return renderHook(() => useChat({ wsUrl: "ws://localhost/ws", getToken, sessionId: "s1" }))
}

function emit(type: string, content = "", metadata?: Record<string, unknown>) {
  const onMessage = (globalThis as Record<string, unknown>).__legacyStopOnMessage as (msg: unknown) => void
  act(() => onMessage({ type: "chat_event", channel: "session:s1", payload: { type, content, metadata } }))
}

function flushFrames() {
  act(() => { const pending = rafQueue; rafQueue = []; for (const cb of pending) cb?.(0) })
}

describe("legacy stream Stop", () => {
  it("keeps a status-only turn as an empty cancelled turn", () => {
    const { result } = setup()
    act(() => { result.current.sendMessage("hi") })
    emit("status", "Starting...")
    act(() => result.current.stopGeneration())

    const turns = result.current.turns
    expect(turns.map((t) => t.role)).toEqual(["user", "assistant"])
    expect(turns[1]).toMatchObject({ parts: [], isStreaming: false, turnState: "cancelled" })
    expect(result.current.isStreaming).toBe(false)
  })

  it("adds a cancelled turn when nothing streamed before Stop", () => {
    const { result } = setup()
    act(() => { result.current.sendMessage("hi") })
    act(() => result.current.stopGeneration())

    const turns = result.current.turns
    expect(turns.map((t) => t.role)).toEqual(["user", "assistant"])
    expect(turns[1]).toMatchObject({ parts: [], isStreaming: false, turnState: "cancelled" })
  })

  it("marks a partial reply cancelled and keeps its text", () => {
    const { result } = setup()
    act(() => { result.current.sendMessage("hi") })
    emit("text", "Half a")
    flushFrames()
    act(() => result.current.stopGeneration())

    const reply = result.current.turns[1]
    expect(reply.turnState).toBe("cancelled")
    expect(reply.parts.map((p) => p.content)).toEqual(["Half a"])
  })

  it("survives the late done ack and late deltas", () => {
    const { result } = setup()
    act(() => { result.current.sendMessage("hi") })
    emit("status", "Starting...")
    act(() => result.current.stopGeneration())
    emit("text", "late")
    emit("done", "", { message_id: "m1" })
    flushFrames()

    const turns = result.current.turns
    expect(turns).toHaveLength(2)
    expect(turns[1]).toMatchObject({ parts: [], turnState: "cancelled", isStreaming: false })
  })

  it("does not cancel turns that were not streaming", () => {
    const { result } = setup()
    act(() => { result.current.sendMessage("first") })
    emit("text", "done reply")
    emit("done", "", { message_id: "m1" })
    flushFrames()
    act(() => result.current.stopGeneration())

    expect(result.current.turns).toHaveLength(2)
    expect(result.current.turns[1].turnState).toBeUndefined()
  })

  it("Regenerate replaces the stopped turn instead of stacking another", () => {
    const { result } = setup()
    act(() => { result.current.sendMessage("hi") })
    emit("status", "Starting...")
    act(() => result.current.stopGeneration())
    act(() => result.current.regenerateLastTurn())
    emit("text", "fresh")
    flushFrames()

    const turns = result.current.turns
    expect(turns.map((t) => t.role)).toEqual(["user", "assistant"])
    expect(turns[1].turnState).toBeUndefined()
    expect(turns[1].parts.map((p) => p.content)).toEqual(["fresh"])
  })

  it("a following send streams into a new turn after the stopped one", () => {
    const { result } = setup()
    act(() => { result.current.sendMessage("hi") })
    act(() => result.current.stopGeneration())
    act(() => { result.current.sendMessage("again") })
    emit("text", "fresh")
    flushFrames()

    const turns = result.current.turns
    expect(turns.map((t) => t.role)).toEqual(["user", "assistant", "user", "assistant"])
    expect(turns[1]).toMatchObject({ parts: [], turnState: "cancelled" })
    expect(turns[3].turnState).toBeUndefined()
    expect(turns[3].parts.map((p) => p.content)).toEqual(["fresh"])
  })
})
