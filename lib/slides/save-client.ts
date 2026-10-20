import type { SlideDeck } from "./model"
import type { PatchRequest } from "./operations"

// Proposed client contract for the future Slides save endpoint (no HTTP route
// exists yet, see coord FE-REQ-F5-1). The UI depends only on this interface.
export function slidesEditorSave(): boolean {
  return process.env.NEXT_PUBLIC_SLIDES_EDITOR_SAVE === "true"
}

export interface SlidesSnapshot { deck: SlideDeck; rev: number; orderVersion: number }
export type SlidesSaveResult =
  | { status: "saved"; snapshot: SlidesSnapshot; mutationId: string }
  /** 409: the base moved; `current` is the fresh confirmed head to rebase onto. */
  | { status: "conflict"; current: SlidesSnapshot }
  /** 404 (absent or private to the caller; never distinguished) or 403 (read-only). */
  | { status: "unavailable"; reason: "not_found" | "forbidden" }
  /** 503 or network failure; the draft is retained and the same mutation_id is retried. */
  | { status: "error"; retryable: boolean; message: string }

export interface SlidesSaveClient {
  load(pageId: string): Promise<SlidesSnapshot | Extract<SlidesSaveResult, { status: "unavailable" | "error" }>>
  /** Idempotent by `mutation_id`: a retry returns the original result. */
  patch(request: PatchRequest): Promise<SlidesSaveResult>
}
