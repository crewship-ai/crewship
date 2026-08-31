import { describe, it, expect } from "vitest"
import { render, screen } from "@testing-library/react"
import { ChatJumpLink, chatHrefForEntry } from "../chat-jump-link"
import type { JournalEntry } from "@/lib/types/journal"

function entry(payload: Record<string, unknown>, refs?: Record<string, unknown>): JournalEntry {
  return {
    id: "e1",
    ts: "2026-08-31T12:00:00Z",
    entry_type: "chat.user_message",
    severity: "info",
    summary: "user → morgan: 312 characters",
    payload,
    refs,
  } as unknown as JournalEntry
}

describe("chatHrefForEntry", () => {
  it("builds the chat URL from agent_slug + chat_id", () => {
    expect(chatHrefForEntry(entry({ agent_slug: "morgan", chat_id: "c-1" }))).toBe(
      "/chat/morgan?session=c-1",
    )
  })

  // #2229 moved the message text out of the payload, so this link is the only
  // way back to what the user actually said. Falling back to refs matters:
  // the emit site writes chat_id to both, and only refs is guaranteed by the
  // entry contract.
  it("falls back to refs.chat_id when the payload has none", () => {
    expect(chatHrefForEntry(entry({ agent_slug: "morgan" }, { chat_id: "c-2" }))).toBe(
      "/chat/morgan?session=c-2",
    )
  })

  it("encodes both segments", () => {
    expect(chatHrefForEntry(entry({ agent_slug: "a b/c", chat_id: "x?y&z" }))).toBe(
      "/chat/a%20b%2Fc?session=x%3Fy%26z",
    )
  })

  it("returns null when either half is missing or not a string", () => {
    expect(chatHrefForEntry(entry({ agent_slug: "morgan" }))).toBeNull()
    expect(chatHrefForEntry(entry({ chat_id: "c-1" }))).toBeNull()
    expect(chatHrefForEntry(entry({ agent_slug: "morgan", chat_id: "" }))).toBeNull()
    expect(chatHrefForEntry(entry({ agent_slug: 7, chat_id: "c-1" }))).toBeNull()
    expect(chatHrefForEntry(entry({}))).toBeNull()
  })

  // encodeURIComponent already rules these out, but the assertion is cheap and
  // the failure mode (an entry payload steering navigation off-site) is not.
  it("cannot be steered into a protocol-relative or absolute URL", () => {
    const href = chatHrefForEntry(entry({ agent_slug: "//evil.example.com", chat_id: "c" }))
    expect(href).toBe("/chat/%2F%2Fevil.example.com?session=c")
    expect(href?.startsWith("/chat/")).toBe(true)
  })
})

describe("ChatJumpLink", () => {
  it("renders a link when the entry carries both halves", () => {
    render(<ChatJumpLink entry={entry({ agent_slug: "morgan", chat_id: "c-1" })} />)
    expect(screen.getByRole("link", { name: /open chat/i })).toHaveAttribute(
      "href",
      "/chat/morgan?session=c-1",
    )
  })

  it("renders nothing for an entry with no chat reference", () => {
    const { container } = render(<ChatJumpLink entry={entry({ host: "example.com" })} />)
    expect(container).toBeEmptyDOMElement()
  })
})
