import * as React from "react"
import { afterEach, describe, expect, it, vi } from "vitest"
import { cleanup, fireEvent, render, screen, within } from "@testing-library/react"
import fixture from "@/testdata/slides-schema-v1.json"
import { SLIDES_MAX_TABLE_CELLS, SLIDES_MAX_TABLE_CELLS_PER_SLIDE, type SlideDeck, type SlideElement } from "@/lib/slides/model"
import { insertSlide, isNativeSlideDeck, moveSlide, orderedSlides } from "../native-model"
import { SlidesEditor } from "../slides-editor"
import { SlideCanvas } from "../slide-canvas"
import { SlidePresent } from "../slide-present"

const sample = () => structuredClone(fixture.deck) as SlideDeck
afterEach(() => { cleanup(); vi.restoreAllMocks() })

function Editor({ readOnly = false }: { readOnly?: boolean }) {
  const [deck, setDeck] = React.useState(sample)
  return <><SlidesEditor deck={deck} onChange={setDeck} readOnly={readOnly} /><output data-testid="deck">{JSON.stringify(deck)}</output></>
}
const currentDeck = () => JSON.parse(screen.getByTestId("deck").textContent!) as SlideDeck

describe("native Slides boundary", () => {
  it("enforces shared aggregate cell budgets including empty cells", () => {
    const deck = sample(), table = deck.slides[1].elements[0]
    if (table.type !== "table") throw new Error("fixture table missing")
    const makeTable = (slide: number, index: number, cells: number): SlideElement => ({ ...structuredClone(table), id: `table_${slide}_${index}`, table: { ...table.table, cells: [Array.from({ length: cells }, () => "")] } })
    const slides = Array.from({ length: SLIDES_MAX_TABLE_CELLS / SLIDES_MAX_TABLE_CELLS_PER_SLIDE }, (_, slide) => ({ id: `slide_${slide}`, notes: "", elements: Array.from({ length: SLIDES_MAX_TABLE_CELLS_PER_SLIDE / 20 }, (_, index) => makeTable(slide, index, 20)) }))
    deck.slides = slides; deck.slide_order = slides.map(slide => slide.id)
    expect(isNativeSlideDeck(deck)).toBe(true)
    const perSlide = structuredClone(deck); perSlide.slides[0].elements.push(makeTable(0, 999, 1))
    expect(isNativeSlideDeck(perSlide)).toBe(false)
    const total = structuredClone(deck); total.slides.push({ id: "slide_extra", notes: "", elements: [makeTable(999, 0, 1)] }); total.slide_order.push("slide_extra")
    expect(isNativeSlideDeck(total)).toBe(false)
  })
  it.each(["alignment", "shape"])("rejects coerced enum values: %s", field => {
    const deck = sample()
    if (field === "alignment") {
      const element = deck.slides[0].elements[0]
      if (element.type === "text") Object.assign(element.text, { align: ["left"] })
    } else {
      const element = deck.slides[0].elements[1]
      if (element.type === "shape") Object.assign(element.shape, { kind: ["ellipse"] })
    }
    expect(isNativeSlideDeck(deck)).toBe(false)
  })
  it("consumes the canonical fixture in slide_order, independent of storage order", () => {
    const deck = sample(); deck.slides.reverse()
    expect(isNativeSlideDeck(deck)).toBe(true)
    expect(orderedSlides(deck).map(slide => slide.id)).toEqual(deck.slide_order)
  })
  it.each([
    (deck: SlideDeck) => Object.assign(deck, { html: "<script/>" }),
    (deck: SlideDeck) => Object.assign(deck, { schema_version: 99 }),
    (deck: SlideDeck) => { deck.slides[0].elements[0].width = 100000 },
    (deck: SlideDeck) => { deck.slide_order[1] = deck.slide_order[0] },
    (deck: SlideDeck) => { deck.slides[0].notes = "\ud800" },
    (deck: SlideDeck) => Object.assign(deck.slides[0].elements[0], { type: "image", url: "https://example.test" }),
  ])("fails closed for unexpected or invalid props", mutate => {
    const deck = sample(); mutate(deck)
    render(<SlidesEditor deck={deck} onChange={vi.fn()} />)
    expect(screen.getByRole("alert").textContent).toContain("unavailable")
    expect(screen.queryByRole("button")).toBeNull()
  })
  it("duplicates every identity, preserves source data and keeps reorder identities", () => {
    const deck = sample(), before = structuredClone(deck), sourceId = deck.slide_order[0]
    const duplicated = insertSlide(deck, sourceId, true)
    const source = deck.slides.find(slide => slide.id === sourceId)!, copy = duplicated.deck.slides.find(slide => slide.id === duplicated.slideId)!
    const originalIds = new Set(deck.slides.flatMap(slide => [slide.id, ...slide.elements.map(element => element.id)]))
    expect(originalIds.has(copy.id)).toBe(false)
    expect(copy.elements.every(element => !originalIds.has(element.id))).toBe(true)
    expect(copy.elements.map(({ id: _id, ...element }) => element)).toEqual(source.elements.map(({ id: _id, ...element }) => element))
    expect(isNativeSlideDeck(duplicated.deck)).toBe(true)
    const moved = moveSlide(duplicated.deck, copy.id, -1)
    expect(moved.slides).toEqual(duplicated.deck.slides)
    expect(moved.slide_order[0]).toBe(copy.id)
    expect(deck).toEqual(before)
  })
})

