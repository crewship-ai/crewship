import { describe, expect, it } from "vitest"
import { parseProposalRequest, proposalChatHref, proposalIntent, proposalRequestParam, proposalSchema } from "../proposal-request"
import { CREATE_SCHEMA, schemaAllows } from "../contracts"

describe("proposal request handoff", () => {
  it.each([4, 5] as const)("round-trips one schema-%i target through the chat address", schema => {
    const target = { pageId: "page_1-a", rev: 7, generation: 2, schema }
    expect(parseProposalRequest(proposalRequestParam(target))).toEqual(target)
    const href = new URL(proposalChatHref("ws", target, "Tighten it"), "https://x.test")
    expect(Object.fromEntries(href.searchParams)).toEqual({ workspace_id: "ws", propose: `page_1-a.7.2.${schema}`, prompt: "Tighten it", draft: "1" })
    // The mandate names the Doc's own schema, never a fixed one (#5233).
    expect(proposalIntent(target, "sub-1")).toEqual({ submission_id: "sub-1", page_id: "page_1-a", schema, rev: 7, generation: 2 })
  })

  it("reads a link from before #5233 as the schema-4 target it meant", () => {
    expect(parseProposalRequest("page.7.2")).toEqual({ pageId: "page", rev: 7, generation: 2, schema: 4 })
  })

  it.each(["", "page", "page.1", "page.1.2.3", "page.1.2.6", "page.1.2.", "pa ge.1.2", "page.-1.2", "page.1.x", "a".repeat(129) + ".1.2", "page.9999999999999999.0"])("refuses %j", value => {
    expect(parseProposalRequest(value)).toBeNull()
  })

  it("offers proposals exactly where the capability table does", () => {
    for (const schema of [1, 2, 3, 4, 5, 6]) expect(proposalSchema(schema) !== undefined).toBe(schemaAllows(schema, "propose"))
    expect(schemaAllows(CREATE_SCHEMA, "propose") && schemaAllows(CREATE_SCHEMA, "accept") && schemaAllows(CREATE_SCHEMA, "draft")).toBe(true)
  })
})
