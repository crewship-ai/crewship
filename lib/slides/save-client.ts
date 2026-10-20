import type { SlideDeck } from "./model"
import type { PatchRequest } from "./operations"

// Client contract of the Slides endpoints. The server has Read and Initialize only
// (backend decision F5-7); `patch` is a proposed save contract that no HTTP route
// serves yet (coord FE-REQ-F5-1), so only the in-memory fixture implements it.
/** The confirmed head. `rev` is the server's `revision`; `contentHash` is present for server reads. */
export interface SlidesSnapshot { deck: SlideDeck; rev: number; orderVersion: number; contentHash?: string }

/** Initialize takes exactly these five fields; `deck.deck_id` must equal `page_id`. */
export interface SlidesInitializeRequest { page_id: string; schema_version: number; base_rev: 0; mutation_id: string; deck: SlideDeck }
export interface SlidesReceipt { mutationId: string; revision: number; orderVersion: number; contentHash: string; duplicate: boolean }
export type SlidesInitializeResult =
  | { status: "initialized"; receipt: SlidesReceipt }
  | Extract<SlidesSaveResult, { status: "unavailable" | "error" }>
export type SlidesSaveResult =
  | { status: "saved"; snapshot: SlidesSnapshot; mutationId: string }
  /** 409: the base moved; `current` is the fresh confirmed head to rebase onto. */
  | { status: "conflict"; current: SlidesSnapshot }
  /** 404 (absent or private to the caller; never distinguished) or 403 (read-only). */
  | { status: "unavailable"; reason: "not_found" | "forbidden" | "signed_out" }
  /** The server has no Slides route (non-JSON 404, 405 or 501). Never shown as success or as "not found". */
  | { status: "unavailable"; reason: "not_implemented" }
  /** 503 or network failure; the draft is retained and the same mutation_id is retried. */
  | { status: "error"; retryable: boolean; message: string }

export interface SlidesSaveClient {
  load(pageId: string): Promise<SlidesSnapshot | Extract<SlidesSaveResult, { status: "unavailable" | "error" }>>
  /** Idempotent by `mutation_id`: a retry returns the original result. */
  patch(request: PatchRequest): Promise<SlidesSaveResult>
}

/** What the real server offers today: Read and Initialize. There is no save. */
export interface SlidesHttpClient extends SlidesSaveClient {
  initialize(request: SlidesInitializeRequest): Promise<SlidesInitializeResult>
}
