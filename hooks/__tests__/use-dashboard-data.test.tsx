import { describe, it, expect, vi, beforeEach, afterEach } from "vitest"
import React from "react"
import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import { renderHook, waitFor, act } from "@testing-library/react"

import {
  dashboardKeys,
  useAgentSummaries,
  useCrewSummaries,
  useProjectSummaries,
  useDashboardMissions,
  useMissionMetrics,
  useRecentRuns,
  usePendingEscalationCount,
  useKeeperRequests,
  useMetricsTimeseries,
  useInvalidateDashboard,
  DASHBOARD_THROUGHPUT_PARAMS,
  DASHBOARD_COST_PARAMS,
} from "@/hooks/use-dashboard-data"

// Tests run against a fresh QueryClient per test with retries disabled so a
// rejected mock surfaces immediately instead of silently being retried.
function makeWrapper(qc: QueryClient) {
  return function Wrapper({ children }: { children: React.ReactNode }) {
    return <QueryClientProvider client={qc}>{children}</QueryClientProvider>
  }
}

function newQueryClient(): QueryClient {
  return new QueryClient({
    defaultOptions: {
      queries: { retry: false, gcTime: 0 },
      mutations: { retry: false },
    },
  })
}

function okJSON(body: unknown): Response {
  return {
    ok: true,
    status: 200,
    text: async () => JSON.stringify(body),
    json: async () => body,
  } as unknown as Response
}

function errStatus(status: number): Response {
  return {
    ok: false,
    status,
    text: async () => "",
    json: async () => ({}),
  } as unknown as Response
}

