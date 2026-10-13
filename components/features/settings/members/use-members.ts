"use client"

import { useCallback } from "react"
import { useQuery, useQueryClient } from "@tanstack/react-query"
import { toast } from "sonner"

import { apiFetch } from "@/lib/api-fetch"
import type { Member } from "./members-model"

/**
 * The workspace roster, and the two membership writes the page makes (role
 * change, removal) — the same endpoints and the same error handling the
 * in-page Settings section used.
 *
 * A refresh after the first load is SILENT: it keeps the list on screen
 * instead of swapping it for a skeleton. The skeleton swap is what once
 * unmounted the Add member dialog with its one-time setup link still inside
 * it (invite-member-dialog's lifecycle notes) — the roster can wait a beat,
 * a dialog the admin is reading cannot.
 */
export function useMembers(workspaceId: string | null) {
  const queryClient = useQueryClient()
  const query = useQuery({
    queryKey: ["workspace-members", workspaceId],
    enabled: Boolean(workspaceId),
    queryFn: async ({ signal }): Promise<Member[]> => {
      const ws = encodeURIComponent(workspaceId ?? "")
      const res = await apiFetch(`/api/v1/workspaces/${ws}/members?workspace_id=${ws}`, { signal })
      if (!res.ok) throw new Error("Failed to load members")
      const data = await res.json()
      return Array.isArray(data) ? data : []
    },
  })

  // A refresh keeps this workspace's list mounted (and an invite's one-time
  // setup link visible). A different query key never carries the old list
  // into another workspace, even if that workspace's read fails.
  const refresh = useCallback(() => {
    void queryClient.invalidateQueries({ queryKey: ["workspace-members", workspaceId] })
  }, [queryClient, workspaceId])

  /** PATCH the membership's role. Resolves true when the server took it. */
  const changeRole = useCallback(async (member: Member, role: string): Promise<boolean> => {
    if (!workspaceId) return false
    try {
      const res = await apiFetch(
        `/api/v1/workspaces/${workspaceId}/members/${member.id}?workspace_id=${workspaceId}`,
        {
          method: "PATCH",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify({ role }),
        },
      )
      // apiFetch resolves on 4xx/5xx — only a network failure rejects, so a
      // bare `await` would toast every 403 as a successful role change.
      if (!res.ok) {
        const body = await res.json().catch(() => null)
        // The role endpoint returns RFC 7807 problems (`detail`); fall back
        // to the legacy `error` shape for older responses.
        const raw = body?.detail ?? body?.error
        toast.error(typeof raw === "string" ? raw : "Failed to change role")
        return false
      }
      toast.success(`Role changed to ${role}`)
      refresh()
      return true
    } catch {
      toast.error("Failed to change role")
      return false
    }
  }, [workspaceId, refresh])

  /** DELETE the membership. Resolves true when the server took it. */
  const remove = useCallback(async (member: Member): Promise<boolean> => {
    if (!workspaceId) return false
    try {
      const res = await apiFetch(
        `/api/v1/workspaces/${workspaceId}/members/${member.id}?workspace_id=${workspaceId}`,
        { method: "DELETE" },
      )
      // apiFetch resolves on 4xx/5xx — only a network failure rejects.
      if (!res.ok) {
        const body = await res.json().catch(() => null)
        toast.error(typeof body?.error === "string" ? body.error : "Failed to remove member")
        return false
      }
      toast.success("Member removed")
      refresh()
      return true
    } catch {
      toast.error("Failed to remove member")
      return false
    }
  }, [workspaceId, refresh])

  return {
    members: query.data ?? [],
    loading: query.isLoading,
    error: query.isError ? "Failed to load members" : null,
    refresh, changeRole, remove,
  }
}
