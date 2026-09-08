import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"
import { act, renderHook } from "@testing-library/react"
const socket = vi.hoisted(() => ({ message: (_value: unknown) => {}, connect: () => {}, send: vi.fn() }))
vi.mock("@/hooks/use-websocket", () => ({
  useWebSocket: (options: { onMessage: (value: unknown) => void; onConnect: () => void }) => {
    socket.message = options.onMessage; socket.connect = options.onConnect
    return { status: "connected", send: socket.send, disconnect: vi.fn(), reconnect: vi.fn() }
  },
  encodedByteLength: (s: string) => new TextEncoder().encode(s).length,
  WS_MAX_OUTBOUND_FRAME_BYTES: 65536,
}))
import { useChat } from "../use-chat"
let now = 1000000
beforeEach(() => {
  now = 1000000
  vi.spyOn(Date, "now").mockImplementation(() => now)
  vi.stubGlobal("requestAnimationFrame", vi.fn(() => 1))
  vi.stubGlobal("cancelAnimationFrame", vi.fn())
})
afterEach(() => { vi.restoreAllMocks(); vi.unstubAllGlobals() })
function setup() {
  const complete = vi.fn()
  const view = renderHook(({ id }) => useChat({ wsUrl: "ws://test", getToken: async () => "token", sessionId: id, onReplyCompleted: complete }), { initialProps: { id: "s" } })
  act(() => view.result.current.loadHistory([]))
  now += 100
  return { ...view, complete }
}
function event(type: string, content = "", metadata: Record<string, unknown> = {}, session = "s") {
  act(() => socket.message({ type: "chat_event", channel: `session:${session}`, payload: { type, content, metadata } }))
}
const done = () => event("done", "", { message_id: "durable-id", replied_at: new Date(now).toISOString() })
describe("new assistant completion signal", () => {
  it("fires only on persisted done after streamed text, even when observing another sender", () => {
    const f = setup()
    event("text", "Answer")
    event("tool_call", "tool")
    event("tool_result", "tool output")
    event("text", "Final answer")
    expect(f.complete).not.toHaveBeenCalled()
    done()
    expect(f.complete).toHaveBeenCalledExactlyOnceWith({ sessionId: "s", repliedAt: new Date(now).toISOString() })
    done()
    expect(f.complete).toHaveBeenCalledTimes(1)
  })
  it.each(["tools", "error", "cancel", "no-reply", "no-timestamp", "other-session", "old", "future"])("keeps %s silent", reason => {
    const f = setup()
    if (reason !== "tools") event("text", "Answer")
    if (reason === "tools") { event("tool_call", "tool"); event("tool_result", "result") }
    if (reason === "error") event("error", "failed")
    if (reason === "cancel") act(() => f.result.current.stopGeneration())
    const metadata: Record<string, unknown> = { message_id: "id", replied_at: new Date(now).toISOString() }
    if (reason === "no-reply") metadata.no_reply = true
    if (reason === "no-timestamp") delete metadata.replied_at
    if (reason === "old") metadata.replied_at = new Date(now - 61000).toISOString()
    if (reason === "future") metadata.replied_at = new Date(now + 6000).toISOString()
    event("done", "", metadata, reason === "other-session" ? "other" : "s")
    expect(f.complete).not.toHaveBeenCalled()
  })
  it("does not notify for loaded history or a completed reply replayed after reconnect", () => {
    const f = setup()
    act(() => f.result.current.loadHistory([{ id: "history", role: "assistant", content: "old", timestamp: new Date(now) }]))
    expect(f.complete).not.toHaveBeenCalled()
    const stamp = new Date(now).toISOString()
    now += 100
    act(() => socket.connect())
    act(() => f.result.current.loadHistory([]))
    event("text", "replayed")
    event("done", "", { message_id: "past", replied_at: stamp })
    expect(f.complete).not.toHaveBeenCalled()
    now += 100
    event("text", "new answer")
    done()
    expect(f.complete).toHaveBeenCalledTimes(1)
  })
  it("resets pending eligibility when changing session", () => {
    const f = setup()
    event("text", "old stream")
    f.rerender({ id: "next" })
    act(() => f.result.current.loadHistory([]))
    now += 100
    event("done", "", { message_id: "id", replied_at: new Date(now).toISOString() }, "next")
    expect(f.complete).not.toHaveBeenCalled()
  })
})
