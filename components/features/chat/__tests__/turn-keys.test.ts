import { describe, expect, it } from "vitest"
import type { ChatTurn } from "@/hooks/use-chat"
import { turnRenderKeys } from "../turn-keys"

const at = new Date("2026-11-19T12:00:00Z")
const user = (id: string, text: string): ChatTurn => ({ id, role: "user", parts: [{ id: `${id}-p`, type: "text", content: text, timestamp: at }], isStreaming: false, timestamp: at })
const agent = (id: string): ChatTurn => ({ id, role: "assistant", parts: [{ id: `${id}-p`, type: "text", content: "ok", timestamp: at }], isStreaming: false, timestamp: at })

describe("turnRenderKeys", () => {
  // The optimistic bubble and the stored message are the same bubble: the
  // reload must not remount it (that drew it twice, on top of each other).
  it("keeps a sent message's key when history swaps in the stored copy", () => {
    const live = turnRenderKeys([user("client-uuid", "Hi Jamie"), agent("a1")])
    const stored = turnRenderKeys([user("msg_server_1", "Hi Jamie "), agent("a1")])
    expect(stored).toEqual(live)
  })

  it("gives an edited message a new bubble", () => {
    const [before] = turnRenderKeys([user("u", "Hi")])
    const [after] = turnRenderKeys([user("u", "Hi there")])
    expect(after).not.toBe(before)
  })

  it("keeps two identical messages apart and agent turns on their ids", () => {
    const keys = turnRenderKeys([user("u1", "again"), agent("a1"), user("u2", "again"), agent("a2")])
    expect(new Set(keys).size).toBe(4)
    expect(keys[1]).toBe("a1")
    expect(keys[3]).toBe("a2")
  })
})
