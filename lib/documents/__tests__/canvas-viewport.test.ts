import { describe, expect, it } from "vitest"
import { fitAll, fitRect, focusRect, moved, nextPlacement, nudged, pan, PLACEMENT_LIMITS, resized, toScreen, toWorld, wheelFactor, zoomAt, ZOOM, type Rect } from "../canvas-viewport"

const view = { x: 100, y: 50, scale: 2 }
const card: Rect = { x: 10, y: 20, w: 320, h: 120 }

describe("canvas viewport", () => {
  it("maps world and screen both ways", () => {
    const p = { x: 37, y: -12 }
    expect(toWorld(view, toScreen(view, p))).toEqual(p)
    expect(toScreen(view, { x: 100, y: 50 })).toEqual({ x: 0, y: 0 })
  })

  it("pans by a screen delta, independent of the world under it", () => {
    expect(pan(view, 20, -10)).toEqual({ x: 90, y: 55, scale: 2 })
  })

  it.each([
    [{ x: 0, y: 0 }, 1.5], [{ x: 640, y: 360 }, 0.5], [{ x: 123, y: 456 }, 1.1],
  ])("zooms around the pointer %o: the world point under it stays put", (pointer, factor) => {
    const before = toWorld(view, pointer), next = zoomAt(view, pointer, factor)
    const after = toWorld(next, pointer)
    expect(after.x).toBeCloseTo(before.x, 9); expect(after.y).toBeCloseTo(before.y, 9)
    expect(next.scale).toBeCloseTo(view.scale * factor, 9)
  })

  it("clamps zoom and keeps the pointer anchored at the limits", () => {
    expect(zoomAt(view, { x: 5, y: 5 }, 100).scale).toBe(ZOOM.max)
    expect(zoomAt(view, { x: 5, y: 5 }, 0.0001).scale).toBe(ZOOM.min)
    const anchored = zoomAt(view, { x: 200, y: 100 }, 100)
    expect(toWorld(anchored, { x: 200, y: 100 }).x).toBeCloseTo(toWorld(view, { x: 200, y: 100 }).x, 9)
  })

  it("turns wheel deltas into a bounded zoom factor in every delta mode", () => {
    expect(wheelFactor(-100)).toBeGreaterThan(1); expect(wheelFactor(100)).toBeLessThan(1)
    expect(wheelFactor(3, 1)).toBeCloseTo(wheelFactor(48), 9)
    expect(wheelFactor(1e9)).toBeCloseTo(Math.exp(-0.4), 9)
  })

  it("fits all placements centred without magnifying small content", () => {
    const size = { width: 1000, height: 600 }
    const fit = fitAll([card, { x: 2000, y: 900, w: 400, h: 100 }], size)
    expect(fit.scale).toBeLessThan(1)
    const tl = toScreen(fit, { x: 10, y: 20 }), br = toScreen(fit, { x: 2400, y: 1000 })
    expect(tl.x).toBeGreaterThanOrEqual(47.99); expect(br.x).toBeLessThanOrEqual(952.01)
    expect((tl.x + br.x) / 2).toBeCloseTo(500, 6); expect((tl.y + br.y) / 2).toBeCloseTo(300, 6)
    expect(fitRect(card, size).scale).toBe(1)
    expect(fitAll([], size)).toEqual({ x: -48, y: -48, scale: 1 })
  })

  it("focuses one block centred and whole", () => {
    const size = { width: 400, height: 300 }
    const focus = focusRect({ x: 640, y: 0, w: 640, h: 120 }, size)
    const left = toScreen(focus, { x: 640, y: 0 }), right = toScreen(focus, { x: 1280, y: 120 })
    expect(left.x).toBeGreaterThanOrEqual(47.99); expect(right.x).toBeLessThanOrEqual(352.01)
    expect((left.x + right.x) / 2).toBeCloseTo(200, 6)
  })
})

describe("placement geometry", () => {
  it("moves by a screen drag converted at the current zoom, size unchanged", () => {
    expect(moved(card, 100, -40, 2)).toEqual({ x: 60, y: 0, w: 320, h: 120 })
    expect(moved(card, 1e12, 0, 1).x).toBe(PLACEMENT_LIMITS.coordinate)
  })

  it.each([
    ["e", 40, 0, { x: 10, y: 20, w: 360, h: 120 }],
    ["w", 40, 0, { x: 50, y: 20, w: 280, h: 120 }],
    ["s", 0, -30, { x: 10, y: 20, w: 320, h: 90 }],
    ["nw", -10, -10, { x: 0, y: 10, w: 330, h: 130 }],
    ["w", 1000, 0, { x: 290, y: 20, w: 40, h: 120 }],
    ["n", 0, 1000, { x: 10, y: 116, w: 320, h: 24 }],
  ] as const)("resizes from %s within the validator's limits, opposite edge fixed", (handle, dx, dy, expected) => {
    expect(resized(card, handle, dx, dy, 1)).toEqual(expected)
  })

  it("nudges with arrow keys and ignores other keys", () => {
    expect(nudged(card, "ArrowRight")).toEqual({ ...card, x: 18 })
    expect(nudged(card, "ArrowUp", true)).toEqual({ ...card, y: 19 })
    expect(nudged(card, "Enter")).toBeNull()
  })

  it("places a new block below everything already on the canvas", () => {
    expect(nextPlacement([])).toEqual({ x: 0, y: 0, w: 360, h: 120 })
    expect(nextPlacement([card, { x: -50, y: 300, w: 100, h: 60 }])).toEqual({ x: -50, y: 392, w: 360, h: 120 })
  })
})
