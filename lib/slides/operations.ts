import { canonicalSlideDeck, canonicalSlideElement, SLIDES_HASH_DOMAIN, SLIDES_IMAGE_HASH_DOMAIN, SLIDES_IMAGE_SCHEMA_VERSION, SLIDES_SCHEMA_VERSION, slideDeckHashDomain, type Slide, type SlideDeck, type SlideElement } from "./model"
import { sha256Hex } from "./sha256"
import { byteLength, exactObject, HASH_PATTERN, isIdentifier, isSupportedSchema, MAX_DECK_BYTES, MAX_SLIDES, SlidesSchemaError, SlidesValidationError, validateSlideDeck, validText, MAX_NOTES_BYTES } from "./validate"

// Client mirror of internal/slides/operations.go (#3633): same operation names,
// wire fields, validation errors and atomic batch semantics. No HTTP here.
export const MAX_OPERATIONS = 100

export class SlidesConflictError extends Error { constructor() { super("Slides precondition conflict"); this.name = "SlidesConflictError" } }

export interface SlideOperation {
  op: "set_element" | "insert_element" | "delete_element" | "set_notes" | "insert_slide" | "delete_slide"
  slide_id?: string
  element_id?: string
  if_hash?: string
  element?: SlideElement
  slide?: Slide
  notes?: string
  position?: number
}
export interface PatchRequest {
  page_id: string
  schema_version: number
  base_rev: number
  mutation_id: string
  if_order_version?: number
  operations: SlideOperation[]
  target_schema_version?: number
  if_content_hash?: string
}
export interface ReorderRequest { page_id: string; schema_version: number; base_rev: number; mutation_id: string; if_order_version: number; slide_order: string[] }
export interface InitializeRequest { page_id: string; schema_version: number; base_rev: number; mutation_id: string; deck: SlideDeck }

const fail = (message: string): never => { throw new SlidesValidationError(message) }
const isObject = (value: unknown): value is Record<string, unknown> => typeof value === "object" && value !== null && !Array.isArray(value)
const intIn = (value: unknown, min: number) => typeof value === "number" && Number.isInteger(value) && value >= min

// Go canonicalValue for a bare slide/element: sorted keys, line/paragraph separators escaped.
function ordered(value: unknown): unknown {
  if (Array.isArray(value)) return value.map(ordered)
  if (isObject(value)) return Object.fromEntries(Object.entries(value).sort(([a], [b]) => (a < b ? -1 : a > b ? 1 : 0)).map(([key, item]) => [key, ordered(item)]))
  return value
}
const hashOf = (domain: string, json: string) => sha256Hex(new TextEncoder().encode(domain + json))
export const deckHash = (deck: SlideDeck) => hashOf(slideDeckHashDomain(deck.schema_version), canonicalSlideDeck(deck))
export const elementHash = (element: SlideElement) => hashOf(element.type === "image" ? SLIDES_IMAGE_HASH_DOMAIN : SLIDES_HASH_DOMAIN, canonicalSlideElement(element))
export const slideHash = (slide: Slide) => hashOf(SLIDES_HASH_DOMAIN, JSON.stringify(ordered(slide)).replace(/\u2028/g, "\\u2028").replace(/\u2029/g, "\\u2029"))

export function objectIds(deck: Pick<SlideDeck, "slides">): Map<string, "slide" | "element"> {
  const ids = new Map<string, "slide" | "element">()
  for (const slide of deck.slides) { ids.set(slide.id, "slide"); for (const element of slide.elements) ids.set(element.id, "element") }
  return ids
}
export function imageReferences(deck: SlideDeck): Map<string, string> {
  const refs = new Map<string, string>()
  for (const slide of deck.slides) for (const element of slide.elements) if (element.type === "image") refs.set(element.image.asset_id, element.image.expected_sha256)
  return refs
}

// Nested payloads go through the exact deck checks using a maximal legal frame.
function checkStandaloneSlide(slide: unknown, schema: number) {
  const id = isObject(slide) ? slide.id : undefined
  validateSlideDeck({ schema_version: schema, deck_id: "__request_deck", width: 4096, height: 4096, theme: { background: "#FFFFFF", color: "#000000", font_family: "Arial", font_size: 24 }, slide_order: [id], slides: [slide] })
}
function checkStandaloneElement(element: unknown, schema: number) {
  const id = isObject(element) ? element.id : undefined
  checkStandaloneSlide({ id: id === "__request_slide" ? "__request_slide2" : "__request_slide", notes: "", elements: [element] }, schema)
}

