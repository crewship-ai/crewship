import { describe, it, expect } from "vitest"
import { thoughtForLabel, thinkingLiveLabel } from "@/components/ai-elements/reasoning"

// The collapsed reasoning header is the single most-seen piece of thinking UX.
// It must read like Claude.ai's: a live "Thinking… Ns" while streaming and a
// grammatical "Thought for N seconds" once done ("1 seconds" is not a thing).
describe("reasoning header labels", () => {
  it("pluralizes correctly", () => {
    expect(thoughtForLabel(1)).toBe("Thought for 1 second")
    expect(thoughtForLabel(2)).toBe("Thought for 2 seconds")
    expect(thoughtForLabel(12)).toBe("Thought for 12 seconds")
  })

  it("formats minutes for long reasoning passes", () => {
    expect(thoughtForLabel(60)).toBe("Thought for 1m 0s")
    expect(thoughtForLabel(95)).toBe("Thought for 1m 35s")
  })

  it("falls back when duration is unknown", () => {
    expect(thoughtForLabel(undefined)).toBe("Thought for a few seconds")
  })

  it("live label shows elapsed seconds once at least 1s has passed", () => {
    expect(thinkingLiveLabel(0)).toBe("Thinking…")
    expect(thinkingLiveLabel(1)).toBe("Thinking… 1s")
    expect(thinkingLiveLabel(42)).toBe("Thinking… 42s")
    expect(thinkingLiveLabel(75)).toBe("Thinking… 1m 15s")
  })
})
