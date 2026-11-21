"use client"

import { useIsInstanceAdmin } from "@/hooks/use-auth"
import { useWorkspace, type WorkspaceData } from "@/hooks/use-workspace"

/**
 * Who may Allow or Deny a waiting request, or Try a request, on Admin ›
 * Security.
 *
 * Both calls — POST /admin/keeper/requests/{id}/resolve and POST
 * /admin/keeper/ask — are `roleManage` on the server (router_internal.go):
 * the caller must be an OWNER or ADMIN *member* of the workspace the call is
 * scoped to, and being an instance administrator is no exception. So the page
 * offers them only where that holds: an instance administrator (the page is
 * theirs) who is a trusted OWNER or ADMIN of that workspace, read from the
 * caller's own memberships (GET /workspaces).
 */
export function mayDecideIn(row: Pick<WorkspaceData, "currentUserRole" | "currentUserAccessMode"> | null | undefined): boolean {
  if (!row || row.currentUserAccessMode === "restricted") return false
  return row.currentUserRole === "OWNER" || row.currentUserRole === "ADMIN"
}

export interface DecideAccess {
  allowed: boolean
  /** The workspace's name, for the sentence that says why not. */
  name: string
}

export function useDecideAccess(workspaceId: string | null | undefined, workspaceName?: string | null): DecideAccess {
  const instanceAdmin = useIsInstanceAdmin()
  const { workspaces } = useWorkspace()
  const row = workspaceId ? (workspaces ?? []).find((w) => w.id === workspaceId) : undefined
  const name = workspaceName || row?.name || "this workspace"
  return { allowed: instanceAdmin === true && mayDecideIn(row), name }
}
