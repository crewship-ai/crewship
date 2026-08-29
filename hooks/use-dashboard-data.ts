"use client"

import { useCallback } from "react"
import { useQueries, useQuery, useQueryClient } from "@tanstack/react-query"

import { apiFetch } from "@/lib/api-fetch"
import type { Mission } from "@/lib/types/mission"
import type {
  AgentSummary, CrewSummary, ProjectSummary, RunsResponse,
  MissionMetricsResponse, KeeperRequest, TimeseriesResponse,
  CrewServiceSummary, CrewSpendResponse, DashboardWindow,
  MemoryHealthResponse, RunInsightsResponse, RuntimeCapacityResponse,
} from "@/app/(dashboard)/dashboard-types"

/**
 * React Query surface for the dashboard page (W7 — RELEASE-1.0-HARDENING).
 * This file is the canonical pattern for migrating hand-rolled
 * fetch+useState hooks to React Query:
 *
 *   - queryKey convention: [resource, workspaceId, params?]. The
 *     workspace id sits at position 1 so a workspace switch lands on a
 *     fresh cache entry (no stale cross-workspace data) and
 *     `invalidateQueries({ queryKey: [resource, wsId] })` scopes to one
 *     workspace. Non-default request params go last as a stable object
 *     so two callers with different limits never share an entry.
 *   - all requests go through `apiFetch` (lib/api-fetch.ts) so 401s hit
 *     the shared refresh-once-then-retry path instead of silently
 *     emptying a tile after token expiry.
 *   - freshness comes from WS events (see useInvalidateDashboard +
 *     the useRealtimeEvent subscriptions in app/(dashboard)/page.tsx),
 *     not from polling.
 *
 * Error policy: the dashboard is a best-effort aggregate — a single
 * failing endpoint (e.g. the RBAC-gated keeper admin route returning
 * 403 for a non-admin) must degrade that one tile to its empty state,
 * never error the whole page. So these queryFns map non-ok responses
 * to the slice's empty value instead of throwing. Surfaces that render
 * a real error state (use-backups, use-inbox) throw instead — pick per
 * surface.
 */

/** Dashboard-specific request params. Module-level consts so the
 *  objects are referentially stable inside query keys. */
export const DASHBOARD_MISSIONS_PARAMS = { limit: 50, include_tasks: true } as const
export const DASHBOARD_RUNS_PARAMS = { limit: 50 } as const
export const DASHBOARD_KEEPER_PARAMS = { limit: 10 } as const

export interface TimeseriesParams {
  metric: string
  window: string
  bucket: string
  group_by: string
}

export const DASHBOARD_THROUGHPUT_PARAMS: TimeseriesParams = {
  metric: "issues_closed", window: "24h", bucket: "1h", group_by: "crew",
}
export const DASHBOARD_COST_PARAMS: TimeseriesParams = {
  metric: "cost_usd", window: "7d", bucket: "1d", group_by: "none",
}

export const dashboardKeys = {
  agents: (ws: string) => ["agents", ws] as const,
  crews: (ws: string) => ["crews", ws] as const,
  projects: (ws: string) => ["projects", ws] as const,
  missions: (ws: string) => ["missions", ws, DASHBOARD_MISSIONS_PARAMS] as const,
  missionMetrics: (ws: string) => ["mission-metrics", ws] as const,
  runs: (ws: string) => ["runs", ws, DASHBOARD_RUNS_PARAMS] as const,
  escalationCount: (ws: string) => ["escalations-pending-count", ws] as const,
  keeperRequests: (ws: string) => ["keeper-requests", ws, DASHBOARD_KEEPER_PARAMS] as const,
  timeseries: (ws: string, params: TimeseriesParams) =>
    ["metrics-timeseries", ws, params] as const,
  runInsights: (ws: string, window: DashboardWindow) =>
    ["runs-insights", ws, { window }] as const,
  runtimeCapacity: () => ["runtime-capacity"] as const,
  memoryHealth: (ws: string) => ["memory-health", ws] as const,
  crewSpend: (ws: string, window: DashboardWindow) =>
    ["crew-spend", ws, { window }] as const,
  crewServices: (ws: string, crewId: string) =>
    ["crew-services", ws, crewId] as const,
}

