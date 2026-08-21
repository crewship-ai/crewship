import { describe, it, expect } from "vitest"
import {
  SAIL_PATH,
  MARK_SAILS,
  MARK_GEOMETRY,
  splitSubpaths,
} from "@/lib/brand-mark"

// The animated mark draws each of the logo's three sails on its own so they
// can move independently. Lifting a subpath out verbatim is only safe when
// it starts with an absolute `M` — two of the mark's three start with a
// relative `m`, chained to wherever the previous sail ended. Get that wrong
// and the sails silently pile up at the origin, which renders as a mark
// that is subtly not the logo. Hence the arithmetic below.

describe("splitSubpaths", () => {
  it("returns nothing for an empty path", () => {
    expect(splitSubpaths("")).toEqual([])
    expect(splitSubpaths("   ")).toEqual([])
  })

  it("leaves a single-subpath path as one piece", () => {
    const parts = splitSubpaths("M10 10 L20 20 Z")
    expect(parts).toHaveLength(1)
    expect(parts[0]).toBe("M10 10 L20 20 Z")
  })

  const cases: {
    name: string
    d: string
    starts: [number, number][]
  }[] = [
    {
      name: "relative moveto chains off the previous subpath's endpoint",
      d: "M10 10 L30 40 m5 5 L60 60",
      // first ends at (30,40); m5 5 → (35,45)
      starts: [[10, 10], [35, 45]],
    },
    {
      name: "absolute moveto ignores the previous endpoint",
      d: "M10 10 L30 40 M5 5 L60 60",
      starts: [[10, 10], [5, 5]],
    },
    {
      name: "closepath returns the current point to the subpath start",
      d: "M10 10 L30 40 Z m5 5",
      // z snaps back to (10,10), so m5 5 → (15,15), not (35,45)
      starts: [[10, 10], [15, 15]],
    },
    {
      name: "horizontal and vertical linetos move only one axis",
      d: "M10 10 h20 v30 m1 1",
      // h20 → (30,10); v30 → (30,40); m1 1 → (31,41)
      starts: [[10, 10], [31, 41]],
    },
    {
      name: "curve endpoints are the last pair, not the control points",
      d: "M0 0 c10 10 20 20 30 30 m1 1",
      starts: [[0, 0], [31, 31]],
    },
    {
      name: "arc endpoints are the last pair of seven",
      d: "M0 0 a5 5 0 0 1 10 20 m1 1",
      starts: [[0, 0], [11, 21]],
    },
    {
      name: "coordinates after a moveto are an implicit lineto",
      d: "M0 0 10 10 20 20 m1 1",
      // the trailing pairs are linetos, so the pen ends at (20,20)
      starts: [[0, 0], [21, 21]],
    },
    {
      name: "implicit lineto after a relative moveto stays relative",
      d: "M0 0 L5 5 m1 1 2 2 m1 1",
      // m1 1 → (6,6); implicit relative lineto 2 2 → (8,8); m1 1 → (9,9)
      starts: [[0, 0], [6, 6], [9, 9]],
    },
    {
      name: "numbers may run together without separators",
      d: "M10.5.5L20 20m-1-1",
      // "10.5" then ".5"; ends at (20,20); m-1-1 → (19,19)
      starts: [[10.5, 0.5], [19, 19]],
    },
  ]

  it.each(cases)("$name", ({ d, starts }) => {
    const parts = splitSubpaths(d)
    expect(parts).toHaveLength(starts.length)
    parts.forEach((part, i) => {
      const m = part.match(/^M(-?[\d.]+) (-?[\d.]+)/)
      expect(m, `part ${i} must begin with an absolute M: ${part}`).not.toBeNull()
      expect(Number(m![1])).toBeCloseTo(starts[i][0], 6)
      expect(Number(m![2])).toBeCloseTo(starts[i][1], 6)
    })
  })

  it("rewrites only the moveto and leaves the rest byte-for-byte", () => {
    const [, second] = splitSubpaths("M10 10 L30 40 m5 5 C1 2 3 4 5 6 Z")
    expect(second).toBe("M35 45 C1 2 3 4 5 6 Z")
  })

  it("stops at truncated data instead of looping", () => {
    // A trailing command with too few arguments used to be a hang risk.
    expect(() => splitSubpaths("M10 10 L30 40 C1 2 3")).not.toThrow()
    expect(splitSubpaths("M10 10 L30 40 C1 2 3")).toHaveLength(1)
  })
})

describe("the Crewship mark", () => {
  it("is three sails", () => {
    // MARK_GEOMETRY.feet holds one measured pivot per sail. If the mark is
    // ever redrawn with a different number of subpaths those pivots are
    // stale, and the animation would rotate sails about the wrong points —
    // so fail here rather than ship a mark that moves wrong.
    expect(MARK_SAILS).toHaveLength(3)
    expect(MARK_GEOMETRY.feet).toHaveLength(MARK_SAILS.length)
  })

  it("gives every sail an absolute start", () => {
    for (const sail of MARK_SAILS) {
      expect(sail.startsWith("M")).toBe(true)
      expect(sail.slice(1)).not.toMatch(/^[Mm]/)
    }
  })

  it("keeps the sails in the order and place the source path draws them", () => {
    // Left to right across the mark. Pinning the actual coordinates catches
    // a reordered or re-chained split, which a length check would not.
    const starts = MARK_SAILS.map((s) => {
      const m = s.match(/^M(-?[\d.]+) (-?[\d.]+)/)!
      return [Number(m[1]), Number(m[2])]
    })
    expect(starts[0][0]).toBeCloseTo(114.2, 1)
    expect(starts[1][0]).toBeCloseTo(280.8, 1)
    expect(starts[2][0]).toBeCloseTo(443.9, 1)
    expect(starts[0][0]).toBeLessThan(starts[1][0])
    expect(starts[1][0]).toBeLessThan(starts[2][0])
  })

  it("accounts for the whole source path and adds nothing", () => {
    // Every character of SAIL_PATH other than the two rewritten movetos
    // survives into exactly one sail, so the split cannot quietly drop a
    // curve or duplicate one.
    const rejoined = MARK_SAILS.map((s) =>
      s.replace(/^M-?[\d.]+ -?[\d.]+/, "")
    ).join("")
    const original = SAIL_PATH.replace(/^[Mm]-?[\d.]+ ?-?[\d.]+/, "").replace(
      /[Mm]-?[\d.]+ ?-?[\d.]+/g,
      ""
    )
    expect(rejoined.replace(/\s+/g, "")).toBe(original.replace(/\s+/g, ""))
  })

  it("pivots each sail inside the mark's own box", () => {
    for (const foot of MARK_GEOMETRY.feet) {
      expect(foot).toBeGreaterThan(0)
      expect(foot).toBeLessThan(MARK_GEOMETRY.width)
    }
    // Feet run left to right, matching the sail order above.
    expect([...MARK_GEOMETRY.feet]).toEqual([...MARK_GEOMETRY.feet].sort((a, b) => a - b))
  })

  it("splits fast enough to do at module load", () => {
    // The alternative was a generated, checked-in file. That is only worth
    // its staleness risk if parsing is slow, and it is not.
    const started = performance.now()
    for (let i = 0; i < 20; i++) splitSubpaths(SAIL_PATH)
    const each = (performance.now() - started) / 20
    expect(each).toBeLessThan(15)
  })
})
