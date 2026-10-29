import type { SlideDeck } from "@/lib/slides/model"
import { isNativeSlideDeck } from "@/components/features/slides/native-model"
import { artifactSlides } from "@/lib/feature-flags"

export interface ArtifactSlideAnchor { slide_id: string; path: string; selector: string; quote: string }
export interface ArtifactSlidesProjection { deck: SlideDeck; anchors: Record<string, ArtifactSlideAnchor> }

/** Reserved source markers can refuse rendering, never infer a positive type from an asset name. */
export function artifactSlidesMode(kind: unknown, files: readonly { path: string }[]): "slides" | "unsupported" | null {
  if (!artifactSlides()) return null
  const deck = files.some(file => file.path === "deck.json")
  if (kind === "slides" && deck && !files.some(file => file.path === "dashboard.json")) return "slides"
  return deck || kind === "slides" ? "unsupported" : null
}

const object = (value: unknown): value is Record<string, unknown> => value !== null && typeof value === "object" && !Array.isArray(value)
const fields = (value: Record<string, unknown>, names: string[]) => Object.keys(value).length === names.length && names.every(name => Object.hasOwn(value, name))
const id = (value: string) => /^[A-Za-z][A-Za-z0-9_-]{0,63}$/.test(value)

/** Defensive admission of the server's typed projection; source HTML is never parsed or rendered here. */
export function parseArtifactSlides(value: unknown, pageId: string): ArtifactSlidesProjection | null {
  if (!object(value) || !fields(value, ["deck", "anchors"]) || !isNativeSlideDeck(value.deck) || !object(value.anchors)) return null
  const deck = value.deck
  if (deck.deck_id !== pageId || deck.width !== 1920 || deck.height !== 1080 || deck.theme.font_family !== "Go Regular") return null
  const elements = new Map<string, { slide: string; type: string }>()
  for (const slide of deck.slides) {
    if (!id(slide.id) || slide.elements.length > 200 || Array.from(slide.notes).length > 4000) return null
    for (const element of slide.elements) {
      if (!id(element.id)) return null
      if (element.type === "text" && element.text.font_family !== "Go Regular" || element.type === "table" && element.table.font_family !== "Go Regular") return null
      elements.set(element.id, { slide: slide.id, type: element.type })
    }
  }
  let count = 0
  for (const key in value.anchors) {
    if (!Object.hasOwn(value.anchors, key)) continue
    if (++count > 1000) return null
    const anchor = value.anchors[key]
    const element = elements.get(key)
    if (!id(key) || !element || !["text", "table"].includes(element.type) || !object(anchor) || !fields(anchor, ["slide_id", "path", "selector", "quote"])) return null
    if (anchor.slide_id !== element.slide || anchor.path !== `slides/${element.slide}.html` || anchor.selector !== `#${key}` || typeof anchor.quote !== "string") return null
    if (!anchor.quote || anchor.quote.length > 1000 || Array.from(anchor.quote).length > 500 || new TextEncoder().encode(anchor.quote).byteLength > 2000 || /[\u0000-\u001f\u007f\p{Cs}]/u.test(anchor.quote)) return null
  }
  return value as unknown as ArtifactSlidesProjection
}

/** Canvas validates its complete input: thumbnails must only carry their one slide. */
export function artifactSlideThumbnail(deck: SlideDeck, slideId: string): SlideDeck {
  return { ...deck, slide_order: [slideId], slides: deck.slides.filter(slide => slide.id === slideId) }
}
