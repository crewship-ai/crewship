/**
 * Atomic move into a working folder (Q15). PROPOSED contract (FE-REQ-F4-2);
 * the route does not exist yet, so every caller sits behind `artifactCopyMove()`.
 *
 * POST /api/v1/pages/{id}/move?workspace_id=...
 *   { target_folder, pages_version, acl_version }
 *   200 { page: { id, slug, folder, private_audience } }  same id; folder and
 *       audience change in one transaction, rights re-checked at confirm
 *   403 no write on target / not the owner · 404 absent or private to others
 *   409 stale pages or folder permissions version · 503 unavailable
 */
import { apiFetch } from "@/lib/api-fetch"

export interface MoveRequest {
  id: string
  targetFolder: string
  pagesVersion: number | null
  aclVersion: number | null
}

export class MoveError extends Error {
  constructor(readonly status: number, message: string) {
    super(message)
    this.name = "MoveError"
  }
}

export async function movePage(workspaceId: string, req: MoveRequest): Promise<void> {
  const query = new URLSearchParams({ workspace_id: workspaceId }).toString()
  const res = await apiFetch(`/api/v1/pages/${encodeURIComponent(req.id)}/move?${query}`, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ target_folder: req.targetFolder, pages_version: req.pagesVersion, acl_version: req.aclVersion }),
  })
  if (res.ok) return
  const body = await res.json().catch(() => null) as { error?: unknown } | null
  throw new MoveError(res.status, typeof body?.error === "string" ? body.error : `Move failed (${res.status}).`)
}
