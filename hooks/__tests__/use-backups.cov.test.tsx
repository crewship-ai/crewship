import { describe, it, expect, vi, beforeEach, afterEach } from "vitest"
import React from "react"
import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import { renderHook, waitFor, act } from "@testing-library/react"

import {
  useVerifyBackup,
  useRotateBackups,
  useBackupSelfTest,
  useForceUnlock,
  useBackupMetrics,
  useCrewsForBackup,
  buildDownloadUrl,
} from "@/hooks/use-backups"

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

function errJSON(status: number, body: unknown): Response {
  return {
    ok: false,
    status,
    text: async () => JSON.stringify(body),
    json: async () => body,
  } as unknown as Response
}

describe("use-backups (previously-unsurfaced endpoints)", () => {
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

  describe("useVerifyBackup", () => {
    it("GETs /verify with the encoded path and returns the verification body", async () => {
      mockFetch.mockResolvedValueOnce(
        okJSON({ valid: true, size_bytes: 7, manifest: { format_version: 1 }, error: "" }),
      )

      const { result } = renderHook(() => useVerifyBackup("ws-1"), {
        wrapper: makeWrapper(qc),
      })
      let response: unknown
      await act(async () => {
        response = await result.current.mutateAsync("/b/a b.tar.zst")
      })

      expect(mockFetch).toHaveBeenCalledWith(
        "/api/v1/admin/backups/verify?workspace_id=ws-1&path=%2Fb%2Fa+b.tar.zst",
        expect.objectContaining({ credentials: "include" }),
      )
      expect(response).toMatchObject({ valid: true, size_bytes: 7 })
    })

    it("rejects without a workspaceId before any fetch", async () => {
      const { result } = renderHook(() => useVerifyBackup(undefined), {
        wrapper: makeWrapper(qc),
      })
      await expect(result.current.mutateAsync("/p")).rejects.toThrow(/workspaceId is required/)
      expect(mockFetch).not.toHaveBeenCalled()
    })
  })

  describe("buildDownloadUrl", () => {
    it("builds the encoded streaming-download URL", () => {
      expect(buildDownloadUrl("ws 1", "/b/x&y.tar.zst")).toBe(
        "/api/v1/admin/backups/download?workspace_id=ws+1&path=%2Fb%2Fx%26y.tar.zst",
      )
    })
  })

  describe("useRotateBackups", () => {
    it("POSTs the retention request and invalidates the list when files were deleted", async () => {
      mockFetch.mockResolvedValueOnce(okJSON({ deleted: ["/b/old.tar.zst"], dry_run: false }))
      const invalidateSpy = vi.spyOn(qc, "invalidateQueries")

      const { result } = renderHook(() => useRotateBackups("ws-1"), {
        wrapper: makeWrapper(qc),
      })
      await act(async () => {
        await result.current.mutateAsync({ keep_last: 3, dry_run: false })
      })

      const [url, init] = mockFetch.mock.calls[0] as [string, RequestInit]
      expect(url).toBe("/api/v1/admin/backups/rotate?workspace_id=ws-1")
      expect(init.method).toBe("POST")
      expect(JSON.parse(init.body as string)).toEqual({ keep_last: 3, dry_run: false })

      const invalidated = invalidateSpy.mock.calls.map(
        (c) => (c[0] as { queryKey: unknown[] }).queryKey[0],
      )
      expect(invalidated).toContain("backups")
    })

    it("does NOT invalidate on a dry run", async () => {
      mockFetch.mockResolvedValueOnce(okJSON({ deleted: ["/b/old.tar.zst"], dry_run: true }))
      const invalidateSpy = vi.spyOn(qc, "invalidateQueries")

      const { result } = renderHook(() => useRotateBackups("ws-1"), {
        wrapper: makeWrapper(qc),
      })
      await act(async () => {
        await result.current.mutateAsync({ keep_last: 3, dry_run: true })
      })
      expect(invalidateSpy).not.toHaveBeenCalled()
    })

    it("treats a null deleted list (backend shape) as nothing deleted — no invalidate", async () => {
      mockFetch.mockResolvedValueOnce(okJSON({ deleted: null, dry_run: false }))
      const invalidateSpy = vi.spyOn(qc, "invalidateQueries")

      const { result } = renderHook(() => useRotateBackups("ws-1"), {
        wrapper: makeWrapper(qc),
      })
      await act(async () => {
        await result.current.mutateAsync({ keep_days: 30 })
      })
      expect(invalidateSpy).not.toHaveBeenCalled()
    })
  })

  describe("useBackupSelfTest", () => {
    it("POSTs the crew_id body to /self-test and returns the round-trip result", async () => {
      mockFetch.mockResolvedValueOnce(
        okJSON({ ok: true, crew_id: "c1", crew_slug: "alpha", canary_path: "/workspace/CANARY.txt", canary_bytes: 12, bundle_bytes: 100, elapsed_ms: 50 }),
      )

      const { result } = renderHook(() => useBackupSelfTest("ws-1"), {
        wrapper: makeWrapper(qc),
      })
      let response: unknown
      await act(async () => {
        response = await result.current.mutateAsync({ crew_id: "c1" })
      })

      const [url, init] = mockFetch.mock.calls[0] as [string, RequestInit]
      expect(url).toBe("/api/v1/admin/backups/self-test?workspace_id=ws-1")
      expect(init.method).toBe("POST")
      expect(JSON.parse(init.body as string)).toEqual({ crew_id: "c1" })
      expect(response).toMatchObject({ ok: true, crew_slug: "alpha" })
    })

    it("surfaces server errors through asError", async () => {
      mockFetch.mockResolvedValueOnce(errJSON(400, { error: "crew_id is required" }))
      const { result } = renderHook(() => useBackupSelfTest("ws-1"), {
        wrapper: makeWrapper(qc),
      })
      await expect(result.current.mutateAsync({ crew_id: "" })).rejects.toThrow(
        "crew_id is required",
      )
    })
  })

  describe("useForceUnlock", () => {
    it("DELETEs the status endpoint and invalidates backup-status on success", async () => {
      mockFetch.mockResolvedValueOnce(okJSON(null))
      const invalidateSpy = vi.spyOn(qc, "invalidateQueries")

      const { result } = renderHook(() => useForceUnlock("ws-1"), {
        wrapper: makeWrapper(qc),
      })
      await act(async () => {
        await result.current.mutateAsync()
      })

      const [url, init] = mockFetch.mock.calls[0] as [string, RequestInit]
      expect(url).toBe("/api/v1/admin/backups/status?workspace_id=ws-1")
      expect(init.method).toBe("DELETE")

      const invalidated = invalidateSpy.mock.calls.map(
        (c) => (c[0] as { queryKey: unknown[] }).queryKey[0],
      )
      expect(invalidated).toContain("backup-status")
    })

    it("propagates the server error when the unlock is rejected", async () => {
      mockFetch.mockResolvedValueOnce(errJSON(409, { error: "backup in progress" }))
      const { result } = renderHook(() => useForceUnlock("ws-1"), {
        wrapper: makeWrapper(qc),
      })
      await expect(result.current.mutateAsync()).rejects.toThrow("backup in progress")
    })

    it("rejects without a workspaceId before any fetch", async () => {
      const { result } = renderHook(() => useForceUnlock(undefined), {
        wrapper: makeWrapper(qc),
      })
      await expect(result.current.mutateAsync()).rejects.toThrow(/workspaceId is required/)
      expect(mockFetch).not.toHaveBeenCalled()
    })
  })

  describe("useBackupMetrics", () => {
    it("is disabled without a workspaceId", async () => {
      const { result } = renderHook(() => useBackupMetrics(undefined), {
        wrapper: makeWrapper(qc),
      })
      await act(async () => {
        await Promise.resolve()
      })
      expect(mockFetch).not.toHaveBeenCalled()
      expect(result.current.data).toBeUndefined()
    })

    it("fetches the metrics endpoint with workspace_id", async () => {
      mockFetch.mockResolvedValueOnce(
        okJSON({ created_total: 4, failed_total: 1, restored_total: 2 }),
      )
      const { result } = renderHook(() => useBackupMetrics("ws-1"), {
        wrapper: makeWrapper(qc),
      })
      await waitFor(() => expect(result.current.isSuccess).toBe(true))
      expect(mockFetch).toHaveBeenCalledWith(
        "/api/v1/admin/backups/metrics?workspace_id=ws-1",
        expect.objectContaining({ credentials: "include" }),
      )
      expect(result.current.data?.created_total).toBe(4)
    })

    it("surfaces the instance-owner 403 as a normal error (no retry storm)", async () => {
      mockFetch.mockResolvedValue(errJSON(403, { error: "instance owner required" }))
      const { result } = renderHook(() => useBackupMetrics("ws-1"), {
        wrapper: makeWrapper(qc),
      })
      await waitFor(() => expect(result.current.isError).toBe(true))
      expect(result.current.error?.message).toBe("instance owner required")
      // retry: false — exactly one request despite the failure.
      expect(mockFetch).toHaveBeenCalledTimes(1)
    })
  })

  describe("useCrewsForBackup", () => {
    it("is disabled without a workspaceId", async () => {
      const { result } = renderHook(() => useCrewsForBackup(undefined), {
        wrapper: makeWrapper(qc),
      })
      await act(async () => {
        await Promise.resolve()
      })
      expect(mockFetch).not.toHaveBeenCalled()
      expect(result.current.data).toBeUndefined()
    })

    it("accepts the legacy bare-array crews response", async () => {
      mockFetch.mockResolvedValueOnce(okJSON([{ id: "c1", slug: "alpha", name: "Alpha" }]))
      const { result } = renderHook(() => useCrewsForBackup("ws&1"), {
        wrapper: makeWrapper(qc),
      })
      await waitFor(() => expect(result.current.isSuccess).toBe(true))
      expect(mockFetch).toHaveBeenCalledWith("/api/v1/crews?workspace_id=ws%261", expect.objectContaining({ credentials: "include" }))
      expect(result.current.data).toEqual([{ id: "c1", slug: "alpha", name: "Alpha" }])
    })

    it("accepts the paginated { data } shape", async () => {
      mockFetch.mockResolvedValueOnce(okJSON({ data: [{ id: "c2", slug: "beta", name: "Beta" }] }))
      const { result } = renderHook(() => useCrewsForBackup("ws-1"), {
        wrapper: makeWrapper(qc),
      })
      await waitFor(() => expect(result.current.isSuccess).toBe(true))
      expect(result.current.data).toEqual([{ id: "c2", slug: "beta", name: "Beta" }])
    })

    it("falls back to an empty list when data is missing", async () => {
      mockFetch.mockResolvedValueOnce(okJSON({}))
      const { result } = renderHook(() => useCrewsForBackup("ws-1"), {
        wrapper: makeWrapper(qc),
      })
      await waitFor(() => expect(result.current.isSuccess).toBe(true))
      expect(result.current.data).toEqual([])
    })
  })
})
