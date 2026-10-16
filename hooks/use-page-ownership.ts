"use client"

import { useCallback, useEffect, useRef, useState } from "react"
import { useMutation, useInfiniteQuery, useQueryClient, type InfiniteData } from "@tanstack/react-query"
import { apiFetch } from "@/lib/api-fetch"
import { isAdminTier } from "@/lib/permissions/tiers"
import { ownershipAccessDenied, PageOwnershipError, pageOwnershipListSchema, pageOwnershipSchema, type PageOwnershipTransfer, type PageOwnershipList } from "@/lib/page-ownership"
import { useRealtimeEventSafe } from "./use-realtime"

export const pageOwnershipKeys = { workspace: (workspace: string) => ["page-ownership", workspace] as const }

export function usePageOwnership(workspace: string, role: string | null, owner = "") {
  const client = useQueryClient()
  const canManage = isAdminTier(role)
  const [review, setReview] = useState(0)
  const identity = JSON.stringify([workspace, role, owner, review])
  const [denial, setDenial] = useState<{ identity: string; error: PageOwnershipError } | null>(null)
  const stopped = denial?.identity === identity ? denial.error : null
  const active = useRef<AbortController | null>(null)
  useEffect(() => () => active.current?.abort(), [workspace, role])
  const key = [...pageOwnershipKeys.workspace(workspace), role, owner, review] as const
  const endpoint = `/api/v1/workspaces/${encodeURIComponent(workspace)}/page-ownership`
  const query = useInfiniteQuery<PageOwnershipList, Error, InfiniteData<PageOwnershipList>, typeof key, string>({
    queryKey: key, enabled: Boolean(workspace && canManage && !stopped), retry: false, gcTime: 0,
    initialPageParam: "", getNextPageParam: last => last.next_cursor || undefined,
    refetchInterval: 60000, refetchIntervalInBackground: false,
    queryFn: async ({ signal, pageParam }): Promise<PageOwnershipList> => {
      const response = await apiFetch(`${endpoint}?${new URLSearchParams({ workspace_id: workspace, limit: "25", ...(owner ? { owner_user_id: owner } : {}), ...(pageParam ? { cursor: pageParam } : {}) })}`, { signal })
      const body = await response.json().catch(() => null)
      if (!response.ok) throw new PageOwnershipError(body?.error ?? "Could not load output ownership.", response.status)
      const parsed = pageOwnershipListSchema.safeParse(body)
      if (!parsed.success) throw new PageOwnershipError("Could not read output ownership.")
      return parsed.data
    },
  })
  const denied = ownershipAccessDenied(query.error)
  const visible = canManage && !denied && !stopped
  useEffect(() => {
    if (denied) setDenial({ identity, error: query.error as PageOwnershipError })
    if (!canManage || denied) client.removeQueries({ queryKey: pageOwnershipKeys.workspace(workspace) })
  }, [client, workspace, canManage, denied, identity, query.error])
  const invalidate = useCallback(() => {
    void client.invalidateQueries({ queryKey: pageOwnershipKeys.workspace(workspace) })
  }, [client, workspace])
  useRealtimeEventSafe("page.updated", invalidate)
  useRealtimeEventSafe("page.deleted", invalidate)
  useRealtimeEventSafe("workspace.updated", invalidate)
  useRealtimeEventSafe("realtime.reconnected", invalidate)
  const transfer = useMutation({
    retry: false,
    mutationFn: async ({ pageId, ...input }: PageOwnershipTransfer) => {
      if (!visible) throw new PageOwnershipError("Output ownership access is unavailable.", 403)
      const controller = new AbortController()
      active.current = controller
      const timer = setTimeout(() => controller.abort(), 20000)
      try {
        const response = await apiFetch(`${endpoint}/${encodeURIComponent(pageId)}?${new URLSearchParams({ workspace_id: workspace })}`, {
          method: "POST", signal: controller.signal, headers: { "Content-Type": "application/json" }, body: JSON.stringify(input),
        })
        const body = await response.json().catch(() => null)
        if (controller.signal.aborted) throw new Error("aborted")
        if (!response.ok) throw new PageOwnershipError(body?.error ?? "Could not transfer ownership.", response.status)
        const parsed = pageOwnershipSchema.safeParse(body)
        if (!parsed.success || parsed.data.id !== pageId || parsed.data.owner_user_id !== input.successor_user_id) throw new Error("unconfirmed")
        return parsed.data
      } catch (error) {
        throw error instanceof PageOwnershipError ? error : new PageOwnershipError("Transfer could not be confirmed. Refresh ownership before trying again.")
      } finally { clearTimeout(timer); active.current = null }
    },
    onSettled: (_data, error) => {
      if (ownershipAccessDenied(error)) {
        setDenial({ identity, error: error as PageOwnershipError })
        client.removeQueries({ queryKey: pageOwnershipKeys.workspace(workspace) })
      } else invalidate()
      for (const resource of ["pages", "page-artifact", "page-board"]) void client.invalidateQueries({ queryKey: [resource, workspace] })
    },
  })
  return { canManage, identity, data: visible ? query.data?.pages.flatMap(page => page.pages) : undefined, error: stopped ?? query.error, query, transfer, reviewLatest: () => { transfer.reset(); setReview(value => value + 1) } }
}
