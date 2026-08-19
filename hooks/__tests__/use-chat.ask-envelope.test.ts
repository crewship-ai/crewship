import { describe, it, expect, vi, beforeEach } from "vitest"

// Mock useWebSocket so `send` is a spy and no socket is opened. The frame the
// hook hands to send() IS the thing under test here: the envelope has to be on
// the wire, and the text on the wire has to be unchanged by its presence.
const mockSend = vi.fn()

interface UseWebSocketArgs {
  onMessage?: (msg: unknown) => void
}

vi.mock("@/hooks/use-websocket", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@/hooks/use-websocket")>()
  return {
    ...actual,
    useWebSocket: vi.fn(({ onMessage }: UseWebSocketArgs) => {
      if (onMessage) {
        ;(globalThis as Record<string, unknown>).__testOnMessage = onMessage
      }
      return { status: "connected", send: mockSend, disconnect: vi.fn(), reconnect: vi.fn() }
    }),
  }
})

import { renderHook, act } from "@testing-library/react"
import { useChat, messagesToTurns, type ChatMessage } from "@/hooks/use-chat"
import {
  ASK_SUBMISSION_METADATA_KEY,
  type AskSubmissionEnvelope,
} from "@/components/features/chat/asks/ask-envelope"
import {
  askSubmissionForTurn,
  resetAskProvenance,
} from "@/components/features/chat/asks/ask-provenance"

function envelope(overrides: Partial<AskSubmissionEnvelope> = {}): AskSubmissionEnvelope {
  return {
    submission_id: "sub_1",
    form_id: "receipt",
    form_label: "Add a receipt",
    form_version: 2,
    values: { vendor: "Acme", amount: "12.50" },
    field_attachment_ids: { photo: ["attachments/chat-1/receipt.png"] },
    rendered_text: "Receipt from Acme for 12.50",
    ...overrides,
  }
}

/** The last frame handed to send(), decoded the way the server decodes it. */
function lastSentPayload(): Record<string, unknown> {
  const frame = mockSend.mock.calls.at(-1)?.[0] as { type: string; payload: string }
  expect(frame.type).toBe("send_message")
  return JSON.parse(frame.payload) as Record<string, unknown>
}

describe("the submission envelope reaches the wire", () => {
  beforeEach(() => {
    vi.clearAllMocks()
    resetAskProvenance()
  })

  it("puts the envelope in the send_message payload without touching the text", () => {
    const { result } = renderHook(() =>
      useChat({ wsUrl: "ws://x/ws", getToken: async () => "t", sessionId: "chat-1" }),
    )

    const env = envelope()
    act(() => {
      result.current.sendMessage("Receipt from Acme for 12.50", {
        [ASK_SUBMISSION_METADATA_KEY]: env,
      })
    })

    const payload = lastSentPayload()
    expect(payload.session_id).toBe("chat-1")
    // The text is an ordinary message: byte-identical to what a typed message
    // of the same content would carry. Every CLI adapter depends on this.
    expect(payload.content).toBe("Receipt from Acme for 12.50")
    expect(payload.metadata).toEqual({ [ASK_SUBMISSION_METADATA_KEY]: env })
  })

  it("sends a plain message with a payload byte-identical to one with no envelope", () => {
    const { result } = renderHook(() =>
      useChat({ wsUrl: "ws://x/ws", getToken: async () => "t", sessionId: "chat-1" }),
    )

    act(() => {
      result.current.sendMessage("just typing")
    })

    const frame = mockSend.mock.calls.at(-1)?.[0] as { type: string; payload: string }
    // Not "metadata is undefined" — the SERIALIZED payload must be the exact
    // bytes it was before the envelope existed, or every plain send changes.
    expect(frame.payload).toBe(JSON.stringify({ session_id: "chat-1", content: "just typing" }))
    expect(frame.payload).not.toContain("metadata")
  })

  it("carries the envelope on the optimistic turn it just sent", () => {
    const { result } = renderHook(() =>
      useChat({ wsUrl: "ws://x/ws", getToken: async () => "t", sessionId: "chat-1" }),
    )

    const env = envelope()
    act(() => {
      result.current.sendMessage(env.rendered_text, { [ASK_SUBMISSION_METADATA_KEY]: env })
    })

    const turn = result.current.turns.at(-1)!
    expect(turn.role).toBe("user")
    expect(askSubmissionForTurn("chat-1", turn)?.submission_id).toBe("sub_1")
  })
})

describe("a reloaded conversation recovers the envelope", () => {
  beforeEach(() => {
    resetAskProvenance()
  })

  function historyMessage(env: AskSubmissionEnvelope, id: string): ChatMessage {
    return {
      id,
      role: "user",
      content: env.rendered_text,
      timestamp: new Date("2026-01-01T00:00:00Z"),
      // Exactly what the history API hands back: conversation.Message.Metadata,
      // JSON-round-tripped through the JSONL store.
      metadata: JSON.parse(JSON.stringify({ [ASK_SUBMISSION_METADATA_KEY]: env })),
    }
  }

  it("keeps a user message's metadata on the turn — the whole point", () => {
    const env = envelope()
    const turns = messagesToTurns([historyMessage(env, "msg_1")])

    expect(turns).toHaveLength(1)
    const recovered = askSubmissionForTurn("chat-1", turns[0])
    expect(recovered).not.toBeNull()
    expect(recovered!.submission_id).toBe("sub_1")
    expect(recovered!.form_id).toBe("receipt")
    expect(recovered!.form_label).toBe("Add a receipt")
    expect(recovered!.form_version).toBe(2)
    expect(recovered!.values).toEqual({ vendor: "Acme", amount: "12.50" })
    expect(recovered!.field_attachment_ids).toEqual({
      photo: ["attachments/chat-1/receipt.png"],
    })
  })

  it("gives two identical submissions their own submission_id back", () => {
    // Same form, same answers, same rendered text — the collision the old
    // content-keyed map could not survive.
    const first = envelope({ submission_id: "sub_a" })
    const second = envelope({ submission_id: "sub_b" })
    const turns = messagesToTurns([
      historyMessage(first, "msg_1"),
      historyMessage(second, "msg_2"),
    ])

    expect(turns).toHaveLength(2)
    expect(askSubmissionForTurn("chat-1", turns[0])?.submission_id).toBe("sub_a")
    expect(askSubmissionForTurn("chat-1", turns[1])?.submission_id).toBe("sub_b")
  })

  it("leaves a message that came from no form without an envelope", () => {
    const turns = messagesToTurns([
      {
        id: "msg_1",
        role: "user",
        content: "just typing",
        timestamp: new Date("2026-01-01T00:00:00Z"),
      },
    ])
    expect(askSubmissionForTurn("chat-1", turns[0])).toBeNull()
  })
})
