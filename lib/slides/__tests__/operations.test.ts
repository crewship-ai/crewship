/* eslint-disable @typescript-eslint/no-explicit-any -- request mutation tables are intentionally loose */
import { describe, expect, it } from "vitest"
import fixture from "@/testdata/slides-schema-v1.json"
import type { SlideDeck, SlideElement } from "../model"
import { applyPatch, decodeInitialize, decodePatch, decodeReorder, deckHash, diffToPatch, elementHash, slideHash, SlidesConflictError, type PatchRequest, type SlideOperation } from "../operations"
import { createFixtureSaveClient } from "../save-fixture"
import { sha256Hex } from "../sha256"
import { SlidesSchemaError, SlidesValidationError, validateSlideDeck } from "../validate"

const sample = () => structuredClone(fixture.deck) as SlideDeck
const textElement = (id: string): SlideElement => ({ id, type: "text", x: 0, y: 0, width: 20, height: 20, text: { value: "First", font_family: "Arial", font_size: 12, color: "#112233", align: "left" } })
const imageElement = (id = "image_one"): SlideElement => ({ id, type: "image", x: 4, y: 5, width: 20, height: 30, image: { asset_id: "asset_one", expected_sha256: "a".repeat(64), alt: "Sample" } })
const base = { page_id: "fixture_page", schema_version: 1, base_rev: 1 }
const request = (mutation_id: string, operations: SlideOperation[], extra: Partial<PatchRequest> = {}): PatchRequest => ({ ...base, mutation_id, operations, ...extra })
const encode = (value: unknown) => JSON.parse(JSON.stringify(value))

describe("hashes match the Go authority", () => {
  it("sha256 vectors", () => {
    const bytes = (value: string) => new TextEncoder().encode(value)
    expect(sha256Hex(bytes(""))).toBe("e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855")
    expect(sha256Hex(bytes("abc"))).toBe("ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad")
    expect(sha256Hex(bytes("a".repeat(1000)))).toBe("41edece42d63e8d9bf515a9ba6932e1c20cbc9f5a5d134645adb5db1b9737ea3")
  })
  it("deck, slide and element hashes equal Go golden values (incl. U+2028 and astral text)", () => {
    const deck = sample()
    expect(deckHash(deck)).toBe(fixture.hash)
    expect(slideHash(deck.slides[0])).toBe("9a102e9ed55fe27e005f6e261ccee917f8cfdb64a07bf52f15a7545c3011f19a")
    expect(slideHash(deck.slides[1])).toBe("4300a4aa5203989216ab82d8468c73a82eaa5faef013bb0bae73be7795a0cbad")
    expect(elementHash(deck.slides[0].elements[0])).toBe("7829f42a3ecfdb29493b2c0ffc713ca466549e30204c15f6fdb7d3096e682ba5")
    expect(elementHash(deck.slides[0].elements[1])).toBe("db6c4982979de47a39161bfaab579a1e86d7ccac4601c60c5c386b6f28256bb4")
    expect(elementHash({ id: "img", type: "image", x: 0, y: 0, width: 20, height: 20, image: { asset_id: "a1", expected_sha256: "a".repeat(64), alt: "x" } })).toBe("4bec3cf9764d074debc5847aa9b9e2ba0fc59f3f7994f9a9c2adb84fe224facb")
  })
  it("storage order of slides does not change the deck hash", () => {
    const deck = sample(); deck.slides.reverse()
    expect(deckHash(deck)).toBe(fixture.hash)
  })
})

