import { describe, it, expect } from "vitest"

import { minZoomForGraph } from "@/lib/trace/zoom-floor"

// The zoom floor was a constant, 0.3. That is a reasonable floor for a
// fourteen-step routine and absurd for a four-step one: 0.3 of a
// 200px node is 60px, so a small graph could be shrunk to a smudge in
// an empty box and the reader had no way to know they had done it.
//
// A floor should say something about the graph, not about a number
// somebody picked. The rule here is "you cannot zoom out past seeing
// all of it, plus a little": the whole point of zooming out is to
// survey the graph, and there is nothing to survey beyond its own
// bounds.

const pane = { width: 600, height: 580 }

describe("minZoomForGraph", () => {
  it("stops a small graph shrinking below roughly its own fit", () => {
    // Four steps: ~200 wide, ~500 tall. It already fits at 1:1, so
    // there is nothing to gain from zooming out at all.
    const floor = minZoomForGraph({ width: 200, height: 500 }, pane)
    expect(floor).toBeGreaterThan(0.8)
  })

  it("lets a tall graph zoom out far enough to be surveyed", () => {
    // Fourteen steps: ~470 x 1900. Fitting it needs ~0.3, so the floor
    // has to allow that or the reader can never see the whole thing.
    const floor = minZoomForGraph({ width: 470, height: 1900 }, pane)
    expect(floor).toBeLessThan(0.32)
    expect(floor).toBeGreaterThan(0.15)
  })

  it("never exceeds 1 — zooming out to more than life size is not zooming out", () => {
    expect(minZoomForGraph({ width: 50, height: 50 }, pane)).toBeLessThanOrEqual(1)
  })

  it("keeps an enormous graph surveyable rather than pinning it", () => {
    // A 200-step routine. The floor must not be so high that the whole
    // graph can never be on screen at once.
    const floor = minZoomForGraph({ width: 900, height: 26000 }, pane)
    expect(floor).toBeLessThan(0.05)
    expect(floor).toBeGreaterThan(0)
  })

  it("falls back to a sane floor for a degenerate graph or pane", () => {
    expect(minZoomForGraph({ width: 0, height: 0 }, pane)).toBeGreaterThan(0)
    expect(minZoomForGraph({ width: 200, height: 500 }, { width: 0, height: 0 })).toBeGreaterThan(0)
  })
})
