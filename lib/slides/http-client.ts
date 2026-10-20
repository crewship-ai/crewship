import { apiFetch } from "@/lib/api-fetch"
import type { SlideDeck } from "./model"
import type { SlidesHttpClient, SlidesInitializeRequest, SlidesInitializeResult, SlidesReceipt, SlidesSaveResult, SlidesSnapshot } from "./save-client"
import { deckHash } from "./operations"
import { HASH_PATTERN, isIdentifier, isSupportedSchema, validateSlideDeck } from "./validate"

// HTTP binding of the backend contract (decision F5-7, PR #3837): Read and Initialize
// only. There is no PATCH, order or agent route, so `patch` never touches the network.
// Every response is checked before it is believed, and a missing route is reported as such.
const object = (value: unknown): value is Record<string, unknown> => Boolean(value) && typeof value === "object" && !Array.isArray(value)
/**
 * Every required key must be present. Unknown keys are ignored: the server may add fields to an
 * answer (its strict decoder applies to requests only), and the deck itself is still validated strictly.
 */
const hasAll = (value: Record<string, unknown>, keys: readonly string[]) => keys.every(key => Object.hasOwn(value, key))
const positive = (value: unknown): value is number => Number.isSafeInteger(value) && (value as number) >= 1

const MISMATCH = { status: "error", retryable: false, message: "The presentation does not belong to this Page." } as const
const clip = (text: string) => (text.length > 200 ? `${text.slice(0, 200)}…` : text)

const READ_FIELDS = ["schema_version", "revision", "order_version", "content_hash", "deck"] as const
const RECEIPT_FIELDS = ["mutation_id", "revision", "order_version", "content_hash", "duplicate"] as const
const INITIALIZE_FIELDS = ["page_id", "schema_version", "base_rev", "mutation_id", "deck"] as const

/**
 * Identity is `deck.deck_id`, which must be the requested immutable Page ID; there is no
 * `page_id` and no `rev` on the wire. A mismatch is an error state, never a fallback.
 */
function snapshotOf(body: unknown, pageId: string): SlidesSnapshot | typeof MISMATCH | null {
  if (!object(body) || !hasAll(body, READ_FIELDS)) return null
  if (!positive(body.revision) || !positive(body.order_version) || typeof body.content_hash !== "string" || !HASH_PATTERN.test(body.content_hash)) return null
  try { validateSlideDeck(body.deck) } catch { return null }
  const deck = body.deck as SlideDeck
  if (!isSupportedSchema(body.schema_version) || body.schema_version !== deck.schema_version) return null
  if (deck.deck_id !== pageId) return MISMATCH
  return { deck, rev: body.revision, orderVersion: body.order_version, contentHash: body.content_hash }
}

function receiptOf(body: unknown, mutationId: string, sentHash: string): SlidesReceipt | null {
  if (!object(body) || !hasAll(body, RECEIPT_FIELDS)) return null
  // The receipt must be for this mutation and for the content that was sent.
  if (body.mutation_id !== mutationId || body.content_hash !== sentHash || !positive(body.revision) || !positive(body.order_version) || typeof body.content_hash !== "string" || !HASH_PATTERN.test(body.content_hash) || typeof body.duplicate !== "boolean") return null
  return { mutationId, revision: body.revision, orderVersion: body.order_version, contentHash: body.content_hash, duplicate: body.duplicate }
}

/** The five-field request, checked before transport; null when the contract would refuse it. */
function initializeBody(request: unknown): SlidesInitializeRequest | null {
  if (!object(request) || Object.keys(request).length !== INITIALIZE_FIELDS.length || !hasAll(request, INITIALIZE_FIELDS)) return null
  if (!isIdentifier(request.page_id) || !isIdentifier(request.mutation_id) || request.base_rev !== 0 || request.schema_version !== 1) return null
  try { validateSlideDeck(request.deck) } catch { return null }
  const deck = request.deck as SlideDeck
  return deck.deck_id === request.page_id && deck.schema_version === request.schema_version ? (request as unknown as SlidesInitializeRequest) : null
}

const json = async (response: Response): Promise<unknown> => { try { return await response.json() } catch { return undefined } }
/**
 * Heuristic, not a contract: with CREWSHIP_SLIDES_ENABLED off the routes are not installed,
 * so Go's mux answers a plain-text 404 (not a 503), while a missing or private Page answers a JSON 404
 * (`{"error": ...}`). 405 and 501 are also read as a missing route.
 */
const routeMissing = (response: Response) => response.status === 405 || response.status === 501 || (response.status === 404 && !(response.headers.get("content-type") ?? "").includes("json"))

export function createHttpSlidesClient(workspaceId: string): SlidesHttpClient {
  const scope = new URLSearchParams({ workspace_id: workspaceId })
  const url = (pageId: string, suffix = "") => `/api/v1/pages/${encodeURIComponent("@" + pageId)}/slides${suffix}?${scope}`
  const failure = (response: Response): Extract<SlidesSaveResult, { status: "unavailable" | "error" }> =>
    routeMissing(response) ? { status: "unavailable", reason: "not_implemented" }
      : response.status === 401 ? { status: "unavailable", reason: "signed_out" }
      : response.status === 404 ? { status: "unavailable", reason: "not_found" }
      : response.status === 403 ? { status: "unavailable", reason: "forbidden" }
      : response.status === 409 ? { status: "error", retryable: false, message: "This presentation conflicts with what the server holds or uses a format this editor cannot read." }
      : response.status === 429 ? { status: "error", retryable: false, message: "The server's Slides storage limit has been reached." }
      : { status: "error", retryable: response.status >= 500, message: response.status === 503 ? "Slides are temporarily unavailable." : "The request failed." }
  return {
    async load(pageId) {
      let response: Response
      try { response = await apiFetch(url(pageId), { cache: "no-store" }) } catch { return { status: "error", retryable: true, message: "The presentation could not be loaded." } }
      if (!response.ok) return failure(response)
      const snapshot = snapshotOf(await json(response), pageId)
      if (snapshot === MISMATCH) return MISMATCH
      return snapshot ?? { status: "error", retryable: false, message: "The server returned a presentation this editor cannot read." }
    },
    async initialize(raw): Promise<SlidesInitializeResult> {
      const request = initializeBody(raw)
      if (!request) return { status: "error", retryable: false, message: "This initialization request is not valid." }
      let response: Response
      try { response = await apiFetch(url(request.page_id, "/initialize"), { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify(request) }) } catch { return { status: "error", retryable: true, message: "The presentation could not be saved." } }
      const body = await json(response)
      if (response.status === 400) return { status: "error", retryable: false, message: object(body) && typeof body.error === "string" && body.error.trim() ? clip(body.error) : "The server rejected this presentation." }
      if (!response.ok) return failure(response)
      // A 2xx that is not a verifiable receipt for this mutation is never reported as saved.
      const receipt = receiptOf(body, request.mutation_id, deckHash(request.deck))
      return receipt ? { status: "initialized", receipt } : { status: "error", retryable: true, message: "Saving could not be confirmed. Retry the same request." }
    },
    // No route serves saving yet (F5-7): nothing is sent, so nothing can be mistaken for a save.
    patch: async () => ({ status: "unavailable", reason: "not_implemented" }),
  }
}
