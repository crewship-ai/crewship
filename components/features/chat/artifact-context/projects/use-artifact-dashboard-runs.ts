"use client"
import { useQuery } from "@tanstack/react-query"
import { useSessionSafe } from "@/hooks/use-auth"
import { apiFetch } from "@/lib/api-fetch"
import { artifactDashboards } from "@/lib/feature-flags"
import { parseDashboardRuns, type DashboardRunSummary } from "@/lib/pages/artifact-dashboard-runs"

const MAX_RUNS_BYTES = 256 * 1024

/**
 * The producer run records next to the data read. A failed or invalid read
 * returns null: the chip then falls back to the last write and never shows
 * a run state it did not receive. It never affects the data itself.
 */
export function useArtifactDashboardRuns({ workspaceId, pageId, codeVersion, collections, enabled }: { workspaceId: string; pageId: string; codeVersion: number; collections: readonly string[] | undefined; enabled: boolean }): DashboardRunSummary[] | null {
  const user = useSessionSafe()?.data?.user?.id ?? ""
  const on = enabled && artifactDashboards() && Boolean(user && workspaceId && pageId && collections?.length) && Number.isSafeInteger(codeVersion) && codeVersion > 0
  const query = useQuery({
    queryKey: ["artifact-dashboard-runs", user, workspaceId, pageId, codeVersion, collections], enabled: on, retry: false, gcTime: 0, staleTime: 0,
    refetchInterval: 5000, refetchIntervalInBackground: false,
    queryFn: async ({ signal }) => {
      const params = new URLSearchParams({ workspace_id: workspaceId, version: String(codeVersion) })
      const response = await apiFetch(`/api/v1/pages/${encodeURIComponent("@" + pageId)}/artifact/data-runs?${params}`, { method: "GET", cache: "no-store", signal })
      if (!response.ok) throw Object.assign(new Error("Dashboard runs unavailable"), { status: response.status })
      const text = await response.text()
      if (text.length > MAX_RUNS_BYTES) throw new Error("Dashboard runs too large")
      const runs = parseDashboardRuns(JSON.parse(text), codeVersion, collections ?? [])
      if (!runs) throw new Error("Invalid Dashboard runs")
      return runs
    },
  })
  return on && !query.error ? query.data ?? null : null
}