interface DashboardQueryOpts {
  /** Extra gate on top of the workspaceId guard — the page uses it to
   *  hold queries until the onboarding redirect check has resolved. */
  enabled?: boolean
}

/** fetchOr GETs a URL and maps non-ok / unparseable responses to the
 *  given fallback. See the error-policy note in the file header. */
async function fetchOr<T>(url: string, fallback: T, signal?: AbortSignal): Promise<T> {
  const res = await apiFetch(url, { signal })
  if (!res.ok) return fallback
  try {
    return (await res.json()) as T
  } catch {
    return fallback
  }
}

// retry: false on every query mirrors the previous single-shot fetch:
// a network-level failure renders the empty state immediately instead
// of holding the skeleton through a retry backoff; the WS-driven
// invalidation (or the next workspace switch) re-triggers anyway.

export function useAgentSummaries(workspaceId: string | null, opts?: DashboardQueryOpts) {
  return useQuery<AgentSummary[]>({
    queryKey: dashboardKeys.agents(workspaceId ?? ""),
    queryFn: ({ signal }) =>
      fetchOr(`/api/v1/agents?workspace_id=${encodeURIComponent(workspaceId!)}`, [], signal),
    enabled: Boolean(workspaceId) && (opts?.enabled ?? true),
    retry: false,
  })
}

export function useCrewSummaries(workspaceId: string | null, opts?: DashboardQueryOpts) {
  return useQuery<CrewSummary[]>({
    queryKey: dashboardKeys.crews(workspaceId ?? ""),
    queryFn: ({ signal }) =>
      fetchOr(`/api/v1/crews?workspace_id=${encodeURIComponent(workspaceId!)}`, [], signal),
    enabled: Boolean(workspaceId) && (opts?.enabled ?? true),
    retry: false,
  })
}

export function useProjectSummaries(workspaceId: string | null, opts?: DashboardQueryOpts) {
  return useQuery<ProjectSummary[]>({
    queryKey: dashboardKeys.projects(workspaceId ?? ""),
    queryFn: ({ signal }) =>
      fetchOr(`/api/v1/projects?workspace_id=${encodeURIComponent(workspaceId!)}`, [], signal),
    enabled: Boolean(workspaceId) && (opts?.enabled ?? true),
    retry: false,
  })
}

export function useDashboardMissions(workspaceId: string | null, opts?: DashboardQueryOpts) {
  return useQuery<Mission[]>({
    queryKey: dashboardKeys.missions(workspaceId ?? ""),
    queryFn: ({ signal }) =>
      fetchOr(
        `/api/v1/missions?workspace_id=${encodeURIComponent(workspaceId!)}&limit=${DASHBOARD_MISSIONS_PARAMS.limit}&include_tasks=${DASHBOARD_MISSIONS_PARAMS.include_tasks}`,
        [],
        signal,
      ),
    enabled: Boolean(workspaceId) && (opts?.enabled ?? true),
    retry: false,
  })
}

export function useMissionMetrics(workspaceId: string | null, opts?: DashboardQueryOpts) {
  return useQuery<MissionMetricsResponse | null>({
    queryKey: dashboardKeys.missionMetrics(workspaceId ?? ""),
    queryFn: ({ signal }) =>
      fetchOr<MissionMetricsResponse | null>(
        `/api/v1/mission-metrics?workspace_id=${encodeURIComponent(workspaceId!)}`,
        null,
        signal,
      ),
    enabled: Boolean(workspaceId) && (opts?.enabled ?? true),
    retry: false,
  })
}

export function useRecentRuns(workspaceId: string | null, opts?: DashboardQueryOpts) {
  return useQuery<RunsResponse | null>({
    queryKey: dashboardKeys.runs(workspaceId ?? ""),
    queryFn: ({ signal }) =>
      fetchOr<RunsResponse | null>(
        `/api/v1/runs?workspace_id=${encodeURIComponent(workspaceId!)}&limit=${DASHBOARD_RUNS_PARAMS.limit}`,
        null,
        signal,
      ),
    enabled: Boolean(workspaceId) && (opts?.enabled ?? true),
    retry: false,
  })
}

