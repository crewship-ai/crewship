"use client"

import { useEffect } from "react"
import { useRouter } from "next/navigation"

/** Keep the saved identity in every native read; never substitute a reused slug. */
export function PageReferenceEntry({ workspaceId, search }: { workspaceId: string; search: string }) {
  const router = useRouter()
  const params = new URLSearchParams(search)
  const id = params.get("ref") ?? ""
  const valid = params.getAll("ref").length === 1 && /^[A-Za-z0-9_-]{1,128}$/.test(id)
    && params.getAll("workspace_id").length === 1 && params.get("workspace_id") === workspaceId
  useEffect(() => {
    if (valid) router.replace(`/pages/${encodeURIComponent("@" + id)}?${new URLSearchParams({ workspace_id: workspaceId })}`)
  }, [valid, id, router, workspaceId])
  return <p role="status" className="p-6 text-muted-foreground">{valid ? "Opening document…" : "Document unavailable."}</p>
}
