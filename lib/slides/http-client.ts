import { apiFetch } from "@/lib/api-fetch"
import type { SlideDeck } from "./model"
import type { PatchRequest } from "./operations"
import type { SlidesSaveClient, SlidesSaveResult, SlidesSnapshot } from "./save-client"
import { validateSlideDeck } from "./validate"

// HTTP binding of the proposed contract (coord FE-REQ-F5-1). None of these
// routes exist on the server yet; every response is checked before it is
// believed, and a missing route is reported as such instead of as "not found".
const object = (value: unknown): value is Record<string, unknown> => Boolean(value) && typeof value === "object" && !Array.isArray(value)

const MISMATCH = { status: "error", retryable: false, message: "The presentation does not belong to this Page." } as const
const clip = (text: string) => (text.length > 200 ? `${text.slice(0, 200)}…` : text)

// The read answers `revision` (backend branch feat/slides-human-read-create-adapter, no page_id);
// the proposed PATCH answers `rev`. Either is accepted, and both must agree when both are present.
function snapshotOf(body: unknown, pageId: string): SlidesSnapshot | typeof MISMATCH | null {
  if (!object(body)) return null
  const rev = body.revision ?? body.rev
  if (body.revision !== undefined && body.rev !== undefined && body.revision !== body.rev) return null
  if (!Number.isSafeInteger(rev) || (rev as number) < 0 || !Number.isSafeInteger(body.order_version) || (body.order_version as number) < 0) return null
  try { validateSlideDeck(body.deck) } catch { return null }
  // The Page binding comes from the request path and deck_id; a page_id field, when sent, must agree.
  if ((body.deck as SlideDeck).deck_id !== pageId || (body.page_id !== undefined && body.page_id !== pageId)) return MISMATCH
  return { deck: body.deck as SlideDeck, rev: rev as number, orderVersion: body.order_version as number }
}

const json = async (response: Response): Promise<unknown> => { try { return await response.json() } catch { return undefined } }
/**
 * Heuristic, not a contract: today an unknown route answers 404 text/plain (Go's default
 * mux), while a missing or private Page must answer a JSON 404 (FE-REQ-F5-1 addendum). If
 * the real handler answered a missing Page with text/plain, it would read as "not available
 * on this server yet" instead of "not available". 405 and 501 are also read as a missing route.
 */
const routeMissing = (response: Response) => response.status === 405 || response.status === 501 || (response.status === 404 && !(response.headers.get("content-type") ?? "").includes("json"))

export function createHttpSlidesClient(workspaceId: string): SlidesSaveClient {
  const url = (pageId: string) => `/api/v1/pages/${encodeURIComponent("@" + pageId)}/slides?${new URLSearchParams({ workspace_id: workspaceId })}`
  const failure = (response: Response): Extract<SlidesSaveResult, { status: "unavailable" | "error" }> =>
    routeMissing(response) ? { status: "unavailable", reason: "not_implemented" }
      : response.status === 401 ? { status: "unavailable", reason: "signed_out" }
      : response.status === 404 ? { status: "unavailable", reason: "not_found" }
      : response.status === 403 ? { status: "unavailable", reason: "forbidden" }
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
    async patch(request: PatchRequest) {
      let response: Response
      try { response = await apiFetch(url(request.page_id), { method: "PATCH", headers: { "Content-Type": "application/json" }, body: JSON.stringify(request) }) } catch { return { status: "error", retryable: true, message: "Saving failed." } }
      const body = await json(response)
      if (response.status === 409) {
        const current = snapshotOf(body, request.page_id)
        if (current === MISMATCH) return MISMATCH
        return current ? { status: "conflict", current } : { status: "error", retryable: false, message: "The presentation changed and the latest version could not be read." }
      }
      if (response.status === 400) return { status: "error", retryable: false, message: object(body) && typeof body.error === "string" && body.error.trim() ? clip(body.error) : "The server rejected this change." }
      if (!response.ok) return failure(response)
      const snapshot = snapshotOf(body, request.page_id)
      if (snapshot === MISMATCH) return MISMATCH
      // A 2xx that is not a verifiable acknowledgement is never reported as saved.
      if (!snapshot || !object(body) || body.mutation_id !== request.mutation_id || snapshot.rev <= request.base_rev) return { status: "error", retryable: true, message: "Saving could not be confirmed. Try again." }
      return { status: "saved", snapshot, mutationId: request.mutation_id }
    },
  }
}
