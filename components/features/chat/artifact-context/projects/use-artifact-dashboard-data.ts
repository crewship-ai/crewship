"use client"
import { useEffect, useMemo, useState } from "react"
import { useQuery, useQueryClient } from "@tanstack/react-query"
import { useSessionSafe } from "@/hooks/use-auth"
import { apiFetch, AUTH_EVENT } from "@/lib/api-fetch"
import { artifactDashboards } from "@/lib/feature-flags"
import { parseDashboardCollection, parseDashboardManifest } from "@/lib/pages/artifact-dashboard"
import type { ArtifactFile } from "./types"

interface Scope { workspaceId: string; pageId: string; codeVersion: number; files: ArtifactFile[] }
export const DASHBOARD_PREVIEW_LIMIT_ERROR = "Dashboard exceeds the preview data limit"
export function useArtifactDashboardData({ workspaceId, pageId, codeVersion, files }: Scope) {
  const user = useSessionSafe()?.data?.user?.id ?? "", client = useQueryClient()
  const manifest = useMemo(() => parseDashboardManifest(files), [files])
  const scope = JSON.stringify([user, workspaceId, pageId, codeVersion, manifest?.collections])
  const [retired, setRetired] = useState<{ scope: string; limit: boolean }>()
  const enabled = artifactDashboards() && Boolean(user && workspaceId && pageId && manifest) && Number.isSafeInteger(codeVersion) && codeVersion > 0 && retired?.scope !== scope
  const query = useQuery({
    queryKey: ["artifact-dashboard-data", scope], enabled, retry: false, gcTime: 0, staleTime: 0,
    refetchInterval: 5000, refetchIntervalInBackground: false,
    queryFn: async ({ signal }) => {
     const collections = []
     let remaining = 16 * 1024 * 1024
     // Sequential and byte-bounded: do not allocate 32 full collection responses before checking the total.
     for (const collection of manifest!.collections) {
      const params = new URLSearchParams({ workspace_id: workspaceId, version: String(codeVersion) })
      const response = await apiFetch(`/api/v1/pages/${encodeURIComponent("@" + pageId)}/artifact/data/${encodeURIComponent(collection)}?${params}`, { method: "GET", cache: "no-store", signal })
      if (signal.aborted) throw new DOMException("Aborted", "AbortError")
      if (!response.ok) throw Object.assign(new Error("Dashboard data unavailable"), { status: response.status })
      const reader = response.body?.getReader()
      if (!reader) throw new Error("Invalid Dashboard data")
      const decoder = new TextDecoder("utf-8", { fatal: true })
      let source = ""
      try {
       while (true) {
        const { done, value } = await reader.read()
        if (signal.aborted) throw new DOMException("Aborted", "AbortError")
        if (done) { source += decoder.decode(); break }
        remaining -= value.byteLength
        if (remaining < 0) throw new Error(DASHBOARD_PREVIEW_LIMIT_ERROR)
        source += decoder.decode(value, { stream: true })
       }
      } finally { await reader.cancel().catch(() => {}); reader.releaseLock() }
      const result = parseDashboardCollection(JSON.parse(source), collection, codeVersion)
      if (!result) throw new Error("Invalid Dashboard data")
      collections.push(result)
     }
     return collections
    },
  })
  const status = (query.error as { status?: number } | null)?.status
  const limitExceeded = query.error instanceof Error && query.error.message === DASHBOARD_PREVIEW_LIMIT_ERROR
  useEffect(() => {
    if (limitExceeded || status && [401, 403, 404, 409].includes(status)) {
      setRetired({ scope, limit: limitExceeded })
      void client.cancelQueries({ queryKey: ["artifact-dashboard-data", scope], exact: true })
      client.removeQueries({ queryKey: ["artifact-dashboard-data", scope], exact: true })
    }
  }, [client, scope, status, limitExceeded])
  useEffect(() => {
    const retire = () => {
      setRetired({ scope, limit: false })
      void client.cancelQueries({ queryKey: ["artifact-dashboard-data", scope], exact: true })
      client.removeQueries({ queryKey: ["artifact-dashboard-data", scope], exact: true })
    }
    window.addEventListener(AUTH_EVENT, retire)
    return () => window.removeEventListener(AUTH_EVENT, retire)
  }, [client, scope])
  // React Query deliberately keeps old data on a refetch error. That copy must never reach the frame.
  return { scope, manifest, enabled, collections: enabled && !query.error ? query.data : undefined, pending: enabled && query.isPending, error: query.error || retired?.scope === scope, limitExceeded: limitExceeded || retired?.scope === scope && retired.limit }
}
