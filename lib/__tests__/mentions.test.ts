import { describe, it, expect } from "vitest"

import {
  MENTION_URL_SCHEME,
  applyMention,
  extractMentionAgentIds,
  findMentionQuery,
  mentionDirectory,
  mentionToken,
  parseMentionUrl,
  type MentionAgent,
} from "@/lib/mentions"

function agent(over: Partial<MentionAgent> = {}): MentionAgent {
  return {
    id: "cmt20ikph011ab4683c02",
    name: "Robin",
    slug: "robin",
    ...over,
  }
}

describe("mentionToken", () => {
  it("writes the id in the destination and the slug in the label", () => {
    expect(mentionToken(agent())).toBe("[@robin](crewship:agent/cmt20ikph011ab4683c02)")
  })

  it("round-trips through the parser", () => {
    const t = mentionToken(agent({ id: "agt_x1", slug: "ada" }))
    expect(extractMentionAgentIds(t)).toEqual(["agt_x1"])
  })

  it("never lets a label break out of the token", () => {
    // A display name is not a slug. If an agent were ever named
    // `x](crewship:agent/agt_admin)(` the token must not become two tokens,
    // and must not point anywhere but the agent it was written for.
    const t = mentionToken(agent({ slug: "x](crewship:agent/agt_admin)(", id: "agt_real" }))
    expect(extractMentionAgentIds(t)).toEqual(["agt_real"])
    expect(t.match(/crewship:agent\//g)).toHaveLength(1)
    expect(t.endsWith("(crewship:agent/agt_real)")).toBe(true)
  })

  it("refuses to write a token for an id it cannot express", () => {
    expect(() => mentionToken(agent({ id: "agt (evil)" }))).toThrow()
  })
})

describe("parseMentionUrl", () => {
  it("accepts the scheme and returns the id", () => {
    expect(parseMentionUrl(`${MENTION_URL_SCHEME}agt_1`)).toBe("agt_1")
  })

  it.each([
    "https://example.com/agt_1",
    "crewship:agent/",
    "crewship:agents/agt_1",
    "crewship:agent/agt 1",
    "crewship:agent/../../etc/passwd",
    "CREWSHIP:AGENT/agt_1",
    "javascript:alert(1)",
    "",
  ])("rejects %s", (url) => {
    expect(parseMentionUrl(url)).toBeNull()
  })
})

describe("extractMentionAgentIds", () => {
  it("finds every mention, de-duplicated, in order", () => {
    const body = `ping [@robin](crewship:agent/a1) and [@ada](crewship:agent/a2), cc [@robin](crewship:agent/a1)`
    expect(extractMentionAgentIds(body)).toEqual(["a1", "a2"])
  })

  it("ignores text that merely looks like a mention", () => {
    expect(extractMentionAgentIds("@robin please look")).toEqual([])
    expect(extractMentionAgentIds("[@robin](https://evil.example/a1)")).toEqual([])
    expect(extractMentionAgentIds("[@robin](crewship:agent/)")).toEqual([])
  })
})

describe("findMentionQuery", () => {
  it("opens on a bare @ at the start of a word", () => {
    expect(findMentionQuery("hey @ro", 7)).toEqual({ start: 4, query: "ro" })
    expect(findMentionQuery("@", 1)).toEqual({ start: 0, query: "" })
  })

  it("does not open inside a word — an email is not a mention", () => {
    expect(findMentionQuery("mail me at pavel@unify.cz", 21)).toBeNull()
  })

  it("closes once the query stops looking like a handle", () => {
    expect(findMentionQuery("hey @ro bin", 11)).toBeNull()
  })

  it("reads from the caret, not the end of the text", () => {
    expect(findMentionQuery("hey @ro trailing words", 7)).toEqual({ start: 4, query: "ro" })
  })
})

describe("applyMention", () => {
  it("replaces the typed handle with the token and leaves a trailing space", () => {
    const out = applyMention("hey @ro", 4, 7, agent({ id: "a1", slug: "robin" }))
    expect(out.text).toBe("hey [@robin](crewship:agent/a1) ")
    expect(out.caret).toBe(out.text.length)
  })

  it("keeps whatever followed the caret", () => {
    const out = applyMention("hey @ro, thanks", 4, 7, agent({ id: "a1", slug: "robin" }))
    expect(out.text).toBe("hey [@robin](crewship:agent/a1) , thanks")
  })
})

describe("mentionDirectory", () => {
  it("keys agents by id", () => {
    const d = mentionDirectory([agent({ id: "a1" }), agent({ id: "a2", name: "Ada" })])
    expect(d.get("a2")?.name).toBe("Ada")
    expect(d.get("nope")).toBeUndefined()
  })
})
