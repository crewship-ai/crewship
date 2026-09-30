import { realWorkspace } from "@/lib/admin-api"

/**
 * The workspace part of an admin request. An instance administrator need not
 * belong to any workspace, and the instance-wide admin routes answer them with
 * none; so a missing workspace leaves the parameter off instead of sending
 * `workspace_id=null` or doing nothing.
 *
 *   wsParam("ws-1")  → "workspace_id=ws-1"
 *   wsParam(null)    → ""
 *   withWs("/api/v1/admin/health", id) → "/api/v1/admin/health?workspace_id=…" or the bare path
 */
export function wsParam(workspaceId: string | null | undefined): string {
  const id = realWorkspace(workspaceId)
  return id ? `workspace_id=${encodeURIComponent(id)}` : ""
}

export function withWs(path: string, workspaceId: string | null | undefined): string {
  const p = wsParam(workspaceId)
  if (!p) return path
  return path + (path.includes("?") ? "&" : "?") + p
}
