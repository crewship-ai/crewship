import { describe, it, expect } from "vitest"

import { roundedPolylinePath, midpointOf, type Pt } from "@/lib/trace/edge-path"

// Edges were drawn as a bezier straight from the source handle to the
// target handle, ignoring everything in between. On a layered graph
// that is fine for a one-rank hop and wrong for a skip edge: the
// accounting routine's obdobi → sbirat spans six ranks, and the curve
// swept diagonally across every node in between.
//
// dagre already computes a route that avoids the nodes; it was being
// discarded. These cover turning that route into an SVG path.

const straight: Pt[] = [
  { x: 0, y: 0 },
  { x: 0, y: 100 },
]

const corner: Pt[] = [
  { x: 0, y: 0 },
  { x: 0, y: 100 },
  { x: 100, y: 100 },
]

describe("roundedPolylinePath", () => {
  it("draws a straight run with no corners", () => {
    const d = roundedPolylinePath(straight, 8)
    expect(d).toBe("M 0,0 L 0,100")
    expect(d).not.toContain("Q")
  })

  it("rounds a corner with a quadratic curve", () => {
    const d = roundedPolylinePath(corner, 8)
    expect(d).toContain("Q")
    // Starts at the first point and ends at the last, exactly — the
    // path has to meet the handles it is drawn between.
    expect(d.startsWith("M 0,0")).toBe(true)
    expect(d.trimEnd().endsWith("100,100")).toBe(true)
  })

  it("never cuts a corner deeper than half the shorter segment", () => {
    // Segments are 10 and 10; a radius of 40 would overshoot past the
    // neighbouring corners and produce a self-crossing path.
    const tight: Pt[] = [
      { x: 0, y: 0 },
      { x: 0, y: 10 },
      { x: 10, y: 10 },
    ]
    const d = roundedPolylinePath(tight, 40)
    const nums = d.match(/-?\d+(\.\d+)?/g)!.map(Number)
    // Nothing may leave the 0..10 box the polyline lives in.
    for (const n of nums) {
      expect(n).toBeGreaterThanOrEqual(-0.01)
      expect(n).toBeLessThanOrEqual(10.01)
    }
  })

  it("drops a zero-length segment instead of emitting NaN", () => {
    const dupe: Pt[] = [
      { x: 0, y: 0 },
      { x: 0, y: 0 },
      { x: 0, y: 50 },
    ]
    const d = roundedPolylinePath(dupe, 8)
    expect(d).not.toContain("NaN")
    expect(d.length).toBeGreaterThan(0)
  })

  it("returns an empty path for degenerate input rather than throwing", () => {
    expect(roundedPolylinePath([], 8)).toBe("")
    expect(roundedPolylinePath([{ x: 1, y: 1 }], 8)).toBe("")
  })
})

describe("midpointOf", () => {
  it("returns the middle vertex of an odd-length route", () => {
    expect(midpointOf(corner)).toEqual({ x: 0, y: 100 })
  })

  it("interpolates between the two central vertices of an even route", () => {
    expect(midpointOf(straight)).toEqual({ x: 0, y: 50 })
  })

  it("returns null for degenerate input", () => {
    expect(midpointOf([])).toBeNull()
  })
})