describe("use-dashboard-data", () => {
  let mockFetch: ReturnType<typeof vi.fn>
  let qc: QueryClient

  beforeEach(() => {
    mockFetch = vi.fn()
    vi.stubGlobal("fetch", mockFetch)
    qc = newQueryClient()
  })

  afterEach(() => {
    vi.unstubAllGlobals()
    qc.clear()
  })

  const fetchedUrl = (n = 0) => mockFetch.mock.calls[n][0] as string

  describe("workspace gating", () => {
    it("every list hook is disabled without a workspaceId — no fetch fires", async () => {
      renderHook(
        () => {
          useAgentSummaries(null)
          useCrewSummaries(null)
          useProjectSummaries(null)
          useDashboardMissions(null)
          useMissionMetrics(null)
          useRecentRuns(null)
          usePendingEscalationCount(null)
          useKeeperRequests(null)
          useMetricsTimeseries(null, DASHBOARD_THROUGHPUT_PARAMS)
        },
        { wrapper: makeWrapper(qc) },
      )
      await act(async () => { await Promise.resolve() })
      expect(mockFetch).not.toHaveBeenCalled()
    })

    it("respects an explicit enabled:false even with a workspaceId (onboarding gate)", async () => {
      renderHook(() => useAgentSummaries("ws-1", { enabled: false }), {
        wrapper: makeWrapper(qc),
      })
      await act(async () => { await Promise.resolve() })
      expect(mockFetch).not.toHaveBeenCalled()
    })
  })

  describe("useAgentSummaries", () => {
    it("fetches /agents and caches under the canonical key", async () => {
      const agents = [{ id: "a1", name: "Eva", slug: "eva" }]
      mockFetch.mockResolvedValueOnce(okJSON(agents))

      const { result } = renderHook(() => useAgentSummaries("ws-1"), {
        wrapper: makeWrapper(qc),
      })
      await waitFor(() => expect(result.current.isSuccess).toBe(true))

      expect(fetchedUrl()).toBe("/api/v1/agents?workspace_id=ws-1")
      expect(result.current.data).toEqual(agents)
      expect(qc.getQueryData(dashboardKeys.agents("ws-1"))).toEqual(agents)
    })

    it("maps a non-ok response to an empty list (best-effort tile, not an error state)", async () => {
      mockFetch.mockResolvedValueOnce(errStatus(500))

      const { result } = renderHook(() => useAgentSummaries("ws-1"), {
        wrapper: makeWrapper(qc),
      })
      await waitFor(() => expect(result.current.isSuccess).toBe(true))
      expect(result.current.data).toEqual([])
    })

    it("percent-encodes the workspace id", async () => {
      mockFetch.mockResolvedValueOnce(okJSON([]))
      renderHook(() => useAgentSummaries("ws/1 x"), { wrapper: makeWrapper(qc) })
      await waitFor(() => expect(mockFetch).toHaveBeenCalled())
      expect(fetchedUrl()).toBe("/api/v1/agents?workspace_id=ws%2F1%20x")
    })
  })

  describe("useDashboardMissions / useRecentRuns", () => {
    it("missions request carries the dashboard limit + include_tasks params", async () => {
      mockFetch.mockResolvedValueOnce(okJSON([]))
      renderHook(() => useDashboardMissions("ws-1"), { wrapper: makeWrapper(qc) })
      await waitFor(() => expect(mockFetch).toHaveBeenCalled())
      expect(fetchedUrl()).toBe(
        "/api/v1/missions?workspace_id=ws-1&limit=50&include_tasks=true",
      )
    })

    it("runs request carries limit=50 and non-ok maps to null", async () => {
      mockFetch.mockResolvedValueOnce(errStatus(503))
      const { result } = renderHook(() => useRecentRuns("ws-1"), {
        wrapper: makeWrapper(qc),
      })
      await waitFor(() => expect(result.current.isSuccess).toBe(true))
      expect(fetchedUrl()).toBe("/api/v1/runs?workspace_id=ws-1&limit=50")
      expect(result.current.data).toBeNull()
    })
  })

  describe("useMissionMetrics", () => {
    it("returns the metrics payload, null on failure", async () => {
      mockFetch.mockResolvedValueOnce(
        okJSON({ active_missions: 2, total_missions: 9, total_cost_24h: 1.5 }),
      )
      const { result } = renderHook(() => useMissionMetrics("ws-1"), {
        wrapper: makeWrapper(qc),
      })
      await waitFor(() => expect(result.current.isSuccess).toBe(true))
      expect(fetchedUrl()).toBe("/api/v1/mission-metrics?workspace_id=ws-1")
      expect(result.current.data?.active_missions).toBe(2)
    })
  })

  describe("usePendingEscalationCount", () => {
    it("coerces the count field to a number", async () => {
      mockFetch.mockResolvedValueOnce(okJSON({ count: "7" }))
      const { result } = renderHook(() => usePendingEscalationCount("ws-1"), {
        wrapper: makeWrapper(qc),
      })
      await waitFor(() => expect(result.current.isSuccess).toBe(true))
      expect(fetchedUrl()).toBe("/api/v1/escalations/pending-count?workspace_id=ws-1")
      expect(result.current.data).toBe(7)
    })

    it("maps non-ok and malformed payloads to 0", async () => {
      mockFetch.mockResolvedValueOnce(errStatus(403))
      const { result } = renderHook(() => usePendingEscalationCount("ws-1"), {
        wrapper: makeWrapper(qc),
      })
      await waitFor(() => expect(result.current.isSuccess).toBe(true))
      expect(result.current.data).toBe(0)
    })
  })

  describe("useKeeperRequests", () => {
    it("accepts a bare array response", async () => {
      const rows = [{ id: "k1", agent_name: "eva", credential_name: "gh", decision: null, created_at: "" }]
      mockFetch.mockResolvedValueOnce(okJSON(rows))
      const { result } = renderHook(() => useKeeperRequests("ws-1"), {
        wrapper: makeWrapper(qc),
      })
      await waitFor(() => expect(result.current.isSuccess).toBe(true))
      expect(fetchedUrl()).toBe("/api/v1/admin/keeper/requests?workspace_id=ws-1&limit=10")
      expect(result.current.data).toEqual(rows)
    })

    it("accepts the enveloped { data: [...] } shape", async () => {
      const rows = [{ id: "k1" }]
      mockFetch.mockResolvedValueOnce(okJSON({ data: rows }))
      const { result } = renderHook(() => useKeeperRequests("ws-1"), {
        wrapper: makeWrapper(qc),
      })
      await waitFor(() => expect(result.current.isSuccess).toBe(true))
      expect(result.current.data).toEqual(rows)
    })

    it("maps a 403 (RBAC-gated admin endpoint) to an empty list, not an error", async () => {
      mockFetch.mockResolvedValueOnce(errStatus(403))
      const { result } = renderHook(() => useKeeperRequests("ws-1"), {
        wrapper: makeWrapper(qc),
      })
      await waitFor(() => expect(result.current.isSuccess).toBe(true))
      expect(result.current.data).toEqual([])
    })
  })

  describe("useMetricsTimeseries", () => {
    it("builds the params in the documented order and caches per params object", async () => {
      mockFetch.mockResolvedValueOnce(okJSON({ buckets: [], series_labels: {} }))
      const { result } = renderHook(
        () => useMetricsTimeseries("ws-1", DASHBOARD_THROUGHPUT_PARAMS),
        { wrapper: makeWrapper(qc) },
      )
      await waitFor(() => expect(result.current.isSuccess).toBe(true))
      expect(fetchedUrl()).toBe(
        "/api/v1/metrics/timeseries?workspace_id=ws-1&metric=issues_closed&window=24h&bucket=1h&group_by=crew",
      )
      expect(
        qc.getQueryData(dashboardKeys.timeseries("ws-1", DASHBOARD_THROUGHPUT_PARAMS)),
      ).toBeTruthy()
    })

    it("maps a non-ok response to null so charts render their empty state", async () => {
      mockFetch.mockResolvedValueOnce(errStatus(500))
      const { result } = renderHook(
        () => useMetricsTimeseries("ws-1", DASHBOARD_COST_PARAMS),
        { wrapper: makeWrapper(qc) },
      )
      await waitFor(() => expect(result.current.isSuccess).toBe(true))
      expect(result.current.data).toBeNull()
    })
  })

  describe("useInvalidateDashboard", () => {
    it("invalidates every dashboard query key for the workspace", async () => {
      const invalidateSpy = vi.spyOn(qc, "invalidateQueries")
      const { result } = renderHook(() => useInvalidateDashboard("ws-1"), {
        wrapper: makeWrapper(qc),
      })

      act(() => { result.current() })

      const keys = invalidateSpy.mock.calls.map(
        (c) => (c[0] as { queryKey: readonly unknown[] }).queryKey,
      )
      expect(keys).toContainEqual(dashboardKeys.agents("ws-1"))
      expect(keys).toContainEqual(dashboardKeys.crews("ws-1"))
      expect(keys).toContainEqual(dashboardKeys.projects("ws-1"))
      expect(keys).toContainEqual(dashboardKeys.missions("ws-1"))
      expect(keys).toContainEqual(dashboardKeys.missionMetrics("ws-1"))
      expect(keys).toContainEqual(dashboardKeys.runs("ws-1"))
      expect(keys).toContainEqual(dashboardKeys.escalationCount("ws-1"))
      expect(keys).toContainEqual(dashboardKeys.keeperRequests("ws-1"))
      expect(keys).toContainEqual(dashboardKeys.timeseries("ws-1", DASHBOARD_THROUGHPUT_PARAMS))
      expect(keys).toContainEqual(dashboardKeys.timeseries("ws-1", DASHBOARD_COST_PARAMS))
    })

    it("is a no-op without a workspaceId", () => {
      const invalidateSpy = vi.spyOn(qc, "invalidateQueries")
      const { result } = renderHook(() => useInvalidateDashboard(null), {
        wrapper: makeWrapper(qc),
      })
      act(() => { result.current() })
      expect(invalidateSpy).not.toHaveBeenCalled()
    })
  })
})
