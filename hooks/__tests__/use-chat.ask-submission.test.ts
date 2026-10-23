import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"
import { act, renderHook } from "@testing-library/react"

const transport = vi.hoisted(() => ({ status: "connected", send: vi.fn(), onMessage: undefined as undefined | ((message: unknown) => void) }))
vi.mock("@/hooks/use-websocket", async (importOriginal) => ({
  ...await importOriginal<typeof import("@/hooks/use-websocket")>(),
  useWebSocket: (options: { onMessage?: (message: unknown) => void }) => {
    transport.onMessage = options.onMessage
    return { status: transport.status, send: transport.send, retry: vi.fn() }
  },
}))

import { useChat } from "@/hooks/use-chat"
import { AskSubmissionError } from "@/lib/ask-submission-error"

const metadata = { ask_submission: { submission_id: "sub-1", form_id: "receipt", values: { answer: "wrong" } } }
function receipt(type: string, extra: Record<string, unknown>, channel = "session:chat-1") {
  transport.onMessage?.({ type: "chat_event", channel, payload: { type, content: "Answer is not in the expected format.", metadata: extra } })
}
function start() {
  return renderHook(({ sessionId = "chat-1", userId = "user-1" }) => useChat({ wsUrl: "ws://test/ws", getToken: async () => "t", sessionId, currentUserId: userId }), { initialProps: { sessionId: "chat-1", userId: "user-1" } })
}
function send(result: ReturnType<typeof start>["result"]) {
  let pending!: Promise<boolean>
  act(() => { pending = result.current.sendMessage("answer", metadata) as Promise<boolean> })
  return pending
}

beforeEach(() => { transport.status = "connected"; transport.send.mockReset() })
afterEach(() => { vi.useRealTimers() })

describe("Ask submissions wait for their server receipt", () => {
  it("refuses restricted Ask submissions before HTTP, WebSocket, or optimistic persistence", async () => {
    const fetchSpy = vi.spyOn(globalThis, "fetch")
    try {
      const { result } = renderHook(() => useChat({ wsUrl: "ws://test/ws", getToken: async () => "t", sessionId: "chat-1", currentUserId: "user-1", workspaceId: "ws-1", executionProfile: "restricted" }))
      await act(async () => {
        await expect(result.current.sendMessage("answer", metadata)).rejects.toBeInstanceOf(AskSubmissionError)
      })
      expect(fetchSpy).not.toHaveBeenCalled()
      expect(transport.send.mock.calls.filter(([frame]) => frame.type === "send_message")).toEqual([])
      expect(result.current.turns).toEqual([])
    } finally { fetchSpy.mockRestore() }
  })

  it("resolves only the matching durable receipt from the current author and session", async () => {
    const { result } = start()
    const pending = send(result)
    const settled = vi.fn()
    void pending.then(settled)
    await act(async () => {
      receipt("user_message", { ask_submission_id: "other", author_user_id: "user-1" })
      receipt("user_message", { ask_submission_id: "sub-1", author_user_id: "other" })
      receipt("user_message", { ask_submission_id: "sub-1", author_user_id: "user-1" }, "session:other")
    })
    expect(settled).not.toHaveBeenCalled()
    await act(async () => { receipt("user_message", { ask_submission_id: "sub-1", author_user_id: "user-1" }); expect(await pending).toBe(true) })
    expect(settled).toHaveBeenCalledWith(true)
  })

  it("rejects field validation without a history gate and removes the unsaved optimistic turn", async () => {
    const { result } = start()
    const pending = send(result)
    const rejected = pending.catch((error: unknown) => error)
    expect(result.current.turns.some((turn) => turn.role === "user")).toBe(true)
    await act(async () => {
      receipt("ask_submission_error", { submission_id: "sub-1", ask_errors: [{ field: "answer", code: "pattern", message: "Answer is not in the expected format." }] })
      const error = await rejected
      expect(error).toBeInstanceOf(AskSubmissionError)
      expect((error as AskSubmissionError).errors[0].field).toBe("answer")
    })
    expect(result.current.turns).toEqual([])
    expect(result.current.isStreaming).toBe(false)
  })

  it.each(["error", "agent_busy", "top-level error"])("an uncorrelated %s does not refuse another pending submission", async (type) => {
    const { result } = start()
    act(() => result.current.markHistoryUnavailable())
    const pending = send(result)
    const settled = vi.fn()
    void pending.then(settled, settled)
    await act(async () => {
      if (type === "top-level error") transport.onMessage?.({ type: "error", channel: "session:chat-1", payload: { error: "another request failed" } })
      else receipt(type, {})
    })
    expect(settled).not.toHaveBeenCalled()
    expect(result.current.turns.some((turn) => turn.role === "user")).toBe(true)
    await act(async () => { receipt("user_message", { ask_submission_id: "sub-1", author_user_id: "user-1" }); expect(await pending).toBe(true) })
    expect(settled).toHaveBeenCalledWith(true)
  })

  it.each(["busy", "pre-append runtime failure"])("only a correlated private refusal settles %s", async () => {
    const { result } = start()
    const rejected = send(result).catch((error: unknown) => error)
    await act(async () => {
      receipt("ask_submission_error", { submission_id: "sub-1", ask_errors: [] })
      expect(await rejected).toBeInstanceOf(AskSubmissionError)
    })
    expect(result.current.turns).toEqual([])
  })

  it("rejects a disconnected pending form and does not retry it", async () => {
    const { result, rerender } = start()
    const rejected = send(result).catch((error: unknown) => error)
    const sends = transport.send.mock.calls.filter(([frame]) => frame.type === "send_message").length
    await act(async () => { transport.status = "disconnected"; rerender({ sessionId: "chat-1", userId: "user-1" }) })
    expect(await rejected).toBeInstanceOf(AskSubmissionError)
    act(() => { transport.status = "connected"; rerender({ sessionId: "chat-1", userId: "user-1" }) })
    expect(transport.send.mock.calls.filter(([frame]) => frame.type === "send_message")).toHaveLength(sends)
  })

  it.each(["session", "principal"])("rejects a pending form when its %s changes", async (change) => {
    const { result, rerender } = start()
    const rejected = send(result).catch((error: unknown) => error)
    act(() => { rerender({ sessionId: change === "session" ? "chat-2" : "chat-1", userId: change === "principal" ? "user-2" : "user-1" }) })
    expect(await rejected).toBeInstanceOf(AskSubmissionError)
  })

  it("times out without retrying or claiming that an unconfirmed send was saved", async () => {
    vi.useFakeTimers()
    const { result } = start()
    const rejected = send(result).catch((error: unknown) => error)
    await act(async () => { await vi.advanceTimersByTimeAsync(30000) })
    expect(await rejected).toBeInstanceOf(AskSubmissionError)
    expect(transport.send.mock.calls.filter(([frame]) => frame.type === "send_message")).toHaveLength(1)
    expect(result.current.turns).toEqual([])
  })
})
