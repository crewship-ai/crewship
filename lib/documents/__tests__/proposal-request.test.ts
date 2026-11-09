import { describe, expect, it } from "vitest"
import { parseProposalRequest, proposalChatHref, proposalIntent, proposalRequestParam } from "../proposal-request"

describe("proposal request handoff", () => {
  it("round-trips one schema-4 target through the chat address", () => {
    const target = { pageId: "page_1-a", rev: 7, generation: 2 }
    expect(parseProposalRequest(proposalRequestParam(target))).toEqual(target)
    const href = new URL(proposalChatHref("ws", target, "Tighten it"), "https://x.test")
    expect(Object.fromEntries(href.searchParams)).toEqual({ workspace_id: "ws", propose: "page_1-a.7.2", prompt: "Tighten it", draft: "1" })
    expect(proposalIntent(target, "sub-1")).toEqual({ submission_id: "sub-1", page_id: "page_1-a", schema: 4, rev: 7, generation: 2 })
  })

  it.each(["", "page", "page.1", "page.1.2.3", "pa ge.1.2", "page.-1.2", "page.1.x", "a".repeat(129) + ".1.2", "page.9999999999999999.0"])("refuses %j", value => {
    expect(parseProposalRequest(value)).toBeNull()
  })
})