describe("applyPatch", () => {
  it("applies preconditions sequentially and is atomic", () => {
    const current = sample(), before = structuredClone(current)
    const target = current.slides[0].elements[0]
    const updated = structuredClone(target)
    if (updated.type === "text") updated.text.value = "Upraveno česky\nDruhý řádek"
    const set: SlideOperation = { op: "set_element", slide_id: "slide_b", element_id: target.id, if_hash: elementHash(target), element: updated }
    const decoded = decodePatch(encode(request("patch_one", [set])))
    const after = applyPatch(current, decoded)
    expect(after.slides[0].elements[0]).toEqual(updated)
    expect(current).toEqual(before)
    const stale = request("patch_two", [set, { op: "delete_element", slide_id: "slide_b", element_id: target.id, if_hash: elementHash(target) }])
    expect(() => applyPatch(current, stale)).toThrow(SlidesConflictError)
    expect(current).toEqual(before)
    stale.operations[1].if_hash = elementHash(updated)
    const result = applyPatch(current, stale)
    expect(result.slides[0].elements.some(element => element.id === target.id)).toBe(false)
  })
  it("rejects a conflicting or mismatched page, schema, slide, element and hash", () => {
    const deck = sample(), hash = slideHash(deck.slides[0])
    const notes = (patch: Partial<PatchRequest>, op: Partial<SlideOperation> = {}) => request("m_notes", [{ op: "set_notes", slide_id: "slide_b", if_hash: hash, notes: "x", ...op }], patch)
    expect(applyPatch(deck, notes({})).slides[0].notes).toBe("x")
    for (const bad of [notes({ page_id: "other" }), notes({ schema_version: 2 }), notes({}, { slide_id: "nope" }), notes({}, { if_hash: "0".repeat(64) })]) expect(() => applyPatch(deck, bad)).toThrow(SlidesConflictError)
    const missing = request("m_missing", [{ op: "delete_element", slide_id: "slide_b", element_id: "ghost", if_hash: "0".repeat(64) }])
    expect(() => applyPatch(deck, missing)).toThrow(SlidesConflictError)
  })
  it("reserves identities: reusing an existing or deleted id conflicts", () => {
    const deck = sample(), hash = slideHash(deck.slides[0])
    expect(() => applyPatch(deck, request("m_dup", [{ op: "insert_element", slide_id: "slide_b", if_hash: hash, element: textElement("table_a") }]))).toThrow(SlidesConflictError)
    const gone = deck.slides[0].elements[0]
    const reuse = request("m_reuse", [
      { op: "delete_element", slide_id: "slide_b", element_id: gone.id, if_hash: elementHash(gone) },
      { op: "insert_element", slide_id: "slide_b", if_hash: slideHash({ ...deck.slides[0], elements: deck.slides[0].elements.slice(1) }), element: textElement(gone.id) },
    ])
    expect(() => applyPatch(deck, reuse)).toThrow(SlidesConflictError)
  })
  it("cannot cross the aggregate deck budget with a small patch", () => {
    const deck = sample()
    deck.slides = [{ id: "budget_slide", notes: "", elements: Array.from({ length: 63 }, (_, i) => ({ ...textElement(`text_${i}`), width: 1, height: 1 })) }]
    deck.slide_order = ["budget_slide"]
    for (const element of deck.slides[0].elements) if (element.type === "text") element.text.value = "x".repeat(32 << 10)
    validateSlideDeck(deck)
    const extra = structuredClone(deck.slides[0].elements[0]); extra.id = "one_more"
    const small = request("small_delta", [{ op: "insert_element", slide_id: "budget_slide", if_hash: slideHash(deck.slides[0]), element: extra }])
    expect(() => applyPatch(deck, small)).toThrow(SlidesValidationError)
  })
  it("rejected batch leaves the decoded request untouched", () => {
    const deck = sample()
    const slide = { id: "new_slide", notes: "", elements: [textElement("new_first"), textElement("new_second")] }
    const ops: SlideOperation[] = [
      { op: "insert_slide", slide, position: 0 },
      { op: "delete_element", slide_id: "new_slide", element_id: "new_first", if_hash: elementHash(slide.elements[0]) },
      { op: "set_notes", slide_id: "new_slide", if_hash: slideHash(slide), notes: "Later operation must fail" },
    ]
    const decoded = decodePatch(encode(request("atomic", ops, { if_order_version: 0 })))
    const before = JSON.stringify(decoded)
    expect(() => applyPatch(deck, decoded)).toThrow(SlidesConflictError)
    expect(JSON.stringify(decoded)).toBe(before)
  })
  it.each(["insert_slide", "insert_element", "set_element"] as const)("%s: candidate and request share no nested payload", operation => {
    for (const kind of ["text", "shape", "table", "image"] as const) for (const direction of ["candidate", "request"]) {
      const deck = sample(); deck.schema_version = 2
      const element: SlideElement = kind === "text" ? textElement("independent_element")
        : kind === "shape" ? { id: "independent_element", type: "shape", x: 0, y: 0, width: 20, height: 20, shape: { kind: "rectangle", fill: "#FFFFFF", stroke: "#112233", stroke_width: 1 } }
        : kind === "table" ? { id: "independent_element", type: "table", x: 0, y: 0, width: 20, height: 20, table: { cells: [["Original"]], font_family: "Arial", font_size: 12, color: "#112233", fill: "#FFFFFF", border: "#112233" } }
        : imageElement("independent_element")
      let op: SlideOperation
      if (operation === "insert_slide") op = { op: operation, slide: { id: "independent_slide", notes: "", elements: [element] }, position: 0 }
      else if (operation === "set_element") { element.id = deck.slides[0].elements[0].id; op = { op: operation, slide_id: "slide_b", element_id: element.id, if_hash: elementHash(deck.slides[0].elements[0]), element } }
      else op = { op: operation, slide_id: "slide_b", if_hash: slideHash(deck.slides[0]), element }
      const decoded = decodePatch(encode({ ...base, schema_version: 2, mutation_id: "iso", operations: [op], ...(operation === "insert_slide" ? { if_order_version: 0 } : {}) }))
      const candidate = applyPatch(deck, decoded)
      const payload = operation === "insert_slide" ? decoded.operations[0].slide!.elements[0] : decoded.operations[0].element!
      const result = candidate.slides.flatMap(slide => slide.elements).find(item => item.id === element.id)!
      const [changed, retained] = direction === "candidate" ? [result, payload] : [payload, result]
      const snapshot = JSON.stringify(retained)
      if (changed.type === "text") changed.text.value = "Mutated"
      else if (changed.type === "shape") changed.shape.fill = "#445566"
      else if (changed.type === "table") changed.table.cells[0][0] = "Mutated"
      else if (changed.type === "image") changed.image.alt = "Mutated"
      expect(JSON.stringify(retained)).toBe(snapshot)
    }
  })
  it("schema upgrade needs the original content fence and an image", () => {
    const deck = sample()
    const op: SlideOperation = { op: "insert_element", slide_id: "slide_b", if_hash: slideHash(deck.slides[0]), element: imageElement() }
    const upgrade = request("first_image", [op], { target_schema_version: 2, if_content_hash: deckHash(deck) })
    const result = applyPatch(deck, decodePatch(encode(upgrade)))
    expect(result.schema_version).toBe(2)
    expect(deck.schema_version).toBe(1)
    expect(() => applyPatch(deck, { ...upgrade, if_content_hash: "b".repeat(64) })).toThrow(SlidesConflictError)
    expect(() => decodePatch(encode({ ...upgrade, target_schema_version: undefined, if_content_hash: undefined }))).toThrow()
    expect(() => decodePatch(encode({ ...request("fence", [{ ...op, element: textElement("t2") }]), if_content_hash: deckHash(deck) }))).toThrow(/fence requires target/)
    expect(() => applyPatch(deck, request("no_image", [{ ...op, element: textElement("t1") }], { target_schema_version: 2, if_content_hash: deckHash(deck) }))).toThrow(/image attachment/)
    expect(() => decodePatch(encode(request("img_v1", [op])))).toThrow(/image/)
  })
})

