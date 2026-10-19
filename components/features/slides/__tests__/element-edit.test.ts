import { describe, expect, it } from "vitest"
import fixture from "@/testdata/slides-schema-v1.json"
import type { SlideDeck } from "@/lib/slides/model"
import { addElement, canReorder, nudgeElements, removeElements, reorderElements, resizeElement } from "../element-edit"
import { isNativeSlideDeck } from "../native-model"

const sample = () => structuredClone(fixture.deck) as SlideDeck
const withFour = () => {
  let deck = sample()
  for (const type of ["text", "shape", "table"] as const) deck = addElement(deck, "slide_b", type).deck
  return deck // slide_b: title_b, shape_b, then three new elements
}
const ids = (deck: SlideDeck) => deck.slides[0].elements.map(element => element.id)

describe("draft edits", () => {
  it("nudges inside the frame and never mutates the input", () => {
    const deck = sample(), before = structuredClone(deck)
    const moved = nudgeElements(deck, "slide_b", ["shape_b"], -500, 10)
    expect(moved.slides[0].elements[1]).toMatchObject({ x: 0, y: 150 })
    expect(nudgeElements(deck, "slide_b", ["shape_b"], 5000, 5000).slides[0].elements[1]).toMatchObject({ x: 860, y: 440 })
    expect(deck).toEqual(before)
    const edge = nudgeElements(deck, "slide_b", ["title_b", "shape_b"], 5000, 0)
    expect(edge.slides[0].elements.map(element => element.x)).toEqual([80, 80])
    const group = nudgeElements(deck, "slide_b", ["title_b", "shape_b"], 1, 1)
    expect(group.slides[0].elements.map(element => [element.x, element.y])).toEqual([[25, 25], [25, 141]])
    expect(isNativeSlideDeck(group)).toBe(true)
  })
  it("resizes within bounds and keeps at least one pixel", () => {
    const deck = sample()
    expect(resizeElement(deck, "slide_b", "shape_b", { x: 10, y: 10, width: 0, height: 99999 }).slides[0].elements[1]).toMatchObject({ x: 10, y: 0, width: 1, height: 540 })
    expect(resizeElement(deck, "slide_b", "ghost", { x: 0, y: 0, width: 5, height: 5 })).toEqual(deck)
  })
  it("adds and removes with fresh identities", () => {
    const { deck, id } = addElement(sample(), "slide_b", "text")
    expect(deck.slides[0].elements.at(-1)?.id).toBe(id)
    expect(removeElements(deck, "slide_b", [id, "shape_b"]).slides[0].elements.map(element => element.id)).toEqual(["title_b"])
  })
  it("layers one element forward, backward, to front and to back", () => {
    const deck = withFour(), [a, b, c, d, e] = ids(deck)
    expect(ids(reorderElements(deck, "slide_b", [b], "forward"))).toEqual([a, c, b, d, e])
    expect(ids(reorderElements(deck, "slide_b", [c], "backward"))).toEqual([a, c, b, d, e])
    expect(ids(reorderElements(deck, "slide_b", [b], "front"))).toEqual([a, c, d, e, b])
    expect(ids(reorderElements(deck, "slide_b", [d], "back"))).toEqual([d, a, b, c, e])
    expect(ids(reorderElements(deck, "slide_b", [e], "forward"))).toEqual([a, b, c, d, e])
  })
  it("layers a selection as a block, keeping its relative order", () => {
    const deck = withFour(), [a, b, c, d, e] = ids(deck)
    expect(ids(reorderElements(deck, "slide_b", [b, d], "forward"))).toEqual([a, c, b, e, d])
    expect(ids(reorderElements(deck, "slide_b", [b, d], "backward"))).toEqual([b, a, d, c, e])
    expect(ids(reorderElements(deck, "slide_b", [d, b], "front"))).toEqual([a, c, e, b, d])
    expect(ids(reorderElements(deck, "slide_b", [d, e], "forward"))).toEqual([a, b, c, d, e])
    expect(canReorder(deck, "slide_b", [d, e], "front")).toBe(false)
    expect(canReorder(deck, "slide_b", [a], "front")).toBe(true)
    expect(canReorder(deck, "slide_b", [], "front")).toBe(false)
  })
})
