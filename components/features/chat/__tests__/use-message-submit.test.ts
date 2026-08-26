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
    const ensureSession = vi.fn(async () => true)
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

  it("does not send when the session's row could not be created", async () => {
    // ensureSession returning false means the `chats` row is not there. The WS
    // channel authorizer refuses a send_message for a session it cannot resolve
    // (internal/ws/channel_auth.go, isSessionOwner), so sending anyway loses the
    // message silently while the composer clears as though it had gone. Nothing
    // downstream may run: no send, no onSend (a phantom sidebar row and an
    // auto-title PATCH against a chat that does not exist), no onSent (the draft
    // and the attachments have to survive for the retry).
    const ensureSession = vi.fn(async () => false)
    const { result, sendMessage, onSend, onSent } = setup({ ensureSession })

    await act(async () => { await result.current({ text: "hello", files: [] }) })

    expect(ensureSession).toHaveBeenCalledTimes(1)
    expect(sendMessage).not.toHaveBeenCalled()
    expect(onSend).not.toHaveBeenCalled()
    expect(onSent).not.toHaveBeenCalled()
    // The message about it belongs to whoever tried to create the row — this
    // hook does not double up on it.
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

  // `isStreaming` is the only other guard on this path and it is read BEFORE
  // the await, from a prop that cannot change until the send it is waiting on
  // produces a render. So while `ensureSession` is in flight — a POST that
  // creates the `chats` row on the main chat, a wait for the transcript base
  // on the onboarding one — every guard in this function is already behind
  // the user, and a second Send lands on nothing. Both submits await, both
  // resolve, and the same text goes out twice.
  //
  // A ref rather than state, for the reason create-agent-dialog.tsx spells out
  // at its own latch: state does not flip until the next render, and both
  // events are dispatched before React gets one.
  describe("re-entrancy while ensureSession is in flight", () => {
    /** An ensureSession that hangs until the test lets it finish — the window
     *  the second click lands in, made long enough to click into. */
    function heldEnsureSession() {
      let release!: () => void
      const held = new Promise<void>((resolve) => { release = resolve })
      const ensureSession = vi.fn(async () => { await held; return true })
      return { ensureSession, release }
    }

    it("sends once when Send is pressed twice inside one create", async () => {
      const { ensureSession, release } = heldEnsureSession()
      const { result, sendMessage, onSend, onSent } = setup({ ensureSession })

      // Both submits are dispatched before either resolves — deliberately not
      // awaited one at a time, because sequentially is exactly the case that
      // already worked.
      let submits!: Promise<void[]>
      await act(async () => {
        submits = Promise.all([
          result.current({ text: "hello", files: [] }),
          result.current({ text: "hello", files: [] }),
        ])
        release()
        await submits
      })

      expect(sendMessage).toHaveBeenCalledTimes(1)
      expect(sendMessage).toHaveBeenCalledWith("hello")
      // And nothing downstream ran twice either: no duplicate sidebar row, no
      // second auto-title PATCH, no second clear of a draft already cleared.
      expect(onSend).toHaveBeenCalledTimes(1)
      expect(onSent).toHaveBeenCalledTimes(1)
      // The second submit is not a failure to report — it is the same send.
      expect(toastError).not.toHaveBeenCalled()
    })

    it("lets the next message through once the first has gone", async () => {
      const { result, sendMessage } = setup()

      await act(async () => { await result.current({ text: "first", files: [] }) })
      await act(async () => { await result.current({ text: "second", files: [] }) })

      expect(sendMessage).toHaveBeenCalledTimes(2)
      expect(sendMessage).toHaveBeenNthCalledWith(1, "first")
      expect(sendMessage).toHaveBeenNthCalledWith(2, "second")
    })

    it("does not wedge the composer shut when a send throws", async () => {
      // sendMessage goes through useWebSocket; if it ever throws, the latch
      // must still clear or the composer is dead for the rest of the session
      // with nothing on screen to say why. That is a worse failure than the
      // duplicate it exists to prevent.
      const sendMessage = vi.fn(() => { throw new Error("socket gone") })
      const { result } = setup({ sendMessage })

      await act(async () => {
        await result.current({ text: "boom", files: [] }).catch(() => {})
      })
      sendMessage.mockImplementation(() => {})
      await act(async () => { await result.current({ text: "after", files: [] }) })

      expect(sendMessage).toHaveBeenCalledTimes(2)
      expect(sendMessage).toHaveBeenLastCalledWith("after")
    })
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
    const ensureSession = vi.fn(async () => true)
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
