import { describe, it, expect } from "vitest"

import {
  MAX_SUGGESTED_PROMPTS,
  MAX_SUGGESTED_PROMPT_LENGTH,
  getSuggestions,
  parseSuggestedPrompts,
} from "@/lib/agent-suggestions"

// PRD chat-as-a-primary-surface, Step 7. The three properties that matter:
// an agent's own list wins, an agent without one behaves EXACTLY as it did
// before the column existed, and the cap is enforced at render — the
// textarea shows counts but the server is the enforcement, so the client
// must never render a ninth chip just because a row predates the check.

describe("parseSuggestedPrompts", () => {
  it("returns an empty list for null, undefined and blank input", () => {
    expect(parseSuggestedPrompts(null)).toEqual([])
    expect(parseSuggestedPrompts(undefined)).toEqual([])
    expect(parseSuggestedPrompts("")).toEqual([])
    expect(parseSuggestedPrompts("   \n\t\n ")).toEqual([])
  })

  it("splits on newlines, trims, and drops blank lines", () => {
    expect(parseSuggestedPrompts("  first  \n\n\t second\t\n   \nthird")).toEqual([
      "first",
      "second",
      "third",
    ])
  })

  it("handles CRLF and bare CR line endings", () => {
    expect(parseSuggestedPrompts("first\r\nsecond\rthird")).toEqual(["first", "second", "third"])
  })

  it("caps at the maximum even when the stored value has more", () => {
    const stored = Array.from({ length: 12 }, (_, i) => `prompt ${i + 1}`).join("\n")
    const parsed = parseSuggestedPrompts(stored)
    expect(parsed).toHaveLength(MAX_SUGGESTED_PROMPTS)
    expect(parsed[MAX_SUGGESTED_PROMPTS - 1]).toBe(`prompt ${MAX_SUGGESTED_PROMPTS}`)
  })
})

describe("getSuggestions", () => {
  it("falls back to the default role pack with no role and no agent list", () => {
    const pack = getSuggestions()
    expect(pack.empty).toEqual([
      "Help me get started",
      "What can you do?",
      "Show me your skills",
      "Run a quick task",
    ])
    expect(pack.followUps).toEqual(["Tell me more", "Give me an example", "What's next?"])
  })

  it("still picks the role pack when the agent has no prompts configured", () => {
    // The hard requirement: an unconfigured agent behaves exactly as it did
    // before the column existed, for every argument shape that means "unset".
    const roleOnly = getSuggestions("research")
    for (const empty of [null, undefined, "", "   \n  "]) {
      expect(getSuggestions("research", empty)).toEqual(roleOnly)
    }
    expect(roleOnly.empty[0]).toBe("Summarize the top 5 sources")
  })

  it("prefers the agent's own prompts over the role pack", () => {
    const pack = getSuggestions("research", "What changed in the spec?\nWho reviewed it?")
    expect(pack.empty).toEqual(["What changed in the spec?", "Who reviewed it?"])
  })

  it("prefers the agent's own prompts even when the role is unknown", () => {
    const pack = getSuggestions("wharf-master", "Any deliveries today?")
    expect(pack.empty).toEqual(["Any deliveries today?"])
  })

  it("leaves the follow-ups on the role pack — the column only replaces the empty-state chips", () => {
    const pack = getSuggestions("engineering", "Only this one")
    expect(pack.followUps).toEqual(["Open a PR", "Add tests", "Run benchmarks"])
  })

  it("renders at most the cap, however many lines the column holds", () => {
    const stored = Array.from({ length: 20 }, (_, i) => `q${i + 1}`).join("\n")
    expect(getSuggestions("research", stored).empty).toHaveLength(MAX_SUGGESTED_PROMPTS)
  })

  it("normalises the role key the same way it always did", () => {
    expect(getSuggestions("Data Analyst").empty[0]).toBe("Explore the latest dataset")
  })

  it("agrees with the server on the caps", () => {
    expect(MAX_SUGGESTED_PROMPTS).toBe(8)
    expect(MAX_SUGGESTED_PROMPT_LENGTH).toBe(120)
  })
})