const OP_FIELDS: Record<string, string[]> = {
  set_element: ["op", "slide_id", "element_id", "if_hash", "element"],
  insert_element: ["op", "slide_id", "if_hash", "element"],
  delete_element: ["op", "slide_id", "element_id", "if_hash"],
  set_notes: ["op", "slide_id", "if_hash", "notes"],
  insert_slide: ["op", "slide", "position"],
  delete_slide: ["op", "slide_id", "if_hash"],
}
function requestObject(raw: unknown, required: string[], optional: string[]): Record<string, unknown> {
  if (!isObject(raw)) return fail("required Slides object")
  if (byteLength(JSON.stringify(raw)) > MAX_DECK_BYTES) fail("invalid Slides request byte budget or UTF-8")
  return exactObject(raw, [...required, ...optional.filter(name => Object.hasOwn(raw, name))])
}
function validMutation(request: { schema_version: unknown; base_rev: unknown; mutation_id: unknown; page_id: unknown }) {
  if (!intIn(request.schema_version, 0)) fail("Slides JSON: schema_version must be an integer")
  if (!isSupportedSchema(request.schema_version)) throw new SlidesSchemaError()
  if (!intIn(request.base_rev, 0) || !isIdentifier(request.mutation_id) || !isIdentifier(request.page_id)) fail("invalid mutation identity or base revision")
}

/**
 * Strict decode (Go DecodePatch). Returns a deep copy that owns its payloads.
 * Stricter than Go (safe side): an empty if_content_hash without a target schema and a
 * negative slide position are rejected here. RestoreRequest is not mirrored.
 */
export function decodePatch(input: unknown): PatchRequest {
  const raw = requestObject(input, ["page_id", "schema_version", "base_rev", "mutation_id", "operations"], ["if_order_version", "target_schema_version", "if_content_hash"])
  if (!intIn(raw.schema_version, 0) || (raw.target_schema_version !== undefined && !intIn(raw.target_schema_version, 0))) fail("Slides JSON: schema_version must be an integer")
  const schema = (raw.target_schema_version ?? raw.schema_version) as number
  if (!Array.isArray(raw.operations) || raw.operations.length < 1 || raw.operations.length > MAX_OPERATIONS) fail("patch requires 1–100 operations")
  for (const rawOp of raw.operations as unknown[]) {
    if (!isObject(rawOp) || typeof rawOp.op !== "string") return fail("required Slides object")
    const fields = Object.hasOwn(OP_FIELDS, rawOp.op) ? OP_FIELDS[rawOp.op] : fail("unsupported Slides operation")
    exactObject(rawOp, fields)
    if (Object.hasOwn(rawOp, "slide")) checkStandaloneSlide(rawOp.slide, schema)
    if (Object.hasOwn(rawOp, "element")) checkStandaloneElement(rawOp.element, schema)
  }
  const request = structuredClone(raw) as unknown as PatchRequest
  validMutation(request)
  if (request.target_schema_version !== undefined) {
    if (request.schema_version !== SLIDES_SCHEMA_VERSION || request.target_schema_version !== SLIDES_IMAGE_SCHEMA_VERSION || typeof request.if_content_hash !== "string" || !HASH_PATTERN.test(request.if_content_hash)) fail("explicit schema upgrade requires original content hash")
  } else if (request.if_content_hash !== undefined) fail("content hash upgrade fence requires target schema")
  if (request.if_order_version !== undefined && !intIn(request.if_order_version, 0)) fail("invalid order precondition")
  for (const op of request.operations) {
    if (op.op !== "insert_slide" && (!isIdentifier(op.slide_id) || typeof op.if_hash !== "string" || !HASH_PATTERN.test(op.if_hash))) fail("stable target and hash required")
    if ((op.op === "set_element" || op.op === "delete_element") && !isIdentifier(op.element_id)) fail("element target required")
    if ((op.op === "insert_slide" || op.op === "delete_slide") && request.if_order_version === undefined) fail("structural operations require order precondition")
    if (op.op === "set_notes" && !validText(op.notes, MAX_NOTES_BYTES)) fail("invalid notes")
    if (op.op === "insert_slide" && !intIn(op.position, 0)) fail("invalid slide insertion")
  }
  return request
}

export function decodeReorder(input: unknown): ReorderRequest {
  const raw = requestObject(input, ["page_id", "schema_version", "base_rev", "mutation_id", "if_order_version", "slide_order"], [])
  const request = structuredClone(raw) as unknown as ReorderRequest
  validMutation(request)
  if (!intIn(request.if_order_version, 0) || !Array.isArray(request.slide_order) || request.slide_order.length > MAX_SLIDES || request.slide_order.some(id => typeof id !== "string")) fail("invalid order precondition")
  return request
}

