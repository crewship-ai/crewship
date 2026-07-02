import { describe, it, expect } from "vitest"
import { groupOf, SMART_ORDER } from "../inbox-list"
import type { InboxItem } from "@/hooks/use-inbox"

function item(partial: Partial<InboxItem>): InboxItem {
  return {
    id: "i1",
    kind: "message",
    state: "unread",
    priority: "medium",
    title: "t",
    created_at: new Date().toISOString(),
    ...partial,
  } as InboxItem
}

// Agent chat replies must never drown at the bottom of "FYI / advisories" —
// they're the "continue where you left off" items, so they get their own
// smart bucket ranked right under "Decisions needed".
describe("inbox smart grouping — agent replies", () => {
  it("buckets a chat-reply message under Agent replies", () => {
    const g = groupOf(item({ kind: "message", payload: { chat_url: "/chat/casey?session=c1" } }), "smart")
    expect(g.key).toBe("sm:replies")
    expect(g.label).toBe("Agent replies")
  })

  it("ranks Agent replies above review and FYI, below decisions", () => {
    expect(SMART_ORDER["sm:decisions"]).toBeLessThan(SMART_ORDER["sm:replies"])
    expect(SMART_ORDER["sm:replies"]).toBeLessThan(SMART_ORDER["sm:review"])
    expect(SMART_ORDER["sm:replies"]).toBeLessThan(SMART_ORDER["sm:fyi"])
  })

  it("keeps non-chat messages in their existing buckets", () => {
    expect(groupOf(item({ kind: "message", payload: { issue_identifier: "ENG-6" } }), "smart").key).toBe("sm:review")
    expect(groupOf(item({ kind: "message", payload: {} }), "smart").key).toBe("sm:fyi")
    expect(groupOf(item({ kind: "escalation", payload: {} }), "smart").key).toBe("sm:decisions")
  })
})
