import { describe, it, expect, vi, beforeEach } from "vitest"
import { renderHook, act } from "@testing-library/react"

const toastError = vi.fn()
vi.mock("sonner", () => ({
  toast: {
    error: (...args: unknown[]) => toastError(...args),
  },
}))

import { useMessageSubmit, checkChatMessageSize } from "@/components/features/chat/hooks/use-message-submit"
import { WS_MAX_OUTBOUND_FRAME_BYTES } from "@/hooks/use-websocket"

describe("checkChatMessageSize", () => {
  it("passes a normal short message", () => {
    const result = checkChatMessageSize("session-1", "hello there")
    expect(result.ok).toBe(true)
    expect(result.message).toBe("")
  })

  it("fails a message whose encoded frame exceeds the limit", () => {
    const huge = "x".repeat(WS_MAX_OUTBOUND_FRAME_BYTES + 1000)
    const result = checkChatMessageSize("session-1", huge)
    expect(result.ok).toBe(false)
    expect(result.sizeBytes).toBeGreaterThan(WS_MAX_OUTBOUND_FRAME_BYTES)
    expect(result.message).toMatch(/too large/i)
    expect(result.message).toMatch(/KB/)
  })

  it("sizes by UTF-8 bytes, not JS string length — a paste that's short in", () => {
    // 20,000 rocket emoji: 20,000 UTF-16 code units *2 = 40,000 JS length,
    // but 80,000 bytes on the wire. A .length-based guard using the raw
    // 64 KiB number would let this through; a byte-accurate one must not.
    const content = "🚀".repeat(20000)
    const result = checkChatMessageSize("session-1", content)
    expect(content.length).toBeLessThan(WS_MAX_OUTBOUND_FRAME_BYTES)
    expect(result.ok).toBe(false)
    expect(result.sizeBytes).toBeGreaterThan(content.length)
  })

  it("is right at the boundary: exactly at the limit passes, one byte over fails", () => {
    // Reproduce the exact envelope so we can hit the boundary precisely.
    const sessionId = "s"
    const envelopeBytes = (content: string) =>
      new TextEncoder().encode(
        JSON.stringify({ type: "send_message", payload: JSON.stringify({ session_id: sessionId, content }) }),
      ).length

    // Binary-search-free approach: start with a content string sized so the
    // envelope lands under the limit, then pad up to (and past) the exact
    // boundary one char at a time.
    let content = ""
    while (envelopeBytes(content) < WS_MAX_OUTBOUND_FRAME_BYTES) content += "a"
    // Back off one char so we're exactly at (or one under) the limit.
    content = content.slice(0, -1)
    const atOrUnder = envelopeBytes(content)
    expect(atOrUnder).toBeLessThanOrEqual(WS_MAX_OUTBOUND_FRAME_BYTES)
    expect(checkChatMessageSize(sessionId, content).ok).toBe(true)

    const over = content + "a".repeat(WS_MAX_OUTBOUND_FRAME_BYTES - atOrUnder + 1)
    expect(envelopeBytes(over)).toBeGreaterThan(WS_MAX_OUTBOUND_FRAME_BYTES)
    expect(checkChatMessageSize(sessionId, over).ok).toBe(false)
  })
})

describe("useMessageSubmit", () => {
  beforeEach(() => {
    toastError.mockClear()
  })

  function setup(overrides?: Partial<Parameters<typeof useMessageSubmit>[0]>) {
    const sendMessage = vi.fn()
    const onSend = vi.fn()
    const onSent = vi.fn()
    const ensureSession = vi.fn(async () => {})
    const { result } = renderHook(() =>
      useMessageSubmit({
        sessionId: "session-1",
        isStreaming: false,
        ensureSession,
        sendMessage,
        onSend,
        onSent,
        ...overrides,
      }),
    )
    return { result, sendMessage, onSend, onSent, ensureSession }
  }

  it("sends a normal message and clears the draft", async () => {
    const { result, sendMessage, onSend, onSent, ensureSession } = setup()
    await act(async () => { await result.current({ text: "hello", files: [] }) })

    expect(ensureSession).toHaveBeenCalledTimes(1)
    expect(sendMessage).toHaveBeenCalledWith("hello")
    expect(onSend).toHaveBeenCalledWith("session-1", "hello")
    expect(onSent).toHaveBeenCalledTimes(1)
    expect(toastError).not.toHaveBeenCalled()
  })

  it("blocks an oversize message: never reaches sendMessage/send()", async () => {
    // This is the bug: a paste over the server's 64 KiB frame cap used to
    // sail straight into sendMessage -> useWebSocket.send() -> ws.send(),
    // which the server's readPump treats as a fatal read error, killing
    // the whole connection. The guard must stop it before ensureSession
    // or sendMessage are ever called.
    const { result, sendMessage, onSend, onSent, ensureSession } = setup()
    const huge = "x".repeat(WS_MAX_OUTBOUND_FRAME_BYTES + 5000)

    await act(async () => { await result.current({ text: huge, files: [] }) })

    expect(sendMessage).not.toHaveBeenCalled()
    expect(ensureSession).not.toHaveBeenCalled()
    expect(onSend).not.toHaveBeenCalled()
    expect(onSent).not.toHaveBeenCalled()
  })

  it("shows a clear, actionable error for an oversize message", async () => {
    const { result } = setup()
    const huge = "x".repeat(WS_MAX_OUTBOUND_FRAME_BYTES + 5000)

    await act(async () => { await result.current({ text: huge, files: [] }) })

    expect(toastError).toHaveBeenCalledTimes(1)
    const [msg] = toastError.mock.calls[0]
    expect(msg).toMatch(/too large/i)
    expect(msg).toMatch(/KB/)
  })

  it("preserves the draft on block — onSent (which clears input) is never called", async () => {
    const { result, onSent } = setup()
    const huge = "🚀".repeat(20000)

    await act(async () => { await result.current({ text: huge, files: [] }) })

    expect(onSent).not.toHaveBeenCalled()
  })

  it("still guards a huge multi-byte (emoji) paste even though JS .length looks small enough", async () => {
    const { result, sendMessage } = setup()
    // 20k rockets: 40,000 JS length (looks well under the 61,440-byte
    // limit if you naively compare against .length) but 80,000+ bytes on
    // the wire once UTF-8 encoded.
    const content = "🚀".repeat(20000)
    expect(content.length).toBeLessThan(WS_MAX_OUTBOUND_FRAME_BYTES)

    await act(async () => { await result.current({ text: content, files: [] }) })

    expect(sendMessage).not.toHaveBeenCalled()
  })

  it("ignores empty/whitespace-only text without invoking the guard's error path", async () => {
    const { result, sendMessage } = setup()
    await act(async () => { await result.current({ text: "   ", files: [] }) })
    expect(sendMessage).not.toHaveBeenCalled()
    expect(toastError).not.toHaveBeenCalled()
  })

  it("no-ops while streaming, even for an oversize message", async () => {
    const { result, sendMessage } = setup({ isStreaming: true })
    await act(async () => { await result.current({ text: "hello", files: [] }) })
    expect(sendMessage).not.toHaveBeenCalled()
    expect(toastError).not.toHaveBeenCalled()
  })
})
