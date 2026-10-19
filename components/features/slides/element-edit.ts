import type { SlideDeck, SlideElement } from "@/lib/slides/model"
import { newElement } from "./native-model"

// Pure draft edits used by the editor. Every function returns a new deck and
// never mutates its input; the caller still runs the whole-deck gate.
export type Layer = "forward" | "backward" | "front" | "back"
export type Rect = { x: number; y: number; width: number; height: number }

const clamp = (value: number, min: number, max: number) => Math.min(Math.max(value, min), Math.max(min, max))
function withElements(deck: SlideDeck, slideId: string, edit: (elements: SlideElement[]) => SlideElement[] | void): SlideDeck {
  const next = structuredClone(deck), slide = next.slides.find(value => value.id === slideId)
  if (slide) slide.elements = edit(slide.elements) ?? slide.elements
  return next
}

/** Largest shared offset that keeps the whole selection inside the frame (a group moves as one). */
export function clampDelta(deck: SlideDeck, elements: readonly SlideElement[], dx: number, dy: number): { dx: number; dy: number } {
  if (elements.length === 0) return { dx: 0, dy: 0 }
  const minX = Math.min(...elements.map(element => element.x)), minY = Math.min(...elements.map(element => element.y))
  const maxX = Math.max(...elements.map(element => element.x + element.width)), maxY = Math.max(...elements.map(element => element.y + element.height))
  return { dx: clamp(Math.round(dx), -minX, deck.width - maxX), dy: clamp(Math.round(dy), -minY, deck.height - maxY) }
}

/** Moves the elements by whole pixels as one group, keeping the group inside the frame. */
export function nudgeElements(deck: SlideDeck, slideId: string, ids: readonly string[], dx: number, dy: number): SlideDeck {
  return withElements(deck, slideId, elements => {
    const picked = elements.filter(element => ids.includes(element.id)), delta = clampDelta(deck, picked, dx, dy)
    for (const element of picked) { element.x += delta.dx; element.y += delta.dy }
  })
}

export function resizeElement(deck: SlideDeck, slideId: string, id: string, rect: Rect): SlideDeck {
  return withElements(deck, slideId, elements => {
    const element = elements.find(value => value.id === id)
    if (!element) return
    element.width = clamp(Math.round(rect.width), 1, deck.width)
    element.height = clamp(Math.round(rect.height), 1, deck.height)
    element.x = clamp(Math.round(rect.x), 0, deck.width - element.width)
    element.y = clamp(Math.round(rect.y), 0, deck.height - element.height)
  })
}

export const removeElements = (deck: SlideDeck, slideId: string, ids: readonly string[]) => withElements(deck, slideId, elements => elements.filter(element => !ids.includes(element.id)))

export function addElement(deck: SlideDeck, slideId: string, type: "text" | "shape" | "table"): { deck: SlideDeck; id: string } {
  const element = newElement(deck, type)
  return { deck: withElements(deck, slideId, elements => [...elements, element]), id: element.id }
}

/** Array order is paint order: the last element is on top. Relative order inside the selection is kept. */
export function reorderElements(deck: SlideDeck, slideId: string, ids: readonly string[], layer: Layer): SlideDeck {
  return withElements(deck, slideId, elements => {
    const picked = (element: SlideElement) => ids.includes(element.id)
    if (layer === "front") return [...elements.filter(element => !picked(element)), ...elements.filter(picked)]
    if (layer === "back") return [...elements.filter(picked), ...elements.filter(element => !picked(element))]
    const result = [...elements], forward = layer === "forward"
    const order = forward ? result.map((_, index) => result.length - 1 - index) : result.map((_, index) => index)
    for (const index of order) {
      const target = index + (forward ? 1 : -1)
      if (picked(result[index]) && target >= 0 && target < result.length && !picked(result[target])) [result[index], result[target]] = [result[target], result[index]]
    }
    return result
  })
}

/** Whether a layer move would change anything, for disabling buttons. */
export const canReorder = (deck: SlideDeck, slideId: string, ids: readonly string[], layer: Layer) =>
  ids.length > 0 && JSON.stringify(reorderElements(deck, slideId, ids, layer)) !== JSON.stringify(deck)
