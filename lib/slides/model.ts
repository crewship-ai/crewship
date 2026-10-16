// Schema-1 wire contract for the Go Slides authority. This module does not
// advertise a renderer or replace server admission/authorization.
export const SLIDES_SCHEMA_VERSION = 1
export const SLIDES_MAX_TABLE_CELLS = 10000
export const SLIDES_MAX_TABLE_CELLS_PER_SLIDE = 2000
export const SLIDES_HASH_DOMAIN = "crewship-slides-canonical-json/v1\n"
export interface SlideText { value: string; font_family: string; font_size: number; color: string; align: "left" | "center" | "right" }
export interface SlideShape { kind: "rectangle" | "ellipse"; fill: string; stroke: string; stroke_width: number }
export interface SlideTable { cells: string[][]; font_family: string; font_size: number; color: string; fill: string; border: string }
interface Geometry { id: string; x: number; y: number; width: number; height: number }
export type SlideElement = Geometry & ({ type: "text"; text: SlideText } | { type: "shape"; shape: SlideShape } | { type: "table"; table: SlideTable })
export interface Slide { id: string; notes: string; elements: SlideElement[] }
export interface SlideTheme { background: string; color: string; font_family: string; font_size: number }
export interface SlideDeck { theme: SlideTheme; schema_version: 1; deck_id: string; width: number; height: number; slide_order: string[]; slides: Slide[] }

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
 if (deck.schema_version !== SLIDES_SCHEMA_VERSION) throw new Error("Unsupported Slides schema")
 const sorted = { ...deck, slides: [...deck.slides].sort((a, b) => a.id < b.id ? -1 : a.id > b.id ? 1 : 0) }
 return JSON.stringify(ordered(sorted)).replace(/\u2028/g, "\\u2028").replace(/\u2029/g, "\\u2029")
}
export function canonicalSlideElement(element: SlideElement): string {
 return JSON.stringify(ordered(element)).replace(/\u2028/g, "\\u2028").replace(/\u2029/g, "\\u2029")
}
