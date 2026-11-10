import { describe, expect, it } from "vitest"
import type { DesignNode } from "../design-document"
import { groupBox, marqueeHits, moveGroup, resizeGroup, selectionRoots, toggleSelection } from "../design-selection"

const nodes: DesignNode[] = [
 { id: "s", type: "frame", name: "Screen", x: 100, y: 100, width: 400, height: 300, fill: "#fff" },
 { id: "a", type: "rect", name: "A", parentId: "s", x: 10, y: 10, width: 50, height: 20, fill: "#000" },
 { id: "b", type: "rect", name: "B", parentId: "s", x: 110, y: 60, width: 40, height: 40, fill: "#000" },
 { id: "loose", type: "rect", name: "Loose", x: 700, y: 100, width: 10, height: 10, fill: "#000" },
]

describe("design selection", () => {
 it("toggles one layer in or out, keeping the others in order", () => {
  expect(toggleSelection([], "a")).toEqual(["a"])
  expect(toggleSelection(["a"], "b")).toEqual(["a", "b"])
  expect(toggleSelection(["a", "b"], "a")).toEqual(["b"])
 })

 it("selects with a marquee the layers wholly inside it, in canvas coordinates", () => {
  // a sits at (110,110)-(160,130), b at (210,160)-(250,200).
  expect(marqueeHits(nodes, { x: 105, y: 105 }, { x: 260, y: 210 })).toEqual(["a", "b"])
  // Dragged up and left works the same; a box cutting through b leaves it out.
  expect(marqueeHits(nodes, { x: 230, y: 190 }, { x: 105, y: 105 })).toEqual(["a"])
  // A screen is selected only when the marquee holds the whole screen.
  expect(marqueeHits(nodes, { x: 90, y: 90 }, { x: 510, y: 410 })).toEqual(["s", "a", "b"])
  expect(marqueeHits(nodes, { x: 0, y: 0 }, { x: 1, y: 1 })).toEqual([])
 })

 it("acts on a layer once when its screen is selected too", () => {
  expect(selectionRoots(nodes, ["a", "s", "loose"])).toEqual(["s", "loose"])
  expect(selectionRoots(nodes, ["a", "b"])).toEqual(["a", "b"])
 })

 it("moves the whole selection by one offset", () => {
  expect(moveGroup(nodes, ["a", "s", "loose"], 5, -3)).toEqual([
   { id: "s", x: 105, y: 97 },
   { id: "loose", x: 705, y: 97 },
  ])
 })

 it("resizes the selection's bounding box and scales each layer inside it", () => {
  const box = groupBox(nodes, ["a", "b"])!
  expect(box).toEqual({ x: 110, y: 110, width: 140, height: 90 })
  // Twice as wide, the same height, the left edge kept.
  const patches = resizeGroup(nodes, ["a", "b"], box, { x: 110, y: 110, width: 280, height: 90 })
  expect(patches).toEqual([
   { id: "a", x: 10, y: 10, width: 100, height: 20 },
   { id: "b", x: 210, y: 60, width: 80, height: 40 },
  ])
 })
})
