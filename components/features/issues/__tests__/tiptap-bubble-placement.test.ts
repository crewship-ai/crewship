import { describe, it, expect } from "vitest"
import { placeBubbleMenu } from "../tiptap-editor"

/**
 * The selection bubble is positioned by hand, in container coordinates, and
 * rendered with `translateX(-50%)`. Both facts were load-bearing and both were
 * missing a rule:
 *
 *  · Reported from a screenshot of New project: selecting the first word of a
 *    line drew the bubble half outside the editor, and the dialog clipped the
 *    half that was outside.
 *  · Selecting on the first line drew it on top of the toolbar it duplicates.
 *
 * Container-relative numbers throughout: `container.left`/`top` are subtracted,
 * so a container at (400, 300) and one at (0, 0) must place identically.
 */
const CONTAINER = { top: 300, left: 400, width: 800 }
const MENU_W = 190
const HALF = MENU_W / 2

/** A one-line selection from x1 to x2 with its top at y. */
function selection(x1: number, x2: number, y: number, height = 20) {
  const rect = (l: number, r: number) => ({ top: y, bottom: y + height, left: l, right: r })
  return { selection: rect(x1, x1), selectionEnd: rect(x2, x2) }
}

describe("placeBubbleMenu", () => {
  it("centres on the selection when there is room on both sides", () => {
    const { left } = placeBubbleMenu({
      ...selection(800, 900, 600),
      container: CONTAINER,
      menuWidth: MENU_W,
    })
    // Midpoint 850 in page coords → 450 inside the container.
    expect(left).toBe(450)
  })

  it("keeps a selection at the left edge fully inside the container", () => {
    const { left } = placeBubbleMenu({
      ...selection(405, 425, 600),
      container: CONTAINER,
      menuWidth: MENU_W,
    })
    // Uncentred this would be 15, putting the left 80px of the menu outside.
    expect(left).toBeGreaterThanOrEqual(HALF)
    expect(left - HALF).toBeGreaterThanOrEqual(0)
  })

  it("keeps a selection at the right edge fully inside the container", () => {
    const { left } = placeBubbleMenu({
      ...selection(1180, 1198, 600),
      container: CONTAINER,
      menuWidth: MENU_W,
    })
    expect(left + HALF).toBeLessThanOrEqual(CONTAINER.width)
  })

  it("still returns a point inside a container narrower than the menu", () => {
    const { left } = placeBubbleMenu({
      ...selection(410, 440, 600),
      container: { ...CONTAINER, width: 120 },
      menuWidth: MENU_W,
    })
    // No placement can fit; what it must not do is return a lower bound above
    // its own upper bound, which Math.min/Math.max in the wrong order does.
    expect(Number.isFinite(left)).toBe(true)
    expect(left).toBeGreaterThan(0)
  })

  it("sits above the selection when the line has room above it", () => {
    const { top } = placeBubbleMenu({
      ...selection(800, 900, 600),
      container: CONTAINER,
      menuWidth: MENU_W,
    })
    // 600 - 300 (container) - 44 (bubble height + gap).
    expect(top).toBe(256)
  })

  it("flips below the selection on the first line rather than over the toolbar", () => {
    const { top } = placeBubbleMenu({
      ...selection(800, 900, 305),
      container: CONTAINER,
      menuWidth: MENU_W,
    })
    // Above would be 305 - 300 - 44 = -39. Below is bottom (325) - 300 + 8.
    expect(top).toBe(33)
  })

  it("never returns a negative coordinate", () => {
    const { top, left } = placeBubbleMenu({
      ...selection(400, 401, 300),
      container: CONTAINER,
      menuWidth: MENU_W,
    })
    expect(top).toBeGreaterThanOrEqual(0)
    expect(left).toBeGreaterThanOrEqual(0)
  })
})
