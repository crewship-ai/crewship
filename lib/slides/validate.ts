import { canonicalSlideDeck, SLIDES_IMAGE_SCHEMA_VERSION, SLIDES_MAX_IMAGE_ALT_BYTES, SLIDES_MAX_IMAGE_ASSETS, SLIDES_MAX_TABLE_CELLS, SLIDES_MAX_TABLE_CELLS_PER_SLIDE, SLIDES_SCHEMA_VERSION, type SlideDeck } from "./model"

// Mirrors internal/slides/model.go Validate (strict field sets + bounded model).
// Error texts match the Go authority so UI and fixtures can compare them.
export class SlidesSchemaError extends Error { constructor() { super("unsupported Slides schema"); this.name = "SlidesSchemaError" } }
export class SlidesValidationError extends Error { constructor(message: string) { super(message); this.name = "SlidesValidationError" } }

export const MAX_DECK_BYTES = 2 << 20
export const MAX_SLIDES = 100
export const MAX_ELEMENTS = 1000
export const MAX_TEXT_BYTES = 32 << 10
export const MAX_NOTES_BYTES = 16 << 10
const encoder = new TextEncoder()
export const byteLength = (value: string) => encoder.encode(value).length

const IDENTIFIER = /^[A-Za-z0-9_-]{1,128}$/
const COLOR = /^#[0-9A-Fa-f]{6}$/
export const HASH_PATTERN = /^[0-9a-f]{64}$/
export const isIdentifier = (value: unknown): value is string => typeof value === "string" && IDENTIFIER.test(value)
export const isSupportedSchema = (value: unknown) => value === SLIDES_SCHEMA_VERSION || value === SLIDES_IMAGE_SCHEMA_VERSION

const fail = (message: string): never => { throw new SlidesValidationError(message) }
const isObject = (value: unknown): value is Record<string, unknown> => typeof value === "object" && value !== null && !Array.isArray(value)
const integer = (value: unknown, min: number, max: number) => typeof value === "number" && Number.isInteger(value) && value >= min && value <= max

/** Same rule as Go requiredObject: exact key set, no null values. */
export function exactObject(value: unknown, fields: readonly string[]): Record<string, unknown> {
  if (!isObject(value)) return fail("required Slides object")
  if (Object.keys(value).length !== fields.length) return fail("Slides object contains unknown or incorrectly cased fields")
  for (const field of fields) if (!Object.hasOwn(value, field) || value[field] === null) fail(`required Slides field ${field}`)
  return value
}
/** Go validText: UTF-8, byte limit, no CR, no control characters except LF and TAB. */
export function validText(value: unknown, limit: number): value is string {
  return typeof value === "string" && !/\p{Cs}/u.test(value) && byteLength(value) <= limit && !/[\u0000-\u0008\u000b-\u001f\u007f]/.test(value)
}
// Go strings.TrimSpace (unicode.IsSpace): differs from String.trim at U+0085 and U+FEFF.
const GO_SPACE = new RegExp("^[\\t\\n\\v\\f\\r \\u0085\\u00a0\\u1680\\u2000-\\u200a\\u2028\\u2029\\u202f\\u205f\\u3000]*$")
const validFont = (family: unknown, size: unknown) => validText(family, 128) && !GO_SPACE.test(family) && !/[\n\t]/.test(family) && integer(size, 1, 160)
const isColor = (value: unknown) => typeof value === "string" && COLOR.test(value)

const FIELDS = {
  text: ["value", "font_family", "font_size", "color", "align"],
  shape: ["kind", "fill", "stroke", "stroke_width"],
  table: ["cells", "font_family", "font_size", "color", "fill", "border"],
  image: ["asset_id", "expected_sha256", "alt"],
} as const

