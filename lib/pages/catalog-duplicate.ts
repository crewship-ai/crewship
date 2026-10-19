/**
 * Duplicate — the only way to get a second copy (Q15). PROPOSED contract
 * (FE-REQ-F4-1); the server route does not exist yet, so callers sit behind
 * `artifactCopyMove()`.
 *
 * POST /api/v1/pages/{id}/duplicate?workspace_id=…  {}
 *   201 { page: { id, slug, … } }  the copy: a new ID, private to the caller,
 *                                  history not carried over, unfiled.
 *   403 not allowed to read the source · 404 absent or private to others
 *   409 source changed or caller left the workspace · 503 unavailable
 */
import { apiFetch } from "@/lib/api-fetch"

export interface DuplicateResult {
  id: string
  slug: string
}

export class DuplicateError extends Error {
  constructor(readonly status: number, message: string) {
    super(message)
    this.name = "DuplicateError"
  }
}

export async function duplicatePage(workspaceId: string, id: string): Promise<DuplicateResult> {
  const query = new URLSearchParams({ workspace_id: workspaceId }).toString()
  const res = await apiFetch(`/api/v1/pages/${encodeURIComponent(id)}/duplicate?${query}`, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: "{}",
  })
  const body = await res.json().catch(() => null) as { page?: { id?: unknown; slug?: unknown }; error?: unknown } | null
  if (!res.ok) throw new DuplicateError(res.status, typeof body?.error === "string" ? body.error : `Duplicate failed (${res.status}).`)
  const page = body?.page
  if (typeof page?.id !== "string" || typeof page.slug !== "string") throw new DuplicateError(502, "The copy could not be read back.")
  return { id: page.id, slug: page.slug }
}
