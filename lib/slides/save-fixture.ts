import { applyPatch, decodePatch, SlidesConflictError } from "./operations"
import type { SlidesSaveClient, SlidesSaveResult, SlidesSnapshot } from "./save-client"

/** In-memory stand-in for the future endpoint: base_rev/order CAS plus mutation_id replay. */
export function createFixtureSaveClient(initial: SlidesSnapshot): SlidesSaveClient & { head(): SlidesSnapshot; advance(next: SlidesSnapshot): void } {
  let head = structuredClone(initial)
  const receipts = new Map<string, SlidesSaveResult>()
  return {
    head: () => structuredClone(head),
    advance: next => { head = structuredClone(next) },
    load: async () => structuredClone(head),
    patch: async raw => {
      let request
      try { request = decodePatch(raw) } catch (error) { return { status: "error", retryable: false, message: (error as Error).message } }
      const replay = receipts.get(request.mutation_id)
      if (replay) return structuredClone(replay)
      if (request.base_rev !== head.rev || (request.if_order_version !== undefined && request.if_order_version !== head.orderVersion)) return { status: "conflict", current: structuredClone(head) }
      try {
        const deck = applyPatch(head.deck, request)
        const structural = request.operations.some(op => op.op === "insert_slide" || op.op === "delete_slide")
        head = { deck, rev: head.rev + 1, orderVersion: head.orderVersion + (structural ? 1 : 0) }
        const result: SlidesSaveResult = { status: "saved", snapshot: structuredClone(head), mutationId: request.mutation_id }
        receipts.set(request.mutation_id, result)
        return structuredClone(result)
      } catch (error) {
        if (error instanceof SlidesConflictError) return { status: "conflict", current: structuredClone(head) }
        return { status: "error", retryable: false, message: (error as Error).message }
      }
    },
  }
}