export function validateSlideDeck(input: unknown): asserts input is SlideDeck {
  const deck = exactObject(input, ["schema_version", "deck_id", "width", "height", "slide_order", "slides", "theme"])
  if (!isSupportedSchema(deck.schema_version)) throw new SlidesSchemaError()
  const { width, height } = deck
  if (!isIdentifier(deck.deck_id) || !integer(width, 64, 4096) || !integer(height, 64, 4096) || !Array.isArray(deck.slides) || !Array.isArray(deck.slide_order) || deck.slides.length < 1 || deck.slides.length > MAX_SLIDES || deck.slide_order.length !== deck.slides.length) fail("invalid Slides deck structure")
  const theme = exactObject(deck.theme, ["background", "color", "font_family", "font_size"])
  if (!isColor(theme.background) || !isColor(theme.color) || !validFont(theme.font_family, theme.font_size)) fail("invalid deck theme")
  const ids = new Set<string>(), slides = new Set<string>(), images = new Map<string, string>()
  let count = 0, tableCells = 0
  for (const rawSlide of deck.slides as unknown[]) {
    const slide = exactObject(rawSlide, ["id", "notes", "elements"])
    if (!isIdentifier(slide.id) || ids.has(slide.id) || !Array.isArray(slide.elements) || !validText(slide.notes, MAX_NOTES_BYTES)) fail("invalid slide identity, notes or elements")
    ids.add(slide.id as string); slides.add(slide.id as string)
    let slideCells = 0
    for (const rawElement of slide.elements as unknown[]) {
      if (!isObject(rawElement) || typeof rawElement.type !== "string" || !(rawElement.type in FIELDS)) fail("unsupported element type")
      const kind = (rawElement as { type: keyof typeof FIELDS }).type
      const element = exactObject(rawElement, ["id", "type", "x", "y", "width", "height", kind])
      if (++count > MAX_ELEMENTS || !isIdentifier(element.id) || ids.has(element.id)) fail("invalid or duplicate element identity")
      ids.add(element.id as string)
      const w = element.width, h = element.height, x = element.x, y = element.y
      if (!integer(x, 0, 1 << 30) || !integer(y, 0, 1 << 30) || !integer(w, 1, width as number) || !integer(h, 1, height as number) || (x as number) > (width as number) - (w as number) || (y as number) > (height as number) - (h as number)) fail("element outside bounded deck geometry")
      const v = exactObject(element[kind], FIELDS[kind])
      if (kind === "text") {
        if (!validText(v.value, MAX_TEXT_BYTES) || !validFont(v.font_family, v.font_size) || !isColor(v.color) || !["left", "center", "right"].includes(v.align as string)) fail("invalid text element")
      } else if (kind === "shape") {
        if (!["rectangle", "ellipse"].includes(v.kind as string) || !isColor(v.fill) || !isColor(v.stroke) || !integer(v.stroke_width, 0, 16)) fail("invalid shape element")
      } else if (kind === "table") {
        const cells = v.cells
        if (!Array.isArray(cells) || cells.length < 1 || cells.length > 30 || !Array.isArray(cells[0]) || cells[0].length < 1 || cells[0].length > 20 || !validFont(v.font_family, v.font_size) || !isColor(v.color) || !isColor(v.fill) || !isColor(v.border)) return fail("invalid table element")
        for (const row of cells) {
          if (!Array.isArray(row) || row.length !== cells[0].length) fail("nonrectangular table")
          for (const cell of row) {
            if (++slideCells > SLIDES_MAX_TABLE_CELLS_PER_SLIDE) fail("slide exceeds 2000 total table cells")
            if (++tableCells > SLIDES_MAX_TABLE_CELLS) fail("deck exceeds 10000 total table cells")
            if (!validText(cell, 4096)) fail("invalid table cell")
          }
        }
      } else {
        if (deck.schema_version !== SLIDES_IMAGE_SCHEMA_VERSION || !isIdentifier(v.asset_id) || typeof v.expected_sha256 !== "string" || !HASH_PATTERN.test(v.expected_sha256) || !validText(v.alt, SLIDES_MAX_IMAGE_ALT_BYTES)) fail("invalid schema-2 image element")
        const known = images.get(v.asset_id as string)
        if (known !== undefined && known !== v.expected_sha256) fail("conflicting image asset identity")
        images.set(v.asset_id as string, v.expected_sha256 as string)
        if (images.size > SLIDES_MAX_IMAGE_ASSETS) fail("too many unique image assets")
      }
    }
  }
  for (const id of deck.slide_order as unknown[]) {
    if (typeof id !== "string" || !slides.delete(id)) fail("slide order is not a unique complete permutation")
  }
  if (byteLength(canonicalSlideDeck(input as SlideDeck)) > MAX_DECK_BYTES) fail("Slides deck exceeds byte budget")
}
