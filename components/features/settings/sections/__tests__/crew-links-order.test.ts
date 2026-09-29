import { describe, it, expect } from "vitest"

import { partitionOthers, linkSummary } from "../connections-section"

const crew = (id: string, name = id) => ({ id, name, slug: id })
const link = (from: string, to: string, direction = "outbound") => ({ id: `${from}-${to}`, from_crew_id: from, to_crew_id: to, direction, status: "active" })

// From one crew's point of view, the crews it is linked to come first — the
// nine identical "Not linked" rows used to bury the one link that exists.
describe("partitionOthers", () => {
  it("puts linked crews first, each group alphabetical, and leaves the selected crew out", () => {
    const crews = [crew("a", "Ops"), crew("b", "Coolify"), crew("c", "Quality"), crew("d", "Engineering")]
    const out = partitionOthers(crews, "a", [link("a", "c"), link("d", "a", "bidirectional")])
    expect(out.linked.map((c) => c.name)).toEqual(["Engineering", "Quality"])
    expect(out.unlinked.map((c) => c.name)).toEqual(["Coolify"])
  })
})

describe("linkSummary", () => {
  it("counts links and the crews that have none", () => {
    const crews = [crew("a"), crew("b"), crew("c"), crew("d")]
    expect(linkSummary(crews, [link("a", "b"), link("b", "c", "bidirectional")])).toEqual({ links: 2, isolated: 1 })
    expect(linkSummary(crews, [])).toEqual({ links: 0, isolated: 4 })
  })
})
