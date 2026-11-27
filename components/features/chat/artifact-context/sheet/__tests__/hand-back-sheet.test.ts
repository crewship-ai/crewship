import { describe, it, expect, vi } from "vitest"
import { handBackSheet } from "../hand-back-sheet"

describe("handing the Sheet to the agent before a message that lets it change the Sheet", () => {
  it("lets the message go once the server confirmed the hand-over", async () => {
    expect(await handBackSheet(async () => ({ state: "returned" }))).toEqual({ hold: false, say: expect.stringMatching(/passed to the agent/) })
  })
  it("lets the message go when the person did not hold the Sheet", async () => {
    expect(await handBackSheet(async () => ({ state: "not_held" }))).toEqual({ hold: false, say: null })
  })
  it.each([["unsaved", /not saved/], ["busy", /still working/], ["unconfirmed", /not confirmed/]] as const)("holds the message when it was kept (%s)", async (reason, text) => {
    const result = await handBackSheet(async () => ({ state: "kept", reason }))
    expect(result.hold).toBe(true)
    expect(result.say).toMatch(text)
  })
  it("holds the message when asking failed", async () => {
    expect(await handBackSheet(async () => { throw Error("offline") })).toEqual({ hold: true, say: expect.stringMatching(/not confirmed/) })
  })
  it("holds the message when no answer came in time", async () => {
    vi.useFakeTimers()
    try {
      const pending = handBackSheet(() => new Promise(() => {}))
      await vi.advanceTimersByTimeAsync(12_000)
      expect((await pending).hold).toBe(true)
    } finally { vi.useRealTimers() }
  })
})
