"use client"

import { useEffect } from "react"
import { useRouter } from "next/navigation"
import { useQuery } from "@tanstack/react-query"
import { useSessionSafe } from "@/hooks/use-auth"
import { apiFetch } from "@/lib/api-fetch"

/** Resolve saved identities only through the current human's authorized catalog. */
export function PageReferenceEntry({ workspaceId, search }: { workspaceId: string; search: string }) {
  const router = useRouter()
  const { data: session } = useSessionSafe()
  const humanId = session?.user?.id
  const params = new URLSearchParams(search)
  const id = params.get("ref") ?? ""
  const valid = params.getAll("ref").length === 1 && /^[A-Za-z0-9_-]{1,128}$/.test(id)
    && params.getAll("workspace_id").length === 1 && params.get("workspace_id") === workspaceId
  const result = useQuery({
    queryKey: ["page-reference-entry", workspaceId, humanId ?? "", id],
    enabled: valid && Boolean(humanId),
    gcTime: 0,
    retry: false,
    queryFn: async ({ signal }) => {
      const response = await apiFetch(`/api/v1/pages?${new URLSearchParams({ workspace_id: workspaceId })}`, { signal })
      if (!response.ok) throw new Error("Document unavailable.")
      const pages: unknown = await response.json()
      if (!Array.isArray(pages)) throw new Error("Document unavailable.")
      const page = pages.find((item: unknown) => item && typeof item === "object" && "id" in item && item.id === id)
      return page && typeof page.slug === "string" && page.slug ? page.slug : null
    },
  })
  useEffect(() => {
    if (valid && humanId && result.isSuccess && result.data) {
      router.replace(`/pages/${encodeURIComponent(result.data)}?${new URLSearchParams({ workspace_id: workspaceId })}`)
    }
  }, [valid, humanId, result.isSuccess, result.data, router, workspaceId])
  if (!valid || !humanId || result.isError || result.isSuccess && !result.data) {
    return <p role="status" className="p-6 text-muted-foreground">Document unavailable.</p>
  }
  return <p role="status" className="p-6 text-muted-foreground">Opening document…</p>
}
