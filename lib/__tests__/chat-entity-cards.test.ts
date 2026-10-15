import { describe, expect, it } from "vitest"
import page from "@/internal/api/testdata/chat-entity-refs/page.json"
import failed from "@/internal/api/testdata/chat-entity-refs/failed-run.json"
import { anchorEntityRefs, type EntityRef } from "../chat-entity-cards"
import { messagesToTurns, type ChatMessage } from "@/hooks/use-chat"
const message = (id: string, role: ChatMessage["role"], runId?: string): ChatMessage => ({ id, role, runId, content: id, timestamp: new Date() })
describe("durable card anchors", () => {
  it("survives history reload and attaches only to the last reply from its run", () => {
    const turns = messagesToTurns([message("trigger-message", "user"), message("reply", "assistant", "entity-run"), message("later", "assistant", "other-run")])
    expect(turns).toHaveLength(3)
    const anchors = anchorEntityRefs(turns, page.refs as EntityRef[])
    expect(anchors.get("reply")?.refs).toHaveLength(2)
    expect(anchors.has("later")).toBe(false)
    expect(anchors.get("reply")?.withoutReply).toBe(false)
  })
  it("anchors a failed run without an answer behind its exact triggering user message", () => {
    const turns = messagesToTurns([message("trigger-message", "user"), message("unrelated", "assistant", "other-run")])
    const anchors = anchorEntityRefs(turns, failed.refs as EntityRef[])
    expect(anchors.get("trigger-message")?.withoutReply).toBe(true)
    expect(anchors.get("trigger-message")?.refs).toHaveLength(2)
    expect(anchors.has("unrelated")).toBe(false)
  })
  it("does not invent an anchor when the triggering message is absent", () => {
    expect(anchorEntityRefs([], failed.refs as EntityRef[]).size).toBe(0)
  })
})
