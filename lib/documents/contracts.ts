/** Public Document routes use opaque epochs after the authorized read. Initialize
 * alone returns a numeric durable fence; it must never hydrate the editor. */
export interface DocumentNode {
  type: string
  text?: string
  attrs?: Record<string, unknown>
  marks?: { type: string; attrs?: { href?: string } }[]
  content?: DocumentNode[]
}
export interface DocumentState {
  page_id: string; schema: number; rev: number; epoch: string
  generation: number; incarnation: string; snapshot: string; snapshotRev: number
  updates: string[]; blocks: {id: string; hash: string}[]
  content: DocumentNode
  outline: {id: string; type: string; parent: string | null; hash: string; text: string}[]
  can_write: boolean; can_comment: boolean; can_resolve: boolean
}
export interface MutationAttempt { id: string; epoch: string; schema: number; baselineRev: number }
export interface MutationReceipt { id: string; epoch: string; rev: number; schema?: number }
function record(value: unknown): Record<string, unknown> {
  if (!value || typeof value !== "object" || Array.isArray(value)) throw new Error("Document response could not be confirmed.")
  return value as Record<string, unknown>
}
const revision = (value: unknown): value is number => Number.isSafeInteger(value) && (value as number) >= 0
export function verifyMutationReceipt(value: unknown, attempt: MutationAttempt): MutationReceipt {
  const receipt = record(value)
  if (receipt.id !== attempt.id || receipt.epoch !== attempt.epoch || !revision(receipt.rev) || receipt.rev <= attempt.baselineRev || (receipt.schema !== undefined && receipt.schema !== attempt.schema)) {
    throw new Error("Document update could not be confirmed. Your draft remains unsaved.")
  }
  return receipt as unknown as MutationReceipt
}
export function verifyInitialization(value: unknown, id: string): void {
  const state = record(value)
  if (!revision(state.rev) || !revision(state.epoch)) throw new Error("Document initialization could not be confirmed. Retry the original request.")
  if (state.id !== undefined) {
    if (state.id !== id || typeof state.actor !== "string" || state.kind !== "init" || state.found !== true || state.duplicate !== true) throw new Error("Document initialization receipt could not be confirmed.")
  } else if (state.schema !== 3 || !revision(state.generation) || typeof state.incarnation !== "string" || !state.incarnation || typeof state.snapshot !== "string" || !revision(state.snapshotRev) || !Array.isArray(state.updates) || state.updates.some(item=>typeof item !== "string") || !Array.isArray(state.blocks)) {
    throw new Error("Document initialization state could not be confirmed.")
  }
}
