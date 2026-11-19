import { describe, it, expect } from "vitest"

import {
  combinePair,
  directionsOf,
  duplicateNames,
  splitPair,
  crewMatches,
  crewResources,
  shortImage,
  DEFAULT_CREW_LINK_FILTERS,
  LEVELS,
  describeLinkChange,
  linkRows,
  matrixCrews,
  mostLinked,
  pairLevels,
  parseView,
  partnerCounts,
  planChange,
  planLevels,
  crewFigures,
  levelSentence,
  type Connection,
  type Level,
  type Side,
  type Crew,
  type CrewLinkFilters,
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

describe("duplicateNames", () => {
  it("finds names two crews share, so their slug can be shown", () => {
    expect([...duplicateNames([crew("a", "Sampler"), crew("b", "Sampler"), crew("c", "Ops")])]).toEqual(["Sampler"])
  })
})

describe("crew filters and facts", () => {
  const eng: Crew = { id: "eng", name: "Engineering", slug: "engineering", network_mode: "restricted", _count: { agents: 2 } }
  const col: Crew = { id: "col", name: "Collector", slug: "collector", network_mode: "free", _count: { agents: 0 } }
  const f = (over: Partial<CrewLinkFilters> = {}): CrewLinkFilters => ({ ...DEFAULT_CREW_LINK_FILTERS, ...over })

  it("matches agent names, links, agents and network", () => {
    const agents = [{ id: "a", name: "Riley", slug: "riley", crew_id: "eng" }]
    expect(crewMatches(eng, f({ q: "ril" }), agents)).toBe(true)
    expect(crewMatches(col, f({ q: "ril" }), [])).toBe(false)
    expect(crewMatches(col, f({ agents: "none" }), undefined)).toBe(true)
    expect(crewMatches(eng, f({ agents: "none" }), undefined)).toBe(false)
    expect(crewMatches(col, f({ net: "open" }), undefined)).toBe(true)
    expect(crewMatches(eng, f({ net: "open" }), undefined)).toBe(false)
  })

  it("reads the box and the image the way a person would", () => {
    expect(crewResources({ ...eng, container_memory_mb: 256, container_cpus: 0.25 })).toBe("256 MB · 0.25 CPU")
    expect(crewResources({ ...eng, container_memory_mb: 4096, container_cpus: 2 })).toBe("4 GB · 2 CPU")
    expect(shortImage("mcr.microsoft.com/devcontainers/javascript-node:22@sha256:abc")).toBe("javascript-node:22")
    expect(shortImage("python:3.12-slim")).toBe("python:3.12-slim")
    expect(shortImage("")).toBeNull()
  })
})

// ── The ladder ─────────────────────────────────────────────────────────────

describe("pairLevels", () => {
  it("reads each direction of a stored row as one step", () => {
    const c = conn("eng", "ops", "bidirectional", "read", "read_write")
    expect(pairLevels(c, "eng")).toEqual({ out: "view", in: "deliver", inactive: false })
    expect(pairLevels(c, "ops")).toEqual({ out: "deliver", in: "view", inactive: false })
  })

  it("keeps a one-way link one-way, seen from either end", () => {
    const c = conn("eng", "qa", "unidirectional", "none", "read")
    expect(pairLevels(c, "eng")).toMatchObject({ out: "work", in: "off" })
    expect(pairLevels(c, "qa")).toMatchObject({ out: "off", in: "work" })
  })

  it("is Off both ways without a row, and Work only where the server reports no files", () => {
    expect(pairLevels(undefined, "a")).toEqual({ out: "off", in: "off", inactive: false })
    expect(pairLevels(conn("a", "b", "bidirectional"), "a")).toMatchObject({ out: "work", in: "work" })
  })

  it("shows an inactive row as Off both ways, flagged", () => {
    const c = { ...conn("a", "b", "bidirectional", "read_write", "read_write"), status: "inactive" }
    expect(pairLevels(c, "a")).toEqual({ out: "off", in: "off", inactive: true })
  })
})

// What the server must be asked, written out by hand: moving one ladder.
const FILES = { work: "none", view: "read", deliver: "read_write" } as const
type Step = { pair: string | null; unlink: boolean; files: { side: Side; level: string }[] }

function expected(side: Side, from: Level, to: Level, other: Level): Step {
  const self = side === "out" ? "out" : "in"
  const rest = side === "out" ? "in" : "out"
  const stateWith = (mine: boolean, theirs: boolean) => (mine && theirs ? "both" : mine ? self : theirs ? rest : "none")
  const theirs = other !== "off"
  if (from === to) return { pair: null, unlink: false, files: [] }
  if (to === "off") {
    const left = stateWith(false, theirs)
    return { pair: left, unlink: left === "none", files: [] }
  }
  if (from === "off") return { pair: stateWith(true, theirs), unlink: false, files: [{ side, level: FILES[to] }] }
  return { pair: null, unlink: false, files: [{ side, level: FILES[to] }] }
}

describe("planChange: every move of one ladder", () => {
  const sides: Side[] = ["out", "in"]
  const others: Level[] = ["off", "view"]
  for (const side of sides) {
    for (const other of others) {
      describe(`${side} ladder, the other at ${other}${other === "off" ? " (a move to Off removes the last direction)" : ""}`, () => {
        const cases = LEVELS.flatMap((from) => LEVELS.map((to) => [from, to] as const))
        it.each(cases)("%s → %s", (from, to) => {
          const cur = { out: "off" as Level, in: "off" as Level, [side]: from, [side === "out" ? "in" : "out"]: other } as { out: Level; in: Level }
          expect(planChange(cur, side, to)).toEqual(expected(side, from, to, other))
        })
      })
    }
  }

  it("has 16 moves per direction", () => {
    expect(LEVELS.flatMap((f) => LEVELS.map((t) => [f, t]))).toHaveLength(16)
  })
})

describe("planLevels: the dialog sets both ladders at once", () => {
  it("creates a two-way link with a file level per direction", () => {
    expect(planLevels({ out: "off", in: "off" }, { out: "deliver", in: "work" })).toEqual({
      pair: "both", unlink: false, files: [{ side: "out", level: "read_write" }, { side: "in", level: "none" }],
    })
  })
  it("turns a one-way link around", () => {
    expect(planLevels({ out: "view", in: "off" }, { out: "off", in: "view" })).toEqual({ pair: "in", unlink: false, files: [{ side: "in", level: "read" }] })
  })
  it("asks to unlink when both go Off", () => {
    expect(planLevels({ out: "view", in: "work" }, { out: "off", in: "off" })).toEqual({ pair: "none", unlink: true, files: [] })
  })
  it("does nothing for no change", () => {
    expect(planLevels({ out: "view", in: "off" }, { out: "view", in: "off" })).toEqual({ pair: null, unlink: false, files: [] })
  })
})

describe("lists", () => {
  const crews = ["a", "b", "c", "d"].map((i) => crew(i, i.toUpperCase()))
  const conns = [conn("a", "b", "bidirectional", "read_write", "read"), conn("a", "c", "unidirectional", "none"), conn("d", "a", "unidirectional", "read")]

  it("counts partners and ranks the most linked first", () => {
    expect(Object.fromEntries(partnerCounts(conns))).toEqual({ a: 3, b: 1, c: 1, d: 1 })
    expect(mostLinked(crews, conns, 2).map((c) => c.id)).toEqual(["a", "b"])
  })

  it("makes one row per link, with both levels", () => {
    const rows = linkRows(crews, conns)
    expect(rows.map((r) => [r.a.id, r.b.id, r.ab, r.ba])).toEqual([["a", "b", "deliver", "view"], ["a", "c", "work", "off"], ["d", "a", "view", "off"]])
  })

  it("keeps the matrix to the most linked crews and says how many there were", () => {
    const m = matrixCrews(linkRows(crews, conns), conns, 2)
    expect(m.crews.map((c) => c.id)).toEqual(["a", "b"])
    expect(m.total).toBe(4)
  })

  it("figures per crew: partners, hands work to, receives from", () => {
    expect(crewFigures("a", conns)).toEqual({ partners: 3, out: 2, in: 2 })
    expect(crewFigures("c", conns)).toEqual({ partners: 1, out: 0, in: 1 })
  })

  it("maps the old ?links= to views", () => {
    expect(parseView(null, "none")).toBe("alone")
    expect(parseView(null, "linked")).toBe("crews")
    expect(parseView("matrix", "none")).toBe("matrix")
    expect(parseView("deliver", null)).toBe("deliver")
    expect(parseView("nonsense", null)).toBe("crew")
  })

  it("words each step", () => {
    expect(levelSentence("Support", "Ops", "view")).toBe("Assigns tasks, and reads Ops’s shared files")
    expect(levelSentence("Support", "Ops", "off")).toBe("Support cannot hand work to Ops")
  })
})

describe("describeLinkChange", () => {
  const nameOf = (id: string) => ({ a: "Alpha", b: "Beta" })[id] ?? id
  const e = (action: string, previous: Record<string, unknown> | null, current: Record<string, unknown> | null) => ({ id: "1", action, at: "", actor: null, previous, current })
  const state = (f: string, r: string) => ({ from_crew_id: "a", to_crew_id: "b", forward_file_access: f, reverse_file_access: r })

  it("names creation and removal", () => {
    expect(describeLinkChange(e("crew_link.create", null, state("none", "none")), nameOf)).toBe("Linked Alpha and Beta")
    expect(describeLinkChange(e("crew_link.delete", state("none", "none"), { status: "deleted" }), nameOf)).toBe("Unlinked Alpha and Beta")
  })

  it("names a file level change in ladder words", () => {
    expect(describeLinkChange(e("crew_link.file_access", state("read", "none"), state("read_write", "none")), nameOf)).toBe("Alpha → Beta: View files → Deliver files")
  })
})