export function decodeInitialize(input: unknown): InitializeRequest {
  const raw = requestObject(input, ["page_id", "schema_version", "base_rev", "mutation_id", "deck"], [])
  validateSlideDeck(raw.deck)
  const request = structuredClone(raw) as unknown as InitializeRequest
  validMutation(request)
  if (request.schema_version !== request.deck.schema_version) throw new SlidesSchemaError()
  if (request.page_id !== request.deck.deck_id) fail("initialize Page identity differs from deck")
  if (request.base_rev !== 0) throw new SlidesConflictError()
  return request
}

/**
 * Pure atomic apply (Go ApplyPatch). Operations see the candidate produced by
 * earlier ones; any failure throws and `current`/`request` stay untouched.
 * The result shares no nested object with `current` or `request`.
 * Expects a request that passed `decodePatch` (operation field types are not rechecked).
 */
export function applyPatch(current: SlideDeck, request: PatchRequest): SlideDeck {
  if (request.page_id !== current.deck_id || request.schema_version !== current.schema_version) throw new SlidesConflictError()
  const candidate = structuredClone(current)
  validateSlideDeck(candidate)
  if (request.target_schema_version !== undefined) {
    if (deckHash(current) !== request.if_content_hash || current.schema_version !== SLIDES_SCHEMA_VERSION || request.target_schema_version !== SLIDES_IMAGE_SCHEMA_VERSION) throw new SlidesConflictError()
    candidate.schema_version = SLIDES_IMAGE_SCHEMA_VERSION
  }
  applyOperations(candidate, objectIds(candidate), request.operations)
  if (request.target_schema_version !== undefined && imageReferences(candidate).size === 0) fail("schema upgrade requires an image attachment")
  validateSlideDeck(candidate)
  return candidate
}

function applyOperations(candidate: SlideDeck, used: Map<string, "slide" | "element">, operations: readonly SlideOperation[]) {
  for (const op of operations) {
    if (op.op === "insert_slide") {
      if (!op.slide || op.position === undefined || op.position < 0 || op.position > candidate.slide_order.length) return fail("invalid slide insertion")
      for (const [id, kind] of objectIds({ slides: [op.slide] })) {
        if (used.has(id)) throw new SlidesConflictError()
        used.set(id, kind)
      }
      candidate.slides.push(structuredClone(op.slide))
      candidate.slide_order.splice(op.position, 0, op.slide.id)
      continue
    }
    const index = candidate.slides.findIndex(slide => slide.id === op.slide_id)
    if (index < 0) throw new SlidesConflictError()
    const slide = candidate.slides[index]
    if (op.op === "set_element" || op.op === "delete_element") {
      const at = slide.elements.findIndex(element => element.id === op.element_id)
      if (at < 0 || elementHash(slide.elements[at]) !== op.if_hash) throw new SlidesConflictError()
      if (op.op === "delete_element") slide.elements.splice(at, 1)
      else {
        if (!op.element || op.element.id !== op.element_id) return fail("replacement must preserve identity")
        slide.elements[at] = structuredClone(op.element)
      }
      continue
    }
    if (slideHash(slide) !== op.if_hash) throw new SlidesConflictError()
    if (op.op === "insert_element") {
      if (!op.element) return fail("element required")
      if (used.has(op.element.id)) throw new SlidesConflictError()
      used.set(op.element.id, "element")
      slide.elements.push(structuredClone(op.element))
    } else if (op.op === "set_notes") {
      if (op.notes === undefined) return fail("notes required")
      slide.notes = op.notes
    } else if (op.op === "delete_slide") {
      candidate.slides.splice(index, 1)
      const at = candidate.slide_order.indexOf(op.slide_id!)
      if (at >= 0) candidate.slide_order.splice(at, 1)
    } else fail(`unsupported operation "${op.op}"`)
  }
}

export interface MutationContext { pageId: string; schemaVersion: number; baseRev: number; mutationId: string; orderVersion?: number }
export type Unrepresentable = "layer_order" | "slide_order" | "deck_frame" | "deck_identity" | "identity_reuse" | "order_version_missing" | "operation_budget" | "unreproduced"
export interface DiffResult { request: PatchRequest | null; unrepresentable: Unrepresentable[] }
const same = (a: unknown, b: unknown) => JSON.stringify(ordered(a)) === JSON.stringify(ordered(b))

/**
 * Turns a local draft into an operation batch against `base`. Hashes come from the
 * running candidate so the batch is valid sequentially. Changes the wire contract
 * cannot express (z-order, slide reorder, theme/size) are reported, never dropped.
 */