export function usePendingEscalationCount(workspaceId: string | null, opts?: DashboardQueryOpts) {
  return useQuery<number>({
    queryKey: dashboardKeys.escalationCount(workspaceId ?? ""),
    queryFn: async ({ signal }) => {
      const data = await fetchOr<{ count?: unknown } | null>(
        `/api/v1/escalations/pending-count?workspace_id=${encodeURIComponent(workspaceId!)}`,
        null,
        signal,
      )
      return Number(data?.count) || 0
    },
    enabled: Boolean(workspaceId) && (opts?.enabled ?? true),
    retry: false,
  })
}

export function useKeeperRequests(workspaceId: string | null, opts?: DashboardQueryOpts) {
  return useQuery<KeeperRequest[]>({
    queryKey: dashboardKeys.keeperRequests(workspaceId ?? ""),
    queryFn: async ({ signal }) => {
      // Admin endpoint — non-admins get a 403, which fetchOr maps to
      // []. The handler historically returned a bare array; the newer
      // paginated shape wraps it under { data }. Accept both.
      const data = await fetchOr<KeeperRequest[] | { data?: KeeperRequest[] } | null>(
        `/api/v1/admin/keeper/requests?workspace_id=${encodeURIComponent(workspaceId!)}&limit=${DASHBOARD_KEEPER_PARAMS.limit}`,
        null,
        signal,
      )
      if (Array.isArray(data)) return data
      return data?.data ?? []
    },
    enabled: Boolean(workspaceId) && (opts?.enabled ?? true),
    retry: false,
  })
}

export function useMetricsTimeseries(
  workspaceId: string | null,
  params: TimeseriesParams,
  opts?: DashboardQueryOpts,
) {
  return useQuery<TimeseriesResponse | null>({
    queryKey: dashboardKeys.timeseries(workspaceId ?? "", params),
    queryFn: ({ signal }) =>
      fetchOr<TimeseriesResponse | null>(
        `/api/v1/metrics/timeseries?workspace_id=${encodeURIComponent(workspaceId!)}&metric=${params.metric}&window=${params.window}&bucket=${params.bucket}&group_by=${params.group_by}`,
        null,
        signal,
      ),
    enabled: Boolean(workspaceId) && (opts?.enabled ?? true),
    retry: false,
  })
}

export function useRunsInsights(
  workspaceId: string | null,
  window: DashboardWindow,
  opts?: DashboardQueryOpts,
) {
  return useQuery<RunInsightsResponse | null>({
    queryKey: dashboardKeys.runInsights(workspaceId ?? "", window),
    queryFn: ({ signal }) =>
      fetchOr<RunInsightsResponse | null>(
        `/api/v1/runs/insights?workspace_id=${encodeURIComponent(workspaceId!)}&window=${window}`,
        null,
        signal,
      ),
    enabled: Boolean(workspaceId) && (opts?.enabled ?? true),
    retry: false,
  })
}

export function useRuntimeCapacity(opts?: DashboardQueryOpts) {
  return useQuery<RuntimeCapacityResponse | null>({
    queryKey: dashboardKeys.runtimeCapacity(),
    queryFn: ({ signal }) =>
      fetchOr<RuntimeCapacityResponse | null>("/api/v1/runtime/capacity", null, signal),
    enabled: opts?.enabled ?? true,
    retry: false,
    // Capacity is instance-scoped and can change without a workspace event.
    refetchInterval: 15_000,
  })
}

export function useMemoryHealth(workspaceId: string | null, opts?: DashboardQueryOpts) {
  return useQuery<MemoryHealthResponse | null>({
    queryKey: dashboardKeys.memoryHealth(workspaceId ?? ""),
    queryFn: ({ signal }) =>
      fetchOr<MemoryHealthResponse | null>(
        `/api/v1/memory/health?workspace_id=${encodeURIComponent(workspaceId!)}`,
        null,
        signal,
      ),
    enabled: Boolean(workspaceId) && (opts?.enabled ?? true),
    retry: false,
    staleTime: 60_000,
  })
}

