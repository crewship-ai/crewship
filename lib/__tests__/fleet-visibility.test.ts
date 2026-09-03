import { describe, it, expect } from "vitest"
import { isInternalAgent, visibleFleetAgents } from "@/lib/fleet-visibility"

// docs/ux/audit-fleet.md §2 "Internal leaks": the onboarding guide agent
// (`_crewship-setup-guide`, crew "—") was the first row of the client's fleet
// roster. It lives in a crew of kind "setup" that the crews list deliberately
// never returns, so the roster showed an agent whose crew did not exist.
const crews = [{ id: "crew-eng" }, { id: "crew-ops" }]

describe("isInternalAgent", () => {
  it("hides an underscore-prefixed slug whatever crew it claims", () => {
    expect(isInternalAgent({ slug: "_crewship-setup-guide", crew_id: "crew-eng" }, crews)).toBe(true)
  })
  it("hides an agent whose crew is not one the workspace lists", () => {
    expect(isInternalAgent({ slug: "guide", crew_id: "crew-setup-hidden" }, crews)).toBe(true)
  })
  it("keeps an unassigned agent — no crew is not a hidden crew", () => {
    expect(isInternalAgent({ slug: "drifter", crew_id: null }, crews)).toBe(false)
  })
  it("keeps an ordinary agent in a listed crew", () => {
    expect(isInternalAgent({ slug: "alex", crew_id: "crew-eng" }, crews)).toBe(false)
  })
})

describe("visibleFleetAgents", () => {
  it("drops only the internal rows and keeps order", () => {
    const agents = [
      { slug: "_crewship-setup-guide", crew_id: "crew-x" },
      { slug: "alex", crew_id: "crew-eng" },
      { slug: "drifter", crew_id: null },
      { slug: "ghost", crew_id: "crew-gone" },
    ]
    expect(visibleFleetAgents(agents, crews).map((a) => a.slug)).toEqual(["alex", "drifter"])
  })
  it("returns the same array when nothing is hidden, so memoised callers do not re-render", () => {
    const agents = [{ slug: "alex", crew_id: "crew-eng" }]
    expect(visibleFleetAgents(agents, crews)).toBe(agents)
  })
})
