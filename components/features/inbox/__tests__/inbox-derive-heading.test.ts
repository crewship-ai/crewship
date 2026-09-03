import { describe, expect, it } from "vitest"

import type { InboxItem } from "@/hooks/use-inbox"

import { deciderCopy, decisionMetaFor, escalationHeading, linkToOpen } from "../inbox-derive"

function esc(payload: Record<string, unknown>): InboxItem {
  return {
    id: "ibx-1", workspace_id: "ws-1", kind: "escalation", source_id: "esc-1",
    title: "Agent escalation: x", state: "unread", priority: "high", blocking: true,
    created_at: "2026-09-03T13:11:00Z", updated_at: "2026-09-03T13:11:00Z", payload,
  }
}

describe("the decision card heading", () => {
  it("says what an escalation asks for", () => {
    expect(escalationHeading(esc({ escalation_type: "TEXT" }))).toBe("Question from an agent")
    expect(escalationHeading(esc({ escalation_type: "LINK", link_url: "https://x.test/a" }))).toBe("A link to open")
    expect(escalationHeading(esc({ escalation_type: "CREDENTIAL" }))).toBe("Credential request")
    expect(escalationHeading(esc({ request_type: "access", request_id: "kr-1" }))).toBe("Access request")
  })

  it("reaches the card through decisionMetaFor", () => {
    expect(decisionMetaFor(esc({ escalation_type: "TEXT" }))?.heading).toBe("Question from an agent")
    expect(decisionMetaFor(esc({ kind: "routine_proposal" }))?.heading).toBe("Proposed routine")
  })
})

describe("deciderCopy", () => {
  it("never prints a role enum", () => {
    for (const requires of ["create", "manage"] as const) {
      const copy = deciderCopy(requires)
      expect(copy).not.toMatch(/OWNER|ADMIN|MANAGER/)
      expect(copy).toMatch(/decides this$/)
    }
  })
})

describe("linkToOpen", () => {
  it("accepts the https link the server validated", () => {
    expect(linkToOpen(esc({ link_url: "https://github.com/crewship-ai/crewship/pull/1" })))
      .toBe("https://github.com/crewship-ai/crewship/pull/1")
  })
  it("refuses anything that is not an https address", () => {
    expect(linkToOpen(esc({ link_url: "javascript:alert(1)" }))).toBeNull()
    expect(linkToOpen(esc({ link_url: "http://plain.test/" }))).toBeNull()
    expect(linkToOpen(esc({ link_url: "//evil.test/x" }))).toBeNull()
    expect(linkToOpen(esc({}))).toBeNull()
  })
})
