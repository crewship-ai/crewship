import { record, revision } from "./channel"

/** Confirmed revision notifications contain identity and revision only; content
 * is reread through the host's authorized transport for the bound document. */
export function parseRevisionPush(payload: unknown, artifactId: string): { rev: number; generation: number } | null {
  if (!record(payload) || payload.artifact_id !== artifactId || !revision(payload.rev) || !revision(payload.generation) || payload.generation < 1) return null
  return { rev: payload.rev, generation: payload.generation }
}
