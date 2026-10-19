import { describe, expect, it } from "vitest"

import { crewDeleteSummary } from "../crew-delete-summary"

describe("crewDeleteSummary (the object line of Delete crew)", () => {
  it.each([
    [{ agents: 3, routines: 2, container: "running" }, "3 agents · 2 routines · container running"],
    [{ agents: 1, routines: 1, container: "stopped" }, "1 agent · 1 routine · container stopped"],
    [{ agents: 0, routines: 0, container: null }, "no agents · no routines · no container"],
    // Not yet known is not zero: a count still loading, or one that failed, is left out.
    [{ agents: 3, routines: null, container: "running" }, "3 agents · container running"],
    [{ agents: 2, routines: null, container: undefined }, "2 agents"],
  ])("%j -> %s", (input, line) => {
    expect(crewDeleteSummary(input)).toBe(line)
  })
})
