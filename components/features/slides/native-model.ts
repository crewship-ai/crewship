import { canonicalSlideDeck, SLIDES_MAX_IMAGE_ASSETS, SLIDES_MAX_IMAGE_ALT_BYTES, SLIDES_MAX_TABLE_CELLS, SLIDES_MAX_TABLE_CELLS_PER_SLIDE, type Slide, type SlideDeck, type SlideElement } from "@/lib/slides/model"

const bytes = (value: string) => new TextEncoder().encode(value).length
const object = (value: unknown): value is Record<string, unknown> => typeof value === "object" && value !== null && !Array.isArray(value)
const keys = (value: Record<string, unknown>, names: string[]) => Object.keys(value).length === names.length && names.every(name => Object.hasOwn(value, name))
const integer = (value: unknown, min: number, max: number): value is number => Number.isInteger(value) && Number(value) >= min && Number(value) <= max
const color = (value: unknown): value is string => typeof value === "string" && /^#[\da-fA-F]{6}$/.test(value)
const text = (value: unknown, max: number): value is string => typeof value === "string" && value.length <= max && bytes(value) <= max && !/[\u0000-\u0008\u000b-\u001f\u007f\p{Cs}]/u.test(value)
const font = (family: unknown, size: unknown) => text(family, 128) && family.trim() !== "" && !/[\n\t]/.test(family) && integer(size, 1, 160)
const id = (value: unknown): value is string => typeof value === "string" && /^[A-Za-z0-9_-]{1,128}$/.test(value)

// A defensive UI boundary for parsed props, not a replacement for Go admission.
// Unknown fields/types never become DOM attributes, styles or executable markup.
export function isNativeSlideDeck(value: unknown): value is SlideDeck {
  if (!object(value) || !keys(value, ["schema_version", "deck_id", "width", "height", "theme", "slide_order", "slides"]) || (value.schema_version !== 1 && value.schema_version !== 2) || !id(value.deck_id) || !integer(value.width, 64, 4096) || !integer(value.height, 64, 4096)) return false
  const theme = value.theme
  if (!object(theme) || !keys(theme, ["background", "color", "font_family", "font_size"]) || !color(theme.background) || !color(theme.color) || !font(theme.font_family, theme.font_size)) return false
  if (!Array.isArray(value.slides) || value.slides.length < 1 || value.slides.length > 100 || !Array.isArray(value.slide_order) || value.slide_order.length !== value.slides.length) return false
  const ids = new Set<string>(), slideIds = new Set<string>(), imageAssets = new Map<string, string>()
  let count = 0, totalCells = 0
  for (const slide of value.slides) {
    let slideCells = 0
    if (!object(slide) || !keys(slide, ["id", "notes", "elements"]) || !id(slide.id) || ids.has(slide.id) || !text(slide.notes, 16384) || !Array.isArray(slide.elements)) return false
    ids.add(slide.id); slideIds.add(slide.id)
    for (const element of slide.elements) {
      if (++count > 1000 || !object(element) || !id(element.id) || ids.has(element.id) || !integer(element.x, 0, value.width) || !integer(element.y, 0, value.height) || !integer(element.width, 1, value.width) || !integer(element.height, 1, value.height) || element.x + element.width > value.width || element.y + element.height > value.height) return false
      ids.add(element.id)
      if (!keys(element, ["id", "type", "x", "y", "width", "height", String(element.type)])) return false
      if (element.type === "text") {
        const v = element.text
        if (!object(v) || !keys(v, ["value", "font_family", "font_size", "color", "align"]) || !text(v.value, 32768) || !font(v.font_family, v.font_size) || !color(v.color) || typeof v.align !== "string" || !["left", "center", "right"].includes(v.align)) return false
      } else if (element.type === "shape") {
        const v = element.shape
        if (!object(v) || !keys(v, ["kind", "fill", "stroke", "stroke_width"]) || typeof v.kind !== "string" || !["rectangle", "ellipse"].includes(v.kind) || !color(v.fill) || !color(v.stroke) || !integer(v.stroke_width, 0, 16)) return false
      } else if (element.type === "table") {
        const v = element.table
        if (!object(v) || !keys(v, ["cells", "font_family", "font_size", "color", "fill", "border"]) || !font(v.font_family, v.font_size) || !color(v.color) || !color(v.fill) || !color(v.border) || !Array.isArray(v.cells) || v.cells.length < 1 || v.cells.length > 30) return false
        const columns = Array.isArray(v.cells[0]) ? v.cells[0].length : 0
        if (columns < 1 || columns > 20 || v.cells.some(row => !Array.isArray(row) || row.length !== columns || row.some(cell => !text(cell, 4096)))) return false
        const cells = columns * v.cells.length
        totalCells += cells; slideCells += cells
        if (totalCells > SLIDES_MAX_TABLE_CELLS || slideCells > SLIDES_MAX_TABLE_CELLS_PER_SLIDE) return false
      } else if (element.type === "image") {
        const v = element.image
        if (value.schema_version !== 2 || !object(v) || !keys(v, ["asset_id", "expected_sha256", "alt"]) || !id(v.asset_id) || typeof v.expected_sha256 !== "string" || !/^[a-f0-9]{64}$/.test(v.expected_sha256) || !text(v.alt, SLIDES_MAX_IMAGE_ALT_BYTES)) return false
        if (imageAssets.has(v.asset_id) && imageAssets.get(v.asset_id) !== v.expected_sha256) return false
        imageAssets.set(v.asset_id, v.expected_sha256)
        if (imageAssets.size > SLIDES_MAX_IMAGE_ASSETS) return false
      } else return false
    }
  }
  for (const slideId of value.slide_order) {
    if (typeof slideId !== "string" || !slideIds.delete(slideId)) return false
  }
  return bytes(canonicalSlideDeck(value as unknown as SlideDeck)) <= 2 * 1024 * 1024
}

