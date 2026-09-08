"use client"

import { useEffect, useState } from "react"
import { apiFetch } from "@/lib/api-fetch"

export function useIssuePeople(workspaceId: string, enabled = true) {
  const [people, setPeople] = useState<{ id: string; name: string }[]>([])
  const [error, setError] = useState(false)
  useEffect(() => {
    setPeople([])
    setError(false)
    if (!workspaceId || !enabled) return
    const controller = new AbortController()
    void apiFetch(`/api/v1/workspaces/${encodeURIComponent(workspaceId)}/members?workspace_id=${encodeURIComponent(workspaceId)}`, { signal: controller.signal })
      .then(async (res) => {
        if (!res.ok) throw new Error("Members unavailable")
        const rows = await res.json() as { user_id: string; user?: { full_name?: string; email?: string } }[]
        if (!controller.signal.aborted) setPeople(rows.map((row) => ({ id: row.user_id, name: row.user?.full_name || row.user?.email || "Workspace member" })))
      })
      .catch(() => { if (!controller.signal.aborted) setError(true) })
    return () => controller.abort()
  }, [workspaceId, enabled])
  return { people, error }
}
