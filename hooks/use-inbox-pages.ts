"use client"

import { useCallback, useMemo } from "react"
import { useInfiniteQuery } from "@tanstack/react-query"

import { apiFetch } from "@/lib/api-fetch"
import type { InboxItem, InboxListResponse, InboxSpaceCounts } from "@/hooks/use-inbox"

// The key sits under ["inbox", workspaceId], the prefix hooks/use-inbox.ts
// invalidates on every inbox.updated event, so these pages refresh with the rest.

/** What a paged, server-side read of the list is asked (see internal/api/inbox_query.go). */
export interface InboxPagesParams {
  space?: keyof InboxSpaceCounts
  /** Search text; trimmed, empty means no search. */
  q?: string
}

const INBOX_PAGES_SIZE = 100

/**
 * The list read page by page with a keyset cursor, for what the background
 * walk does not load: older history ("Load more") and server-side search.
 * `items` is every page fetched so far, newest first. Disabled (and empty)
 * until `enabled`, so a view that needs nothing extra costs no request.
 */
export function useInboxPages(workspaceId: string | null | undefined, params: InboxPagesParams, enabled = true) {
  const q = params.q?.trim() ?? ""
  const normalised: InboxPagesParams = { space: params.space, q: q || undefined }
  const query = useInfiniteQuery<InboxListResponse, Error, { pages: InboxListResponse[] }, readonly ["inbox", string, "pages", InboxPagesParams], string>({
    queryKey: ["inbox", workspaceId ?? "", "pages", normalised] as const,
    initialPageParam: "",
    retry: false,
    enabled: Boolean(workspaceId) && enabled,
    queryFn: async ({ pageParam, signal }) => {
      const search = new URLSearchParams({ workspace_id: workspaceId!, state: "all", limit: String(INBOX_PAGES_SIZE) })
      if (normalised.space) search.set("space", normalised.space)
      if (normalised.q) search.set("q", normalised.q)
      if (pageParam) search.set("cursor", pageParam)
      const res = await apiFetch(`/api/v1/inbox?${search.toString()}`, { signal })
      if (!res.ok) throw new Error(`inbox: ${res.status}`)
      return (await res.json()) as InboxListResponse
    },
    getNextPageParam: (last) => (last.has_more && last.next_cursor ? last.next_cursor : undefined),
  })
  const items = useMemo(() => {
    const seen = new Set<string>()
    const out: InboxItem[] = []
    for (const page of query.data?.pages ?? []) {
      for (const row of page.rows ?? []) {
        if (seen.has(row.id)) continue
        seen.add(row.id)
        out.push(row)
      }
    }
    return out
  }, [query.data])
  const { fetchNextPage } = query
  const loadMore = useCallback(() => { void fetchNextPage() }, [fetchNextPage])
  return {
    items,
    hasMore: Boolean(query.hasNextPage),
    loadMore,
    /** The first page is on its way. */
    loading: enabled && query.isPending && query.fetchStatus !== "idle",
    /** A "Load more" page is on its way. */
    loadingMore: query.isFetchNextPageError ? false : query.isFetchingNextPage,
    error: query.error ? query.error.message : null,
  }
}
