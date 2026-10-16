import { canonicalSlideDeck, canonicalSlideElement, SLIDES_HASH_DOMAIN, type Slide, type SlideDeck } from "./model"
import { isNativeSlideDeck } from "@/components/features/slides/native-model"

export interface SlidesState { schema_version: 1; revision: number; order_version: number; content_hash: string; deck: SlideDeck }
export interface SlidesReceipt { mutation_id: string; revision: number; order_version: number; content_hash: string; duplicate: boolean }
export type SlideOperation = { op: string; slide_id?: string; element_id?: string; if_hash?: string; element?: Slide["elements"][number]; slide?: Slide; notes?: string; position?: number }
export interface SlidesAttempt { path: "/patch" | "/reorder" | "/restore"; body: { page_id: string; schema_version: 1; base_rev: number; mutation_id: string; if_order_version?: number; operations?: SlideOperation[]; slide_order?: string[]; revision?: number } }
const same = (a: unknown, b: unknown) => JSON.stringify(a) === JSON.stringify(b)
function canonical(value: unknown): unknown {
  if (Array.isArray(value)) return value.map(canonical)
  if (value && typeof value === "object") return Object.fromEntries(Object.entries(value).sort(([a], [b]) => a < b ? -1 : a > b ? 1 : 0).map(([k, v]) => [k, canonical(v)]))
  return value
}
async function hash(value: unknown) {
  const encoded = JSON.stringify(canonical(value)).replace(/\u2028/g, "\\u2028").replace(/\u2029/g, "\\u2029")
  const digest = await crypto.subtle.digest("SHA-256", new TextEncoder().encode(SLIDES_HASH_DOMAIN + encoded))
  return Array.from(new Uint8Array(digest), byte => byte.toString(16).padStart(2, "0")).join("")
}
export function validSlidesState(value: unknown): value is SlidesState {
  if (!value || typeof value !== "object") return false
  const v = value as SlidesState
  return v.schema_version === 1 && Number.isSafeInteger(v.revision) && v.revision >= 1 && Number.isSafeInteger(v.order_version) && v.order_version >= 0 && typeof v.content_hash === "string" && /^[a-f0-9]{64}$/.test(v.content_hash) && isNativeSlideDeck(v.deck)
}
/** Preconditions and target hashes come exclusively from the immutable saved base.
 * Hashes for multiple operations on a slide advance with the private candidate. */
export async function planSlidesMutation(base: SlidesState, draft: SlideDeck, mutationID: string): Promise<SlidesAttempt> {
  if (!validSlidesState(base) || !isNativeSlideDeck(draft) || base.deck.deck_id !== draft.deck_id) throw new Error("The saved Page identity or Slides format changed.")
  if (base.deck.width !== draft.width || base.deck.height !== draft.height || !same(base.deck.theme, draft.theme)) throw new Error("Frame and theme editing are not available.")
  if (canonicalSlideDeck(base.deck) === canonicalSlideDeck(draft)) throw new Error("There are no unsaved changes.")
  const current = structuredClone(base.deck), operations: SlideOperation[] = []
  for (const old of [...current.slides]) if (!draft.slides.some(s => s.id === old.id)) {
    operations.push({ op: "delete_slide", slide_id: old.id, if_hash: await hash(old) })
    current.slides = current.slides.filter(s => s.id !== old.id); current.slide_order = current.slide_order.filter(id => id !== old.id)
  }
  for (const [position, id] of draft.slide_order.entries()) if (!current.slides.some(s => s.id === id)) {
    const slide = structuredClone(draft.slides.find(s => s.id === id)!)
    operations.push({ op: "insert_slide", slide, position }); current.slides.push(slide); current.slide_order.splice(position, 0, id)
  }
  for (const next of draft.slides) {
    const old = current.slides.find(s => s.id === next.id)!
    if (old.notes !== next.notes) { operations.push({ op: "set_notes", slide_id: old.id, if_hash: await hash(old), notes: next.notes }); old.notes = next.notes }
    for (const element of [...old.elements]) if (!next.elements.some(e => e.id === element.id)) {
      operations.push({ op: "delete_element", slide_id: old.id, element_id: element.id, if_hash: await hash(element) }); old.elements = old.elements.filter(e => e.id !== element.id)
    }
    for (const element of next.elements) {
      const previous = old.elements.find(e => e.id === element.id)
      if (!previous) { operations.push({ op: "insert_element", slide_id: old.id, if_hash: await hash(old), element: structuredClone(element) }); old.elements.push(structuredClone(element)) }
      else if (canonicalSlideElement(previous) !== canonicalSlideElement(element)) {
        operations.push({ op: "set_element", slide_id: old.id, element_id: element.id, if_hash: await hash(previous), element: structuredClone(element) }); old.elements[old.elements.indexOf(previous)] = structuredClone(element)
      }
    }
    // The current operation contract appends new elements; arbitrary z-order is
    // deliberately unavailable rather than silently changing the user's draft.
    if (!same(old.elements.map(e => e.id), next.elements.map(e => e.id))) throw new Error("Element ordering is unavailable.")
  }
  if (operations.length > 100) throw new Error("Save smaller groups of changes (at most100 operations).")
  const body = { page_id: base.deck.deck_id, schema_version: 1 as const, base_rev: base.revision, mutation_id: mutationID, if_order_version: base.order_version }
  if (!same(current.slide_order, draft.slide_order)) {
    if (operations.length) throw new Error("Save or discard content changes before reordering slides.")
    return { path: "/reorder", body: { ...body, slide_order: [...draft.slide_order] } }
  }
  return { path: "/patch", body: { ...body, operations } }
}
