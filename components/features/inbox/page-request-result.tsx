"use client"

import Link from "next/link"
import { useQuery } from "@tanstack/react-query"
import { apiFetch } from "@/lib/api-fetch"
import { Button } from "@/components/ui/button"
import type { InboxItem } from "@/hooks/use-inbox"

// Notification payloads are pointers, never page access or navigation authority.
export function PageRequestResult({ item }: { item: InboxItem }) {
  const kind = item.payload?.request_kind
  const id = item.payload?.request_id
  const valid = item.kind === "message" && item.payload?.subkind === "page_request_result"
    && (kind === "artifact" || kind === "board") && typeof id === "string" && id.length > 0 && id.length <= 128
  const query = useQuery({
    queryKey: ["page-request-result", item.workspace_id, item.id, kind, id],
    enabled: valid, retry: false, gcTime: 0, staleTime: 0,
    queryFn: async ({ signal }) => {
      const response = await apiFetch(`/api/v1/page-request-results/${encodeURIComponent(String(kind))}/${encodeURIComponent(String(id))}?${new URLSearchParams({ workspace_id: item.workspace_id })}`, { signal })
      if (!response.ok) throw new Error("The result is unavailable or you no longer have access.")
      const body: { page_slug?: unknown; revision?: unknown } = await response.json()
      if (typeof body.page_slug !== "string" || !body.page_slug || body.page_slug.length > 256) throw new Error("The result is unavailable.")
      const revision = typeof body.revision === "number" && Number.isSafeInteger(body.revision) && body.revision > 0 ? `?revision=${body.revision}` : ""
      return `/pages/${encodeURIComponent(body.page_slug)}${revision}`
    },
  })
  if (!valid) return null
  if (query.error) return <p role="status" className="text-xs text-muted-foreground">The result is unavailable or you no longer have access.</p>
  if (!query.data) return <p role="status" className="text-xs text-muted-foreground">Checking result access…</p>
  return <Button asChild size="sm"><Link href={query.data}>View result</Link></Button>
}
