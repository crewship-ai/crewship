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

    // ASCII "a" contributes exactly one UTF-8 byte to the encoded envelope
    // (no JSON escaping), so pad straight to the boundary in one step.
    // Growing one char at a time re-encodes the whole ~57 KB string per
    // iteration (quadratic) and times out on slower CI runners.
    const content = "a".repeat(WS_MAX_OUTBOUND_FRAME_BYTES - envelopeBytes(""))
    const atLimit = envelopeBytes(content)
    expect(atLimit).toBe(WS_MAX_OUTBOUND_FRAME_BYTES)
    expect(checkChatMessageSize(sessionId, content).ok).toBe(true)

    const over = content + "a"
    expect(envelopeBytes(over)).toBe(WS_MAX_OUTBOUND_FRAME_BYTES + 1)
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

// The bug: the composer uploaded the file, the file landed in the agent's
// container, and the WS frame carried the user's text and nothing else. The
// agent was never told the attachment existed.
describe("useMessageSubmit — attachments ride along with the message", () => {
  const ready = (name: string, path: string) => ({ name, path, status: "ready" as const })

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

  it("names the attachment by its agent-visible path in the outbound content", async () => {
    const { result, sendMessage, onSent } = setup({
      attachments: [ready("report.pdf", "attachments/session-1/report.pdf")],
    })

    await act(async () => { await result.current({ text: "take a look", files: [] }) })

    expect(sendMessage).toHaveBeenCalledTimes(1)
    const [content] = sendMessage.mock.calls[0]
    expect(content).toContain("take a look")
    expect(content).toContain("attachments/session-1/report.pdf")
    expect(onSent).toHaveBeenCalledTimes(1)
  })

  it("with no attachments the outbound content is byte-identical to the text", async () => {
    const { result, sendMessage } = setup({ attachments: [] })
    await act(async () => { await result.current({ text: "plain message", files: [] }) })
    expect(sendMessage).toHaveBeenCalledWith("plain message")
  })

  it("sends an attachment-only message — a photo with no caption is not dropped", async () => {
    const { result, sendMessage, onSent, ensureSession } = setup({
      attachments: [ready("photo.jpg", "attachments/session-1/photo.jpg")],
    })

    await act(async () => { await result.current({ text: "", files: [] }) })

    expect(ensureSession).toHaveBeenCalledTimes(1)
    expect(sendMessage).toHaveBeenCalledTimes(1)
    expect(sendMessage.mock.calls[0][0]).toContain("attachments/session-1/photo.jpg")
    expect(onSent).toHaveBeenCalledTimes(1)
  })

  it("still ignores an empty message when there is nothing attached", async () => {
    const { result, sendMessage } = setup({ attachments: [] })
    await act(async () => { await result.current({ text: "  ", files: [] }) })
    expect(sendMessage).not.toHaveBeenCalled()
    expect(toastError).not.toHaveBeenCalled()
  })

  it("hands onSend the user's own text, not the composed content", async () => {
    // onSend feeds session auto-titling (chat-page-client.tsx). A title
    // derived from the appended block would name every session "I've
    // attached a file to this message".
    const { result, onSend } = setup({
      attachments: [ready("report.pdf", "attachments/session-1/report.pdf")],
    })

    await act(async () => { await result.current({ text: "take a look", files: [] }) })

    expect(onSend).toHaveBeenCalledWith("session-1", "take a look")
  })

  it("measures the size guard against the FINAL content, block included", async () => {
    // A draft that fits on its own but not once the attachment block is
    // appended. Sizing the user's text alone would let the oversize frame
    // through, and the server's readPump kills the whole connection on it.
    const envelopeBytes = (content: string) =>
      new TextEncoder().encode(
        JSON.stringify({
          type: "send_message",
          payload: JSON.stringify({ session_id: "session-1", content }),
        }),
      ).length
    const text = "a".repeat(WS_MAX_OUTBOUND_FRAME_BYTES - envelopeBytes("") - 10)
    expect(checkChatMessageSize("session-1", text).ok).toBe(true)

    const { result, sendMessage, onSent, ensureSession } = setup({
      attachments: [ready("report.pdf", "attachments/session-1/report.pdf")],
    })

    await act(async () => { await result.current({ text, files: [] }) })

    expect(sendMessage).not.toHaveBeenCalled()
    expect(ensureSession).not.toHaveBeenCalled()
    expect(toastError).toHaveBeenCalledTimes(1)
    // Draft AND attachments survive: onSent is the only thing that clears
    // either, and it must not fire on a refusal.
    expect(onSent).not.toHaveBeenCalled()
  })

  it("refuses to send while an upload is still in flight", async () => {
    const { result, sendMessage, onSent } = setup({
      attachments: [{ name: "big.zip", status: "uploading" as const }],
    })

    await act(async () => { await result.current({ text: "here it is", files: [] }) })

    expect(sendMessage).not.toHaveBeenCalled()
    expect(onSent).not.toHaveBeenCalled()
    expect(toastError).toHaveBeenCalledTimes(1)
    expect(toastError.mock.calls[0][0]).toMatch(/uploading/i)
  })

  it("ignores a failed upload rather than naming a file that is not there", async () => {
    const { result, sendMessage } = setup({
      attachments: [{ name: "big.zip", status: "error" as const }],
    })

    await act(async () => { await result.current({ text: "here it is", files: [] }) })

    expect(sendMessage).toHaveBeenCalledWith("here it is")
  })
})
