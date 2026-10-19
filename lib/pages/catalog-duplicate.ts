/**
 * Duplicate: the only way to get a second copy (Q15). PROPOSED contract
 * (FE-REQ-F4-1, accepted by the backend with this mutation identity); the
 * route does not exist yet, so callers sit behind `artifactCopyMove()`.
 *
 * POST /api/v1/pages/{id}/duplicate?workspace_id=...
 *   Idempotency-Key: <mutationId>
 *   { mutation_id, expected_source: { id, pages_version } }
 *   201 { page: { id, slug, ... } }  a new ID, private to the caller, unfiled,
 *       without history or the source's grants. Replaying the same mutation_id
 *       after a lost response returns the same copy, never a second one.
 *   403 not allowed to read the source · 404 absent or private to others
 *   409 source is not the expected identity/revision, or the mutation_id was
 *       used for a different source · 503 unavailable
 *
 * The caller owns the key: create it once per user action with
 * `newMutationId()` and send the SAME key on every retry of that action.
 */
import { apiFetch } from "@/lib/api-fetch"

export interface DuplicateResult {
  id: string
  slug: string
}

export interface DuplicateRequest {
  /** Immutable ID of the source. */
  id: string
  /** Stable for one user action; reused on retry. */
  mutationId: string
  /** The source revision the person saw, or null when the list did not carry one. */
  pagesVersion: number | null
}

export function newMutationId(): string {
  return globalThis.crypto.randomUUID()
}

export class DuplicateError extends Error {
  constructor(readonly status: number, message: string) {
    super(message)
    this.name = "DuplicateError"
  }
}

export async function duplicatePage(workspaceId: string, req: DuplicateRequest): Promise<DuplicateResult> {
  const query = new URLSearchParams({ workspace_id: workspaceId }).toString()
  const res = await apiFetch(`/api/v1/pages/${encodeURIComponent(req.id)}/duplicate?${query}`, {
    method: "POST",
    headers: { "Content-Type": "application/json", "Idempotency-Key": req.mutationId },
    body: JSON.stringify({ mutation_id: req.mutationId, expected_source: { id: req.id, pages_version: req.pagesVersion } }),
  })
  const body = await res.json().catch(() => null) as { page?: { id?: unknown; slug?: unknown }; error?: unknown } | null
  if (!res.ok) throw new DuplicateError(res.status, typeof body?.error === "string" ? body.error : `Duplicate failed (${res.status}).`)
  const page = body?.page
  if (typeof page?.id !== "string" || typeof page.slug !== "string") throw new DuplicateError(502, "The copy could not be read back.")
  return { id: page.id, slug: page.slug }
}
