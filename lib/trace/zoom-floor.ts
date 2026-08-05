// How far out the trace canvas will let you zoom.
//
// This used to be a constant — 0.3 — which is a reasonable floor for a
// fourteen-step routine and absurd for a four-step one: 0.3 of a 200px
// node is 60px, so a small graph could be scrolled down to a smudge in
// an empty box with no indication that was what had happened.
//
// A floor should say something about the graph rather than about a
// number somebody picked. The rule: you cannot zoom out past seeing all
// of it, plus a little. Surveying the graph is the only reason to zoom
// out, and there is nothing to survey beyond its own bounds.

export interface Size {
  width: number
  height: number
}

// Slack past the exact fit. Landing precisely on the fit zoom feels
// like hitting a wall mid-gesture; a little room past it reads as a
// soft stop.
const SLACK = 0.85

// Absolute floors, for the cases where the relative rule cannot help.
// A 200-step routine is 26,000px tall: its fit zoom is 0.02, and
// refusing to go there would mean the whole graph can never be on
// screen at once. HARD_FLOOR keeps that reachable without letting the
// canvas zoom to infinity.
const HARD_FLOOR = 0.02
const FALLBACK = 0.3

/**
 * Lowest zoom worth allowing for this graph in this pane.
 *
 * Returns FALLBACK for a degenerate graph or an unmeasured pane —
 * during first paint the pane has no size, and a floor computed from
 * zero would pin the canvas at 1 and make it feel stuck.
 */
export function minZoomForGraph(graph: Size, pane: Size): number {
  if (
    !Number.isFinite(graph.width) ||
    !Number.isFinite(graph.height) ||
    graph.width <= 0 ||
    graph.height <= 0 ||
    pane.width <= 0 ||
    pane.height <= 0
  ) {
    return FALLBACK
  }
  const fit = Math.min(pane.width / graph.width, pane.height / graph.height)
  // Never above 1: "zoom out" that stops at larger than life size is
  // not zooming out, it is a lock.
  return Math.max(HARD_FLOOR, Math.min(1, fit * SLACK))
}
