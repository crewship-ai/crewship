import { describe, it, expect } from "vitest"

import {
  combinePair,
  crewLinkStats,
  directionSentence,
  directionsOf,
  duplicateNames,
  splitPair,
  type Connection,
  type Crew,
} from "../crew-links-model"

const crew = (id: string, name = id): Crew => ({ id, name, slug: id })
const conn = (from: string, to: string, direction: string, fwd?: string, rev?: string): Connection =>
  ({ id: `${from}-${to}`, from_crew_id: from, to_crew_id: to, direction, status: "active", forward_file_access: fwd, reverse_file_access: rev, access_version: 1 }) as Connection

// A link is two independent facts per direction: may A hand work to B, and
// what A may do with B's shared files. Every view reads these directions.
describe("directionsOf", () => {
  it("expands a two-way link into both directions with each side's file level", () => {
    const d = directionsOf([conn("eng", "ops", "bidirectional", "read", "read_write")])
    expect(d.get("eng>ops")?.files).toBe("read")
    expect(d.get("ops>eng")?.files).toBe("read_write")
  })

  it("keeps a one-way link one-way", () => {
    const d = directionsOf([conn("eng", "qa", "unidirectional", "none", "read")])
    expect(d.has("eng>qa")).toBe(true)
    expect(d.has("qa>eng")).toBe(false)
  })

  it("reports unknown file access when the server does not send it", () => {
    expect(directionsOf([conn("a", "b", "bidirectional")]).get("a>b")?.files).toBeNull()
  })
})

describe("pair state from one crew's side", () => {
  it.each([
    [false, false, "none"],
    [true, false, "out"],
    [false, true, "in"],
    [true, true, "both"],
  ] as const)("out=%s in=%s is %s, and splits back", (out, inn, state) => {
    expect(combinePair(out, inn)).toBe(state)
    expect(splitPair(state)).toEqual({ out, in: inn })
  })
})

describe("crewLinkStats", () => {
  it("counts links, directions, deliveries and crews with no link", () => {
    const crews = [crew("a"), crew("b"), crew("c"), crew("d")]
    const s = crewLinkStats(crews, [conn("a", "b", "bidirectional", "read_write", "none"), conn("b", "c", "unidirectional", "read")])
    expect(s).toEqual({ links: 2, directions: 3, delivering: 1, alone: 1 })
  })
})

describe("directionSentence", () => {
  it.each([
    ["none", "Engineering can hand work to Ops"],
    ["read", "Engineering can hand work to Ops and read its shared files"],
    ["read_write", "Engineering can hand work to Ops, read its shared files and deliver into them"],
    [null, "Engineering can hand work to Ops"],
  ] as const)("files %s", (files, text) => {
    expect(directionSentence("Engineering", "Ops", files)).toBe(text)
  })
})

describe("duplicateNames", () => {
  it("finds names two crews share, so their slug can be shown", () => {
    expect([...duplicateNames([crew("a", "Sampler"), crew("b", "Sampler"), crew("c", "Ops")])]).toEqual(["Sampler"])
  })
})
