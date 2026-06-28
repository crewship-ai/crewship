import { describe, it, expect } from "vitest"
import { messagesToTurns, type ChatMessage } from "@/hooks/use-chat"

// PR 1.2: the history API now returns each assistant message with a structured
// `parts` array (the same canonical schema the live WebSocket stream uses).
// messagesToTurns must expand those parts into TurnParts so a reloaded turn
// renders thinking + tools + interleaved text exactly as it streamed — not a
// flattened text blob. Legacy messages without `parts` must still render.

describe("messagesToTurns with structured parts", () => {
  it("expands an assistant message's parts into ordered TurnParts", () => {
    const messages: ChatMessage[] = [
      {
        id: "u1",
        role: "user",
        content: "read the file",
        timestamp: new Date("2026-06-26T10:00:00Z"),
      },
      {
        id: "a1",
        role: "assistant",
        content: "Here is the file. Done.",
        timestamp: new Date("2026-06-26T10:00:01Z"),
        parts: [
          { type: "text", content: "Here is the file." },
          { type: "tool_call", content: "Read", tool_name: "Read", tool_id: "t1" },
          { type: "tool_result", content: "file contents", tool_id: "t1" },
          { type: "text", content: "Done." },
        ],
      },
    ]

    const turns = messagesToTurns(messages)

    expect(turns).toHaveLength(2)
    expect(turns[0].role).toBe("user")

    const assistant = turns[1]
    expect(assistant.role).toBe("assistant")
    expect(assistant.parts.map((p) => p.type)).toEqual([
      "text",
      "tool_call",
      "tool_result",
      "text",
    ])
    // text-after-tools must be its own part, not merged into the first bubble
    expect(assistant.parts[0].content).toBe("Here is the file.")
    expect(assistant.parts[3].content).toBe("Done.")
    // tool metadata is carried so the tool card can render name/id
    expect(assistant.parts[1].metadata?.tool_name).toBe("Read")
    expect(assistant.parts[1].metadata?.tool_id).toBe("t1")
    // part ids are stable & unique within the turn
    const ids = assistant.parts.map((p) => p.id)
    expect(new Set(ids).size).toBe(ids.length)
  })

  it("renders a thinking part from history", () => {
    const turns = messagesToTurns([
      {
        id: "a2",
        role: "assistant",
        content: "answer",
        timestamp: new Date(),
        parts: [
          { type: "thinking", content: "let me think" },
          { type: "text", content: "answer" },
        ],
      },
    ])
    expect(turns[0].parts.map((p) => p.type)).toEqual(["thinking", "text"])
    expect(turns[0].parts[0].content).toBe("let me think")
  })

  it("falls back to a single text part for legacy messages without parts", () => {
    const turns = messagesToTurns([
      {
        id: "legacy",
        role: "assistant",
        content: "answer from before the parts model",
        timestamp: new Date(),
      },
    ])
    expect(turns).toHaveLength(1)
    expect(turns[0].parts).toHaveLength(1)
    expect(turns[0].parts[0]).toMatchObject({
      type: "text",
      content: "answer from before the parts model",
    })
  })
})
