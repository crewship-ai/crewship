import { expect, it } from "vitest"
import fixture from "@/testdata/artifact-slides-projection.json"
import { artifactSlideThumbnail, parseArtifactSlides } from "../artifact-slides"

it("admits the selected typed projection without reparsing or rewriting authored quotes", () => {
  const input = structuredClone(fixture)
  expect(parseArtifactSlides(input, "page")).toEqual(fixture)
  expect(input).toEqual(fixture)
})

it.each(["constructor", "prototype"])("keeps an own authored %s anchor", id => {
  const input = JSON.parse(JSON.stringify(fixture))
  input.deck.slides[1].elements[0].id = id
  input.anchors = JSON.parse(`{"${id}":{"slide_id":"details","path":"slides/details.html","selector":"#${id}","quote":"North12"}}`)
  const result = parseArtifactSlides(input, "page")
  expect(result && Object.hasOwn(result.anchors, id)).toBe(true)
})

it.each([
  (v: typeof fixture) => { v.deck.schema_version = 2 },
  (v: typeof fixture) => { v.deck.width = 960 },
  (v: typeof fixture) => { v.deck.deck_id = "another-page" },
  (v: typeof fixture) => { v.deck.theme.font_family = "remote-font" },
  (v: typeof fixture) => { v.anchors.title.path = "slides/../private.html" },
  (v: typeof fixture) => { v.anchors.title.selector = "#constructor" },
  (v: typeof fixture) => { v.anchors.title.slide_id = "details" },
  (v: typeof fixture) => { v.anchors.title.quote = "x".repeat(501) },
  (v: typeof fixture) => { v.anchors.title.quote = "\u0000" },
])("refuses an invalid profile or anchor before rendering", change => {
  const input = structuredClone(fixture)
  change(input)
  expect(parseArtifactSlides(input, "page")).toBeNull()
})

it("accepts exactly 500 four-byte quote codepoints and rejects unknown anchor fields", () => {
  const input = structuredClone(fixture)
  input.anchors.title.quote = "😀".repeat(500)
  expect(parseArtifactSlides(input, "page")).not.toBeNull()
  Object.assign(input.anchors.title, { html: "<script>fetch('/private')</script>" })
  expect(parseArtifactSlides(input, "page")).toBeNull()
})

it("rejects unknown top-level fields, unknown element anchors and prototype IDs", () => {
  expect(parseArtifactSlides({ ...fixture, html: "private" }, "page")).toBeNull()
  expect(parseArtifactSlides({ ...fixture, anchors: { missing: fixture.anchors.title } }, "page")).toBeNull()
  const input = JSON.parse(JSON.stringify(fixture))
  input.deck.slides[0].elements[0].id = "__proto__"
  input.anchors = JSON.parse('{"__proto__":{"slide_id":"intro","path":"slides/intro.html","selector":"#__proto__","quote":"Revenue"}}')
  expect(parseArtifactSlides(input, "page")).toBeNull()
})

it("projects each thumbnail to a single slide without copying the other slide payloads", () => {
  const projection = parseArtifactSlides(fixture, "page")!
  for (const id of projection.deck.slide_order) {
    const thumbnail = artifactSlideThumbnail(projection.deck, id)
    expect(thumbnail.slide_order).toEqual([id])
    expect(thumbnail.slides).toEqual([projection.deck.slides.find(slide => slide.id === id)])
    expect(thumbnail.slides[0]).toBe(projection.deck.slides.find(slide => slide.id === id))
  }
  expect(projection.deck.slides).toHaveLength(2)
})