describe("native editor interactions", () => {
  it("edits valid text, notes and geometry but retains the draft on invalid bounds", () => {
    render(<Editor />)
    fireEvent.click(screen.getByRole("button", { name: "Select text 1" }))
    fireEvent.change(screen.getByLabelText("Text", { selector: "textarea" }), { target: { value: "Edited Czech: příliš 🐎 <script>literal</script>" } })
    fireEvent.change(screen.getByLabelText("Speaker notes"), { target: { value: "Private speaker notes" } })
    fireEvent.change(screen.getByLabelText("x", { selector: "input" }), { target: { value: "30" } })
    const before = currentDeck()
    expect(before.slides.find(slide => slide.id === before.slide_order[0])?.notes).toBe("Private speaker notes")
    expect(before.slides.find(slide => slide.id === before.slide_order[0])?.elements[0].x).toBe(30)
    fireEvent.change(screen.getByLabelText("width", { selector: "input" }), { target: { value: "10000" } })
    expect(currentDeck()).toEqual(before)
    expect(screen.getByRole("alert").textContent).toContain("previous draft is retained")
    expect(document.querySelector("script")).toBeNull()
    expect(screen.getByText(/^Changes in this preview/).textContent).toContain("not saved")
  })
  it("creates, duplicates, moves and deletes slides without aliasing IDs", () => {
    render(<Editor />)
    fireEvent.click(screen.getByRole("button", { name: "Duplicate slide" }))
    expect(currentDeck().slides).toHaveLength(3)
    fireEvent.click(screen.getByRole("button", { name: "Move slide earlier" }))
    const afterMove = currentDeck(); expect(afterMove.slide_order[0]).not.toBe(sample().slide_order[0])
    fireEvent.click(screen.getByRole("button", { name: "Delete slide" }))
    expect(currentDeck().slides).toHaveLength(2)
    fireEvent.click(screen.getByRole("button", { name: "New slide" }))
    fireEvent.click(screen.getByRole("button", { name: "Add table" }))
    fireEvent.change(screen.getByLabelText("Cell 1, 1"), { target: { value: "New heading" } })
    expect(isNativeSlideDeck(currentDeck())).toBe(true)
  })
  it("allows presentation but no mutations in readonly mode", () => {
    render(<Editor readOnly />)
    expect(screen.getByRole("button", { name: "New slide" }).hasAttribute("disabled")).toBe(true)
    expect(screen.getByLabelText("Speaker notes").hasAttribute("disabled")).toBe(true)
    fireEvent.click(screen.getByRole("button", { name: "Present" }))
    expect(screen.getByRole("dialog")).toBeTruthy()
  })
  it("keeps deletion of an externally removed selection explicit", () => {
    const deck = sample(), { rerender } = render(<SlidesEditor deck={deck} onChange={vi.fn()} />)
    const removed = structuredClone(deck); removed.slides = removed.slides.filter(slide => slide.id !== deck.slide_order[0]); removed.slide_order = removed.slide_order.slice(1)
    rerender(<SlidesEditor deck={removed} onChange={vi.fn()} />)
    expect(screen.getByRole("alert").textContent).toContain("no longer available")
  })
})

describe("canvas and Present", () => {
  it("clips the native frame and reports measurable overflow", () => {
    vi.spyOn(HTMLElement.prototype, "scrollHeight", "get").mockReturnValue(100)
    vi.spyOn(HTMLElement.prototype, "clientHeight", "get").mockReturnValue(20)
    const report = vi.fn(); render(<SlideCanvas deck={sample()} slideId={sample().slide_order[0]} onOverflowChange={report} />)
    expect(screen.getByRole("img").style.overflow).toBe("hidden")
    expect(report).toHaveBeenCalledWith(expect.arrayContaining(["title_b"]))
  })
  it("scopes keyboard navigation, traps tab focus and restores its opener", () => {
    render(<Editor />)
    const notes = screen.getByLabelText("Speaker notes"), opener = screen.getByRole("button", { name: "Present" })
    notes.focus(); fireEvent.keyDown(notes, { key: "ArrowRight" }); expect(screen.queryByRole("dialog")).toBeNull()
    opener.focus(); fireEvent.click(opener)
    const dialog = screen.getByRole("dialog")
    expect(document.activeElement).toBe(dialog)
    fireEvent.keyDown(dialog, { key: "ArrowRight" }); expect(within(dialog).getByText("2 / 2")).toBeTruthy()
    const close = within(dialog).getByRole("button", { name: "Close presentation" }); close.focus()
    fireEvent.keyDown(close, { key: "Tab" }); expect(document.activeElement).toBe(within(dialog).getByRole("button", { name: "Previous slide" }))
    fireEvent.keyDown(dialog, { key: "Escape" }); expect(screen.queryByRole("dialog")).toBeNull()
    expect(document.activeElement).toBe(opener)
  })
  it("does not reinterpret an invalid presentation target", () => {
    render(<SlidePresent deck={sample()} initialSlideId="deleted" onClose={vi.fn()} />)
    expect(screen.getByRole("alert").textContent).toContain("unavailable")
  })
})
