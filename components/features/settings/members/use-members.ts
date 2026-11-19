"use client"

import { useCallback } from "react"
import { useQuery, useQueryClient } from "@tanstack/react-query"
import { toast } from "@/lib/toast"

import { apiFetch } from "@/lib/api-fetch"
import { memberCapabilitiesKey } from "@/components/admin/member-capabilities"
import type { Member } from "./members-model"

/** A private artifact the person owns, and who may take it over. */
export interface PageToHandOver {
  id: string
  name: string
  type: string
  successors: { userId: string; name: string; email: string }[]
}
/**
 * What removing a member came to. A 409 `page_ownership_transfer_required`
 * means they own private artifacts nobody else owns yet: an OWNER's answer
 * lists them with the people who may take each over (`pages`); anyone else
 * gets the server's sentence only (an Owner has to choose), so `pages` is
 * empty then.
 */
export type RemoveOutcome =
  | { ok: true }
  | { ok: false; handOver?: { message: string; pages: PageToHandOver[] } }

const PAGE_TRANSFER = "page_ownership_transfer_required"

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
    if (workspaceId) void queryClient.invalidateQueries({ queryKey: memberCapabilitiesKey(workspaceId) })
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

  /**
   * DELETE the membership. A refusal that only needs a successor for private
   * artifacts is returned (`handOver`), not toasted: the dialog asks who takes
   * them over and tries again. Every other refusal is a toast.
   */
  const tryRemove = useCallback(async (member: Member): Promise<RemoveOutcome> => {
    if (!workspaceId) return { ok: false }
    try {
      const res = await apiFetch(
        `/api/v1/workspaces/${workspaceId}/members/${member.id}?workspace_id=${workspaceId}`,
        { method: "DELETE" },
      )
      // apiFetch resolves on 4xx/5xx — only a network failure rejects.
      if (!res.ok) {
        const body = await res.json().catch(() => null)
        if (res.status === 409 && body?.code === PAGE_TRANSFER) {
          const pages: PageToHandOver[] = Array.isArray(body.pages) ? body.pages.flatMap((p: Record<string, unknown>) =>
            typeof p?.id === "string" ? [{
              id: p.id, name: typeof p.name === "string" && p.name ? p.name : "Untitled", type: typeof p.type === "string" ? p.type : "",
              successors: Array.isArray(p.eligible_successors) ? p.eligible_successors.flatMap((c: Record<string, unknown>) =>
                typeof c?.user_id === "string" ? [{ userId: c.user_id, name: typeof c.name === "string" ? c.name : "", email: typeof c.email === "string" ? c.email : "" }] : []) : [],
            }] : []) : []
          return { ok: false, handOver: { message: typeof body.error === "string" ? body.error : "Their private artifacts need a new owner first.", pages } }
        }
        toast.error(typeof body?.error === "string" ? body.error : "Failed to remove member")
        return { ok: false }
      }
      toast.success("Member removed")
      refresh()
      return { ok: true }
    } catch {
      toast.error("Failed to remove member")
      return { ok: false }
    }
  }, [workspaceId, refresh])

  /** DELETE the membership. Resolves true when the server took it. */
  const remove = useCallback(async (member: Member): Promise<boolean> => {
    const outcome = await tryRemove(member)
    if (!outcome.ok && outcome.handOver) toast.error(outcome.handOver.message)
    return outcome.ok
  }, [tryRemove])

  /** Hand one private artifact of `member` to `successorUserId`
   *  (POST …/page-ownership/{pageID}). Resolves to the server's refusal, or
   *  null when it took. */
  const handOverPage = useCallback(async (member: Member, pageId: string, successorUserId: string): Promise<string | null> => {
    if (!workspaceId) return "No workspace"
    try {
      const res = await apiFetch(
        `/api/v1/workspaces/${encodeURIComponent(workspaceId)}/page-ownership/${encodeURIComponent(pageId)}?workspace_id=${encodeURIComponent(workspaceId)}`,
        {
          method: "POST",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify({ expected_owner_user_id: member.user.id, successor_user_id: successorUserId }),
        },
      )
      if (res.ok) return null
      const body = await res.json().catch(() => null)
      return typeof body?.error === "string" ? body.error : `The artifact could not be handed over (${res.status})`
    } catch {
      return "Couldn’t reach the server"
    }
  }, [workspaceId])

  return {
    members: query.data ?? [],
    loading: query.isLoading,
    error: query.isError ? "Failed to load members" : null,
    refresh, changeRole, remove, tryRemove, handOverPage,
  }
}
