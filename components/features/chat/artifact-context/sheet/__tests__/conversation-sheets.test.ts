import { describe, it, expect } from "vitest"
import { rememberConversationSheet, conversationSheets, forgetConversationSheet } from "../conversation-sheets"
import { installMemoryStorage } from "./memory-storage"

const store = installMemoryStorage()

const scope = { userId: "u1", workspaceId: "ws1", chatId: "c1" }
describe("Sheets remembered for a conversation in this browser", () => {
  it("keeps only page ids, newest first, for this person, workspace and conversation", () => {
    rememberConversationSheet(scope, "pg_1"); rememberConversationSheet(scope, "pg_2"); rememberConversationSheet(scope, "pg_1")
    expect(conversationSheets(scope)).toEqual(["pg_1", "pg_2"])
    expect(conversationSheets({ ...scope, userId: "u2" })).toEqual([])
    expect(conversationSheets({ ...scope, chatId: "c2" })).toEqual([])
    expect([...store.values()].join("")).not.toMatch(/name|Quarterly/)
  })
  it("ignores what it cannot read and forgets on request", () => {
    store.set("crewship.chat-sheets:v1:u1:ws1:c1", "{not json")
    expect(conversationSheets(scope)).toEqual([])
    rememberConversationSheet(scope, "pg_1"); forgetConversationSheet(scope, "pg_1")
    expect(conversationSheets(scope)).toEqual([])
  })
  it("keeps at most ten", () => {
    for (let i = 0; i < 12; i++) rememberConversationSheet(scope, `pg_${i}`)
    expect(conversationSheets(scope)).toHaveLength(10)
  })
})