export function orderedSlides(deck: SlideDeck): Slide[] {
  const byId = new Map(deck.slides.map(slide => [slide.id, slide]))
  return deck.slide_order.map(slideId => byId.get(slideId)!)
}

export function freshSlideId(deck: SlideDeck): string {
  const used = new Set(deck.slides.flatMap(slide => [slide.id, ...slide.elements.map(element => element.id)]))
  let next: string
  do { next = `s_${crypto.randomUUID().replaceAll("-", "")}` } while (used.has(next))
  return next
}

export function insertSlide(deck: SlideDeck, after: string, duplicate = false): { deck: SlideDeck; slideId: string } {
  const copy = structuredClone(deck), source = copy.slides.find(slide => slide.id === after)!
  const slideId = freshSlideId(copy)
  const slide: Slide = duplicate ? { ...structuredClone(source), id: slideId } : { id: slideId, notes: "", elements: [] }
  const reserved = new Set([slideId])
  for (const element of slide.elements) {
    do { element.id = freshSlideId(copy) } while (reserved.has(element.id))
    reserved.add(element.id)
  }
  copy.slides.push(slide); copy.slide_order.splice(copy.slide_order.indexOf(after) + 1, 0, slideId)
  return { deck: copy, slideId }
}

export function moveSlide(deck: SlideDeck, slideId: string, direction: -1 | 1): SlideDeck {
  const copy = structuredClone(deck), index = copy.slide_order.indexOf(slideId), target = index + direction
  if (index >= 0 && target >= 0 && target < copy.slide_order.length) [copy.slide_order[index], copy.slide_order[target]] = [copy.slide_order[target], copy.slide_order[index]]
  return copy
}

export function newElement(deck: SlideDeck, type: Exclude<SlideElement["type"], "image">): SlideElement {
  const common = { id: freshSlideId(deck), x: 0, y: 0, width: Math.min(240, deck.width), height: Math.min(100, deck.height) }
  const style = { font_family: deck.theme.font_family, font_size: Math.min(24, deck.theme.font_size), color: deck.theme.color }
  if (type === "text") return { ...common, type, text: { ...style, value: "New text", align: "left" } }
  if (type === "shape") return { ...common, type, shape: { kind: "rectangle", fill: "#ddeeff", stroke: "#112233", stroke_width: 2 } }
  return { ...common, type, table: { ...style, cells: [["Heading", "Value"], ["", ""]], fill: "#ffffff", border: "#112233" } }
}
