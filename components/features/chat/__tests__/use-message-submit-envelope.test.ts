import { describe, it, expect, vi, beforeEach } from "vitest"
import { renderHook, act } from "@testing-library/react"

const toastError = vi.fn()
vi.mock("sonner", () => ({
  toast: { error: (...args: unknown[]) => toastError(...args) },
}))

import {
  useMessageSubmit,
  checkChatMessageSize,
} from "@/components/features/chat/hooks/use-message-submit"
import { WS_MAX_OUTBOUND_FRAME_BYTES } from "@/hooks/use-websocket"
import {
  ASK_SUBMISSION_METADATA_KEY,
  type AskSubmissionEnvelope,
} from "@/components/features/chat/asks/ask-envelope"

function envelope(overrides: Partial<AskSubmissionEnvelope> = {}): AskSubmissionEnvelope {
  return {
    submission_id: "sub_1",
    form_id: "receipt",
    form_label: "Add a receipt",
    form_version: 1,
    values: { vendor: "Acme" },
    rendered_text: "Receipt from Acme",
    ...overrides,
  }
}

/** The frame useChat's sendMessage actually hands to the socket, rebuilt here
 *  so the guard can be checked against the real bytes rather than its own
 *  arithmetic. */
function wireFrameBytes(sessionId: string, content: string, metadata?: Record<string, unknown>): number {
  return new TextEncoder().encode(
    JSON.stringify({
      type: "send_message",
      payload: JSON.stringify(
        metadata ? { session_id: sessionId, content, metadata } : { session_id: sessionId, content },
      ),
    }),
  ).length
}

describe("checkChatMessageSize measures what actually goes on the wire", () => {
  it("counts the envelope's bytes, not just the text's", () => {
    const env = envelope({ values: { notes: "n".repeat(4000) } })
    const metadata = { [ASK_SUBMISSION_METADATA_KEY]: env }

    const withEnvelope = checkChatMessageSize("chat-1", "hi", metadata)
    const withoutEnvelope = checkChatMessageSize("chat-1", "hi")

    expect(withEnvelope.sizeBytes).toBe(wireFrameBytes("chat-1", "hi", metadata))
    expect(withEnvelope.sizeBytes).toBeGreaterThan(withoutEnvelope.sizeBytes + 4000)
  })

  it("refuses a message that only goes over the cap once the envelope is added", () => {
    // Text alone fits with room to spare; the envelope is what pushes the frame
    // past the cap. Before the envelope rode in the payload the guard passed
    // this and the server tore the socket down on the oversize frame.
    const sessionId = "chat-1"
    const content = "a".repeat(WS_MAX_OUTBOUND_FRAME_BYTES - 2000)
    const metadata = {
      [ASK_SUBMISSION_METADATA_KEY]: envelope({ values: { notes: "n".repeat(8000) } }),
    }

    expect(checkChatMessageSize(sessionId, content).ok).toBe(true)

    const guarded = checkChatMessageSize(sessionId, content, metadata)
    expect(guarded.ok).toBe(false)
    expect(guarded.sizeBytes).toBe(wireFrameBytes(sessionId, content, metadata))
    expect(guarded.message).toMatch(/too large/i)
  })

  it("is unchanged for a message with no envelope", () => {
    expect(checkChatMessageSize("chat-1", "hello").sizeBytes).toBe(
      wireFrameBytes("chat-1", "hello"),
    )
    expect(checkChatMessageSize("chat-1", "hello", undefined).sizeBytes).toBe(
      wireFrameBytes("chat-1", "hello"),
    )
  })
})

describe("useMessageSubmit forwards the envelope", () => {
  beforeEach(() => {
    toastError.mockClear()
  })

  function setup(overrides?: Partial<Parameters<typeof useMessageSubmit>[0]>) {
    const sendMessage = vi.fn()
    const onSent = vi.fn()
    const ensureSession = vi.fn(async () => true)
    const { result } = renderHook(() =>
      useMessageSubmit({
        sessionId: "chat-1",
        isStreaming: false,
        ensureSession,
        sendMessage,
        onSent,
        ...overrides,
      }),
    )
    return { submit: result, sendMessage, onSent, ensureSession }
  }

  it("hands the metadata to sendMessage alongside the unchanged text", async () => {
    const { submit, sendMessage } = setup()
    const metadata = { [ASK_SUBMISSION_METADATA_KEY]: envelope() }

    await act(async () => {
      await submit.current({ text: "Receipt from Acme", files: [], metadata })
    })

    expect(sendMessage).toHaveBeenCalledWith("Receipt from Acme", metadata)
  })

  it("sends a plain message with no second argument at all", async () => {
    const { submit, sendMessage } = setup()

    await act(async () => {
      await submit.current({ text: "just typing", files: [] })
    })

    // Not `("just typing", undefined)`. A plain send has to be the same CALL it
    // always was, arity included — every existing caller and spy sees one
    // argument, exactly as before the envelope existed.
    expect(sendMessage).toHaveBeenCalledWith("just typing")
    expect(sendMessage.mock.calls[0]).toHaveLength(1)
  })

  it("refuses — and clears nothing — when the envelope pushes the frame over the cap", async () => {
    const { submit, sendMessage, onSent } = setup()
    const metadata = {
      [ASK_SUBMISSION_METADATA_KEY]: envelope({
        values: { notes: "n".repeat(WS_MAX_OUTBOUND_FRAME_BYTES) },
      }),
    }

    await act(async () => {
      await submit.current({ text: "short", files: [], metadata })
    })

    expect(sendMessage).not.toHaveBeenCalled()
    expect(onSent).not.toHaveBeenCalled()
    expect(toastError).toHaveBeenCalledWith(expect.stringMatching(/too large/i))
  })
})