export function diffToPatch(base: SlideDeck, next: SlideDeck, context: MutationContext): DiffResult {
  try { return diffUnchecked(base, next, context) } catch (error) {
    // Reused ids (cross-slide moves, duplicates) stay reserved in the authority, so they cannot be saved.
    if (error instanceof SlidesConflictError || error instanceof SlidesValidationError) return { request: null, unrepresentable: ["identity_reuse"] }
    throw error
  }
}

const ignored = (deck: SlideDeck, skip: ReadonlySet<Unrepresentable>) => ordered({
  ...deck,
  slides: [...deck.slides].sort((a, b) => (a.id < b.id ? -1 : 1)).map(slide => skip.has("layer_order") ? { ...slide, elements: [...slide.elements].sort((a, b) => (a.id < b.id ? -1 : 1)) } : slide),
  ...(skip.has("slide_order") ? { slide_order: [] } : {}),
  ...(skip.has("deck_frame") ? { width: 0, height: 0, theme: {} } : {}),
})

function diffUnchecked(base: SlideDeck, next: SlideDeck, context: MutationContext): DiffResult {
  const unrepresentable = new Set<Unrepresentable>()
  if (base.schema_version !== next.schema_version || base.deck_id !== next.deck_id) unrepresentable.add("deck_identity")
  if (next.slides.length !== next.slide_order.length || next.slides.some(slide => !next.slide_order.includes(slide.id))) unrepresentable.add("slide_order")
  const running = structuredClone(base), used = objectIds(running), operations: SlideOperation[] = []
  const emit = (op: SlideOperation) => { applyOperations(running, used, [op]); operations.push(structuredClone(op)) }
  if (base.width !== next.width || base.height !== next.height || !same(base.theme, next.theme)) unrepresentable.add("deck_frame")
  const nextById = new Map(next.slides.map(slide => [slide.id, slide])), baseIds = new Set(base.slides.map(slide => slide.id))
  const kept = base.slide_order.filter(id => nextById.has(id))
  if (!same(kept, next.slide_order.filter(id => baseIds.has(id)))) unrepresentable.add("slide_order")
  for (const id of base.slide_order) if (!nextById.has(id)) emit({ op: "delete_slide", slide_id: id, if_hash: slideHash(running.slides.find(slide => slide.id === id)!) })
  for (const id of kept) {
    const before = running.slides.find(slide => slide.id === id)!, after = nextById.get(id)!
    const nextElements = new Map(after.elements.map(element => [element.id, element]))
    for (const element of [...before.elements]) if (!nextElements.has(element.id)) emit({ op: "delete_element", slide_id: id, element_id: element.id, if_hash: elementHash(element) })
    const live = running.slides.find(slide => slide.id === id)!
    const keptIds = live.elements.map(element => element.id)
    const added = after.elements.filter(element => !keptIds.includes(element.id))
    if (!same(keptIds, after.elements.map(element => element.id).filter(elementId => keptIds.includes(elementId))) || !same(after.elements.slice(keptIds.length).map(element => element.id), added.map(element => element.id))) unrepresentable.add("layer_order")
    for (const element of live.elements) {
      const wanted = nextElements.get(element.id)!
      if (!same(element, wanted)) emit({ op: "set_element", slide_id: id, element_id: element.id, if_hash: elementHash(element), element: wanted })
    }
    for (const element of added) emit({ op: "insert_element", slide_id: id, if_hash: slideHash(running.slides.find(slide => slide.id === id)!), element })
    if (live.notes !== after.notes) emit({ op: "set_notes", slide_id: id, if_hash: slideHash(running.slides.find(slide => slide.id === id)!), notes: after.notes })
  }
  next.slide_order.forEach((id, position) => { if (!baseIds.has(id)) emit({ op: "insert_slide", slide: nextById.get(id)!, position }) })
  if (operations.length > MAX_OPERATIONS) unrepresentable.add("operation_budget")
  if (context.orderVersion === undefined && operations.some(op => op.op === "insert_slide" || op.op === "delete_slide")) unrepresentable.add("order_version_missing")
  if (operations.length === 0 || unrepresentable.size > 0) return { request: null, unrepresentable: [...unrepresentable] }
  const request: PatchRequest = { page_id: context.pageId, schema_version: context.schemaVersion, base_rev: context.baseRev, mutation_id: context.mutationId, operations }
  if (context.orderVersion !== undefined) request.if_order_version = context.orderVersion
  // Replay what we would send and compare with the draft: anything not reproduced is reported.
  if (JSON.stringify(ignored(applyPatch(base, request), unrepresentable)) !== JSON.stringify(ignored(next, unrepresentable))) return { request: null, unrepresentable: ["unreproduced"] }
  return { request, unrepresentable: [] }
}
