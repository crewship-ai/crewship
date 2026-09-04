import { describe, it, expect } from "vitest"
import { crewHeaderLinks, crewSpokesperson } from "@/components/features/crews/crew-links"

const crew = { id: "crew_1", slug: "engineering" }
const alex = { slug: "alex", agent_role: "LEAD", name: "Alex" }
const sam = { slug: "sam", agent_role: "AGENT", name: "Sam" }

describe("crewSpokesperson", () => {
  it("prefers the lead, falls back to the first agent, none for an empty crew", () => {
    expect(crewSpokesperson([sam, alex])).toBe(alex)
    expect(crewSpokesperson([sam])).toBe(sam)
    expect(crewSpokesperson([])).toBeNull()
  })
})

describe("crewHeaderLinks", () => {
  it("carries every link the contract asks of a crew, scoped to it", () => {
    const links = crewHeaderLinks({ crew, agents: [sam, alex], counts: { issues: 6, routines: 2, credentials: 2 } })
    expect(links.map((l) => [l.id, l.href])).toEqual([
      ["chat", "/chat/alex"],
      ["issues", "/issues?crew=engineering"],
      ["routines", "/routines?crew=engineering"],
      ["pages", "/pages?crew=engineering"],
      ["journal", "/journal?crew=engineering"],
      ["spend", "/paymaster?crew=crew_1"],
      ["credentials", "/credentials?crew=engineering"],
    ])
    expect(links.find((l) => l.id === "chat")?.title).toBe("Chat with Alex")
    expect(links.find((l) => l.id === "issues")?.count).toBe("6")
    expect(links.find((l) => l.id === "pages")?.count).toBeUndefined()
  })

  it("drops Chat for a crew with nobody to talk to, and never shows a count it does not have", () => {
    const links = crewHeaderLinks({ crew, agents: [] })
    expect(links.map((l) => l.id)).not.toContain("chat")
    expect(links.every((l) => l.count === undefined)).toBe(true)
  })
})