describe("decodePatch strictness", () => {
  const valid = () => encode(request("notes", [{ op: "set_notes", slide_id: "slide_b", if_hash: slideHash(sample().slides[0]), notes: "changed" }]))
  it.each<[string, (value: Record<string, any>) => void]>([
    ["unknown top-level field", v => { v.BASE_REV = 2 }],
    ["unknown operation field", v => { v.operations[0].NOTES = "other" }],
    ["unknown operation", v => { v.operations[0].op = "run_script" }],
    ["null string", v => { v.operations[0].notes = null }],
    ["lone surrogate", v => { v.operations[0].notes = "\ud800" }],
    ["control character", v => { v.operations[0].notes = "a\rb" }],
    ["empty batch", v => { v.operations = [] }],
    ["101 operations", v => { v.operations = Array.from({ length: 101 }, () => v.operations[0]) }],
    ["bad hash", v => { v.operations[0].if_hash = "ABC" }],
    ["bad slide id", v => { v.operations[0].slide_id = "bad id" }],
    ["future schema", v => { v.schema_version = 99 }],
    ["negative base", v => { v.base_rev = -1 }],
    ["fractional base", v => { v.base_rev = 1.5 }],
    ["negative order version", v => { v.if_order_version = -1 }],
    ["missing mutation id", v => { delete v.mutation_id }],
  ])("rejects %s", (_name, mutate) => {
    const value = valid(); mutate(value)
    expect(() => decodePatch(value)).toThrow()
  })
  it("requires an order precondition for structural operations", () => {
    const hash = slideHash(sample().slides[0])
    const op: SlideOperation = { op: "delete_slide", slide_id: "slide_b", if_hash: hash }
    expect(() => decodePatch(encode(request("del", [op])))).toThrow(/order precondition/)
    expect(decodePatch(encode(request("del", [op], { if_order_version: 1 }))).operations).toHaveLength(1)
  })
  it("validates nested payloads exactly and does not restrict public ids to the sentinel", () => {
    const hash = slideHash(sample().slides[0])
    const element = textElement("__request_slide")
    expect(decodePatch(encode(request("ins", [{ op: "insert_element", slide_id: "slide_b", if_hash: hash, element }]))).operations[0].element?.id).toBe("__request_slide")
    const bad = { ...textElement("x1"), x: 10_000 }
    expect(() => decodePatch(encode(request("ins", [{ op: "insert_element", slide_id: "slide_b", if_hash: hash, element: bad }])))).toThrow(/geometry/)
    const extra = { ...textElement("x2"), shape: { kind: "rectangle" } } as unknown as SlideElement
    expect(() => decodePatch(encode(request("ins", [{ op: "insert_element", slide_id: "slide_b", if_hash: hash, element: extra }])))).toThrow()
  })
  it("decodes initialize and reorder with Go identity rules", () => {
    const deck = sample()
    const init = { page_id: deck.deck_id, schema_version: 1, base_rev: 0, mutation_id: "create_deck", deck }
    expect(decodeInitialize(encode(init)).deck.deck_id).toBe("fixture_page")
    for (const mutate of [(r: any) => { r.page_id = "other_page" }, (r: any) => { r.schema_version = 2 }, (r: any) => { r.schema_version = 99 }, (r: any) => { r.mutation_id = "" }]) {
      const bad = encode(init); mutate(bad)
      expect(() => decodeInitialize(bad)).toThrow()
    }
    expect(() => decodeInitialize(encode({ ...init, base_rev: 1 }))).toThrow(SlidesConflictError)
    expect(() => decodeInitialize(encode({ ...init, schema_version: 99 }))).toThrow(SlidesSchemaError)
    const reorder = { ...base, mutation_id: "reorder", if_order_version: 1, slide_order: ["slide_a", "slide_b"] }
    expect(decodeReorder(reorder).slide_order).toEqual(["slide_a", "slide_b"])
    expect(() => decodeReorder({ ...reorder, slide_order: null })).toThrow()
  })
})

