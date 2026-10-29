"use client"

import { useEffect } from "react"
import { useSearchParams } from "next/navigation"
import { useWorkspace } from "@/hooks/use-workspace"

/** A document link selects only a workspace in this account's directory.
 * Wait for the guarded selection before mounting any document requests. */
export function usePageWorkspace() {
  const state = useWorkspace()
  const { workspaceId, workspaces, loading, error, setWorkspaceId } = state
  const params = useSearchParams()
  const ids = params?.getAll("workspace_id") ?? []
  const requested = ids.length === 1 ? ids[0] : null
  const invalid = ids.length > 1 || requested === ""
  const available = requested === workspaceId || (workspaces ?? []).some(workspace => workspace.id === requested)
  const switching = Boolean(requested && available && requested !== workspaceId)

  useEffect(() => {
    if (!loading && !error && !invalid && switching && requested) setWorkspaceId(requested)
  }, [loading, error, setWorkspaceId, invalid, switching, requested])

  return {
    ...state,
    loading: loading || switching,
    linkUnavailable: invalid || (!loading && Boolean(requested) && (!available || error)),
  }
}
