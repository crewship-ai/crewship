import { describe, it, expect } from "vitest"

import { paletteFilter } from "@/lib/palette-filter"

// cmdk's default matcher is a subsequence scorer: every letter of the query
// has to appear in order, but not together. With nine groups loaded that
// turns "gmail" into a match for "Rewrite the HarborliGht reADMe so a
// newcomer can folLow it", which outranked the actual Gmail integration.
// A substring matcher is stricter and, for a list you are typing a known
// name into, better.

describe("paletteFilter", () => {
  it("rejects a subsequence that is not a substring", () => {
    expect(paletteFilter("issue Rewrite the Harborlight README so a newcomer can follow it", "gmail")).toBe(0)
  })

  it("keeps a real substring match", () => {
    expect(paletteFilter("integration Composio: gmail · Full", "gmail")).toBeGreaterThan(0)
  })

  it("ranks a prefix above a word start, and a word start above a mid-word hit", () => {
    const prefix = paletteFilter("classify-ticket", "classify")
    const wordStart = paletteFilter("routine Classify support ticket", "classify")
    const midWord = paletteFilter("routine reclassifying", "classify")
    expect(prefix).toBeGreaterThan(wordStart)
    expect(wordStart).toBeGreaterThan(midWord)
    expect(midWord).toBeGreaterThan(0)
  })

  it("matches on keywords too, so an alias still finds the row", () => {
    expect(paletteFilter("Crews", "roster", ["agents", "roster", "canvas"])).toBeGreaterThan(0)
  })

  it("ignores case and surrounding whitespace", () => {
    expect(paletteFilter("routine Morning briefing", "  MORNING ")).toBeGreaterThan(0)
  })

  it("shows everything when nothing has been typed", () => {
    expect(paletteFilter("anything at all", "")).toBeGreaterThan(0)
  })

  it("scores a keyword hit below a hit in the row's own name", () => {
    // "agents" is a keyword on Crews and the name of nothing, so a row
    // actually called "Agents" should still win if one exists.
    const inName = paletteFilter("Agents", "agents")
    const inKeyword = paletteFilter("Crews", "agents", ["agents"])
    expect(inName).toBeGreaterThan(inKeyword)
  })
})
