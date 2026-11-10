// Schema-1/2 wire contract for the Go Slides authority, plus the schema-3 rich
// read projection of artifact decks (internal/slides/rich.go). This module does
// not advertise a renderer or replace server admission/authorization.
export const SLIDES_SCHEMA_VERSION = 1
export const SLIDES_IMAGE_SCHEMA_VERSION = 2
/** Read-only artifact projection; never stored by the revision-based authority. */
export const SLIDES_RICH_SCHEMA_VERSION = 3
export const SLIDES_IMAGE_HASH_DOMAIN = "crewship-slides-canonical-json/v2\n"
export const SLIDES_MAX_IMAGE_ASSETS = 32
export const SLIDES_MAX_IMAGE_BYTES = 32 << 20
export const SLIDES_MAX_IMAGE_PIXELS = 32 * 1024 * 1024
export const SLIDES_MAX_IMAGE_ALT_BYTES = 2048
// Existing private Page raster policy; checked against actual pageassets Go limits.
export const SLIDES_IMAGE_MAX_DIMENSION = 2048
export const SLIDES_IMAGE_MAX_ASSET_PIXELS = 4 * 1024 * 1024
export const SLIDES_IMAGE_MAX_UPLOAD_BYTES = 5 << 20
export const SLIDES_IMAGE_MAX_STORED_BYTES = 8 << 20
export const SLIDES_MAX_TABLE_CELLS = 10000
export const SLIDES_MAX_TABLE_CELLS_PER_SLIDE = 2000
export const SLIDES_HASH_DOMAIN = "crewship-slides-canonical-json/v1\n"
export const SLIDES_RICH_HASH_DOMAIN = "crewship-slides-canonical-json/v3\n"
/** Schema-3 typography is optional: absent means weight 400, no spacing, line height 120 %. */
export interface SlideText { value: string; font_family: string; font_size: number; color: string; align: "left" | "center" | "right"; font_weight?: number; letter_spacing?: number; line_height?: number }
export interface SlideGradient { angle: number; stops: { color: string; position: number }[] }
export interface SlideLine { x1: number; y1: number; x2: number; y2: number; start: "none" | "triangle"; end: "none" | "triangle" }
/** `line` kind, corner_radius and gradient are schema 3 only. */
export interface SlideShape { kind: "rectangle" | "ellipse" | "line"; fill: string; stroke: string; stroke_width: number; corner_radius?: number; gradient?: SlideGradient; line?: SlideLine }
export interface SlideIcon { name: string; color: string }
export interface SlideTable { cells: string[][]; font_family: string; font_size: number; color: string; fill: string; border: string }
export interface SlideImage { asset_id: string; expected_sha256: string; alt: string }
interface Geometry { id: string; x: number; y: number; width: number; height: number }
export type SlideElement = Geometry & ({ type: "text"; text: SlideText } | { type: "shape"; shape: SlideShape } | { type: "table"; table: SlideTable } | { type: "image"; image: SlideImage } | { type: "icon"; icon: SlideIcon })
export interface Slide { id: string; notes: string; elements: SlideElement[]; transition?: "fade" | "push" }
/** Resolved pin tokens for artifact previews only; never serialized in a deck. */
export interface SlidePreviewTheme {
 background?: string
 textColor?: string
 typography?: { family?: string; size: number; weight?: number }
}
export interface SlideTheme { background: string; color: string; font_family: string; font_size: number }
export interface SlideDeck { theme: SlideTheme; schema_version: 1 | 2 | 3; deck_id: string; width: number; height: number; slide_order: string[]; slides: Slide[] }

// Hash encoding only, not validation. Input must have passed schema admission.
// Model object keys/IDs are ASCII. JSON strings retain Unicode code points;
// Go-compatible escaped U+2028/U+2029 avoids serializer disagreement without
// touching a literal backslash-u sequence. Number fields are bounded integers.
function ordered(value: unknown): unknown {
 if (Array.isArray(value)) return value.map(ordered)
 if (value && typeof value === "object") return Object.fromEntries(Object.entries(value).sort(([a], [b]) => a < b ? -1 : a > b ? 1 : 0).map(([key, item]) => [key, ordered(item)]))
 return value
}
export function canonicalSlideDeck(deck: SlideDeck): string {
 if (deck.schema_version !== SLIDES_SCHEMA_VERSION && deck.schema_version !== SLIDES_IMAGE_SCHEMA_VERSION && deck.schema_version !== SLIDES_RICH_SCHEMA_VERSION) throw new Error("Unsupported Slides schema")
 const sorted = { ...deck, slides: [...deck.slides].sort((a, b) => a.id < b.id ? -1 : a.id > b.id ? 1 : 0) }
 return JSON.stringify(ordered(sorted)).replace(/\u2028/g, "\\u2028").replace(/\u2029/g, "\\u2029")
}
export function canonicalSlideElement(element: SlideElement): string {
 return JSON.stringify(ordered(element)).replace(/\u2028/g, "\\u2028").replace(/\u2029/g, "\\u2029")
}

export function slideDeckHashDomain(version: 1 | 2 | 3): string {
 return version === 2 ? SLIDES_IMAGE_HASH_DOMAIN : version === 3 ? SLIDES_RICH_HASH_DOMAIN : SLIDES_HASH_DOMAIN
}
export function slideElementHashDomain(element: SlideElement): string {
 return element.type === "image" ? SLIDES_IMAGE_HASH_DOMAIN : SLIDES_HASH_DOMAIN
}
