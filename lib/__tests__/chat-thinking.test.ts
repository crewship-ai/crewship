import { describe, it, expect } from "vitest"
import { thinkingHeadline } from "../chat-thinking"

describe("thinkingHeadline", () => {
  it("is the first line of an approved summary once finished", () => {
    expect(thinkingHeadline({ visibility: "summary", state: "completed", summary: "Compared two options.\nChose B." })).toBe("Compared two options.")
  })
  it("never lifts redacted, unavailable or still-running text", () => {
    expect(thinkingHeadline({ visibility: "redacted", state: "completed" })).toBeUndefined()
    expect(thinkingHeadline({ visibility: "unavailable", state: "unavailable" })).toBeUndefined()
    expect(thinkingHeadline({ visibility: "summary", state: "running", summary: "partial" })).toBeUndefined()
  })
})