describe("diffToPatch and the fixture save client", () => {
  const context = { pageId: "fixture_page", schemaVersion: 1, baseRev: 1, orderVersion: 1 }
  it("round-trips a mixed edit exactly through applyPatch", () => {
    const base = sample(), next = sample()
    const title = next.slides[0].elements[0]
    if (title.type === "text") title.text.value = "Nový nadpis"
    next.slides[0].elements[1].x = 300
    next.slides[0].elements.push(textElement("added_one"))
    next.slides[1].notes = "poznámky"
    next.slides[1].elements.pop()
    next.slides.push({ id: "slide_c", notes: "", elements: [textElement("c_text")] }); next.slide_order.splice(1, 0, "slide_c")
    const { request, unrepresentable } = diffToPatch(base, next, { ...context, mutationId: "mix" })
    expect(unrepresentable).toEqual([])
    const decoded = decodePatch(encode(request))
    expect(applyPatch(base, decoded)).toEqual(next)
    expect(deckHash(applyPatch(base, decoded))).toBe(deckHash(next))
  })
  it("reports changes the wire contract cannot express instead of dropping them", () => {
    const layers = sample(); layers.slides[0].elements.reverse()
    expect(diffToPatch(sample(), layers, { ...context, mutationId: "l" })).toEqual({ request: null, unrepresentable: ["layer_order"] })
    const order = sample(); order.slide_order.reverse()
    expect(diffToPatch(sample(), order, { ...context, mutationId: "o" }).unrepresentable).toEqual(["slide_order"])
    const theme = sample(); theme.theme.background = "#000000"
    expect(diffToPatch(sample(), theme, { ...context, mutationId: "t" }).unrepresentable).toEqual(["deck_frame"])
    expect(diffToPatch(sample(), sample(), { ...context, mutationId: "n" })).toEqual({ request: null, unrepresentable: [] })
  })
  it("saves, replays by mutation id and reports conflicts with the fresh head", async () => {
    const server = createFixtureSaveClient({ deck: sample(), rev: 1, orderVersion: 1 })
    const next = sample(); next.slides[1].notes = "saved"
    const { request } = diffToPatch(sample(), next, { ...context, mutationId: "save_1" })
    const first = await server.patch(request!)
    expect(first).toMatchObject({ status: "saved", snapshot: { rev: 2 } })
    expect(await server.patch(request!)).toEqual(first)
    expect(server.head().rev).toBe(2)
    const stale = diffToPatch(sample(), (() => { const d = sample(); d.slides[1].notes = "other"; return d })(), { ...context, mutationId: "save_2" })
    const result = await server.patch(stale.request!)
    expect(result).toMatchObject({ status: "conflict", current: { rev: 2 } })
    expect((await server.patch({ ...request!, mutation_id: "bad", operations: [] })).status).toBe("error")
  })
})
