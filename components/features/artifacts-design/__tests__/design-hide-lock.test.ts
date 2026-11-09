import { describe, expect, it } from "vitest"
import { designHiddenIds, designLockedIds, type DesignNode } from "../design-document"

const node = (id: string, extra: Partial<DesignNode> = {}): DesignNode => ({ id, type: "rect", x: 0, y: 0, width: 10, height: 10, fill: "#000", ...extra })

describe("hidden and locked layers", () => {
 it("treats a missing flag as shown and unlocked, as in every older document", () => {
  expect(designHiddenIds([node("a"), node("b", { hidden: false })])).toEqual(new Set())
  expect(designLockedIds([node("a"), node("b", { locked: false })])).toEqual(new Set())
 })
 it("hides or locks everything inside a hidden or locked screen, and survives a parent cycle", () => {
  const nodes = [node("s", { type: "frame", hidden: true, locked: true }), node("a", { parentId: "s" }), node("b"), node("x", { parentId: "y" }), node("y", { parentId: "x", locked: true })]
  expect(designHiddenIds(nodes)).toEqual(new Set(["s", "a"]))
  expect(designLockedIds(nodes)).toEqual(new Set(["s", "a", "x", "y"]))
 })
})