export function useCrewSpend(
  workspaceId: string | null,
  window: DashboardWindow,
  opts?: DashboardQueryOpts,
) {
  return useQuery<CrewSpendResponse | null>({
    queryKey: dashboardKeys.crewSpend(workspaceId ?? "", window),
    queryFn: ({ signal }) =>
      fetchOr<CrewSpendResponse | null>(
        `/api/v1/paymaster/spend/by-crew?workspace_id=${encodeURIComponent(workspaceId!)}&range=${window}`,
        null,
        signal,
      ),
    enabled: Boolean(workspaceId) && (opts?.enabled ?? true),
    retry: false,
  })
}

interface CrewServiceWireResponse {
  services?: Array<{ status?: string }>
}

/**
 * Live service health is crew-scoped in the API. Fan out with React Query so
 * each crew remains independently cached and one Docker lookup failure cannot
 * turn the whole fleet red. `checked=false` is deliberately distinct from an
 * honest zero-service crew.
 */
export function useCrewServiceSummaries(
  workspaceId: string | null,
  crews: CrewSummary[],
  opts?: DashboardQueryOpts,
) {
  const results = useQueries({
    queries: crews.map((crew) => ({
      queryKey: dashboardKeys.crewServices(workspaceId ?? "", crew.id),
      queryFn: async ({ signal }: { signal: AbortSignal }) => {
        const res = await apiFetch(
          `/api/v1/crews/${encodeURIComponent(crew.id)}/services?workspace_id=${encodeURIComponent(workspaceId!)}`,
          { signal },
        )
        if (!res.ok) throw new Error(`crew services: ${res.status}`)
        return (await res.json()) as CrewServiceWireResponse
      },
      enabled: Boolean(workspaceId) && (opts?.enabled ?? true),
      retry: false,
      staleTime: 15_000,
    })),
  })

  const byCrew = new Map<string, CrewServiceSummary>()
  results.forEach((result, index) => {
    const crew = crews[index]
    if (!crew) return
    const services = result.data?.services
    if (!Array.isArray(services)) {
      byCrew.set(crew.id, { total: 0, running: 0, degraded: 0, checked: false })
      return
    }
    const running = services.filter((service) => {
      const status = (service.status ?? "").toLowerCase()
      return status === "running" || status === "healthy"
    }).length
    byCrew.set(crew.id, {
      total: services.length,
      running,
      degraded: Math.max(0, services.length - running),
      checked: true,
    })
  })

  return {
    byCrew,
    loading: results.some((result) => result.isPending),
    checked: results.filter((result) => result.isSuccess).length,
  }
}

/**
 * Returns a stable callback that invalidates every dashboard query for
 * the workspace. The page calls it (debounced) from its WS event
 * subscriptions — a background refetch via invalidation never flips
 * `isPending`, so the skeleton does not flash on live updates, exactly
 * like the old `fetchData(false)` path.
 */
export function useInvalidateDashboard(workspaceId: string | null) {
  const qc = useQueryClient()
  return useCallback(() => {
    if (!workspaceId) return
    const keys = [
      dashboardKeys.agents(workspaceId),
      dashboardKeys.crews(workspaceId),
      dashboardKeys.projects(workspaceId),
      dashboardKeys.missions(workspaceId),
      dashboardKeys.missionMetrics(workspaceId),
      dashboardKeys.runs(workspaceId),
      dashboardKeys.escalationCount(workspaceId),
      dashboardKeys.keeperRequests(workspaceId),
      dashboardKeys.timeseries(workspaceId, DASHBOARD_THROUGHPUT_PARAMS),
      dashboardKeys.timeseries(workspaceId, DASHBOARD_COST_PARAMS),
    ]
    for (const queryKey of keys) {
      qc.invalidateQueries({ queryKey })
    }
    // Windowed queries share these prefixes; invalidate every mounted window.
    qc.invalidateQueries({ queryKey: ["runs-insights", workspaceId] })
    qc.invalidateQueries({ queryKey: ["crew-spend", workspaceId] })
    qc.invalidateQueries({ queryKey: ["crew-services", workspaceId] })
  }, [qc, workspaceId])
}
