import { describe, it, expect, vi, beforeEach, afterEach } from "vitest"
import React from "react"
import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import { renderHook, waitFor, act } from "@testing-library/react"

import {
  useBackups,
  useBackupStatus,
  useInspectBackup,
  useCreateBackup,
  useRestoreBackup,
  useDeleteBackup,
} from "@/hooks/use-backups"

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

// Mock fetch response factories.
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

function errText(status: number, text: string): Response {
  return {
    ok: false,
    status,
    text: async () => text,
    json: async () => {
      throw new Error("not json")
    },
  } as unknown as Response
}

describe("use-backups", () => {
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

  describe("useBackups", () => {
    it("is disabled when workspaceId is missing — no fetch, undefined data", async () => {
      const { result } = renderHook(() => useBackups(undefined), {
        wrapper: makeWrapper(qc),
      })
      // Flush microtasks — an enabled query would have fired by now.
      await act(async () => { await Promise.resolve() })

      expect(mockFetch).not.toHaveBeenCalled()
      expect(result.current.isSuccess).toBe(false)
      expect(result.current.data).toBeUndefined()
    })

    it("fetches and returns the entries from data[]", async () => {
      mockFetch.mockResolvedValueOnce(okJSON({ data: [{ path: "/a", file_name: "a.tar.zst" }] }))

      const { result } = renderHook(() => useBackups("ws-1"), {
        wrapper: makeWrapper(qc),
      })
      await waitFor(() => expect(result.current.isSuccess).toBe(true))

      expect(mockFetch).toHaveBeenCalledWith("/api/v1/admin/backups?workspace_id=ws-1")
      expect(result.current.data).toEqual([{ path: "/a", file_name: "a.tar.zst" }])
    })

    it("returns an empty array when the server omits `data`", async () => {
      mockFetch.mockResolvedValueOnce(okJSON({}))

      const { result } = renderHook(() => useBackups("ws-1"), {
        wrapper: makeWrapper(qc),
      })
      await waitFor(() => expect(result.current.isSuccess).toBe(true))
      expect(result.current.data).toEqual([])
    })

    it("percent-encodes special characters in the workspace id", async () => {
      mockFetch.mockResolvedValueOnce(okJSON({ data: [] }))

      renderHook(() => useBackups("ws with space&x=1"), { wrapper: makeWrapper(qc) })
      await waitFor(() => expect(mockFetch).toHaveBeenCalled())

      const url = mockFetch.mock.calls[0][0] as string
      // URLSearchParams encodes space → "+", & → %26, = → %3D.
      expect(url).toContain("workspace_id=ws+with+space%26x%3D1")
    })

    it("surfaces JSON {error} as the thrown Error message", async () => {
      mockFetch.mockResolvedValueOnce(errJSON(500, { error: "backup path is invalid" }))

      const { result } = renderHook(() => useBackups("ws-1"), {
        wrapper: makeWrapper(qc),
      })
      await waitFor(() => expect(result.current.isError).toBe(true))
      expect(result.current.error?.message).toBe("backup path is invalid")
    })

    it("falls back to `detail` when `error` is missing", async () => {
      mockFetch.mockResolvedValueOnce(errJSON(500, { detail: "problem-type" }))
      const { result } = renderHook(() => useBackups("ws-1"), {
        wrapper: makeWrapper(qc),
      })
      await waitFor(() => expect(result.current.isError).toBe(true))
      expect(result.current.error?.message).toBe("problem-type")
    })

    it("uses the plain text body when the error is not JSON", async () => {
      mockFetch.mockResolvedValueOnce(errText(503, "upstream unavailable"))
      const { result } = renderHook(() => useBackups("ws-1"), {
        wrapper: makeWrapper(qc),
      })
      await waitFor(() => expect(result.current.isError).toBe(true))
      expect(result.current.error?.message).toBe("upstream unavailable")
    })

    it("falls back to HTTP status when body is empty", async () => {
      mockFetch.mockResolvedValueOnce(errText(418, ""))
      const { result } = renderHook(() => useBackups("ws-1"), {
        wrapper: makeWrapper(qc),
      })
      await waitFor(() => expect(result.current.isError).toBe(true))
      expect(result.current.error?.message).toMatch(/HTTP 418/)
    })
  })

  describe("useBackupStatus", () => {
    it("polls /status and is disabled without workspaceId", async () => {
      const { result } = renderHook(() => useBackupStatus(undefined), {
        wrapper: makeWrapper(qc),
      })
      await act(async () => { await Promise.resolve() })
      expect(mockFetch).not.toHaveBeenCalled()
      expect(result.current.data).toBeUndefined()
    })

    it("hits the status endpoint with workspace_id", async () => {
      mockFetch.mockResolvedValueOnce(okJSON({ workspace_id: "ws-1", held: false }))

      const { result } = renderHook(() => useBackupStatus("ws-1"), {
        wrapper: makeWrapper(qc),
      })
      await waitFor(() => expect(result.current.isSuccess).toBe(true))
      expect(mockFetch).toHaveBeenCalledWith(
        "/api/v1/admin/backups/status?workspace_id=ws-1",
      )
      expect(result.current.data?.held).toBe(false)
    })
  })

  describe("useInspectBackup", () => {
    it("is disabled when workspaceId OR path is missing", async () => {
      const { result } = renderHook(() => useInspectBackup(undefined, "/p"), {
        wrapper: makeWrapper(qc),
      })
      await act(async () => { await Promise.resolve() })
      expect(mockFetch).not.toHaveBeenCalled()
      expect(result.current.data).toBeUndefined()

      const { result: r2 } = renderHook(() => useInspectBackup("ws-1", null), {
        wrapper: makeWrapper(qc),
      })
      await act(async () => { await Promise.resolve() })
      expect(mockFetch).not.toHaveBeenCalled()
      expect(r2.current.data).toBeUndefined()
    })

    it("passes the path as a query param (encoded)", async () => {
      mockFetch.mockResolvedValueOnce(okJSON({ format_version: 1 }))
      renderHook(() => useInspectBackup("ws-1", "/tmp/a b.tar.zst"), {
        wrapper: makeWrapper(qc),
      })
      await waitFor(() => expect(mockFetch).toHaveBeenCalled())
      const url = mockFetch.mock.calls[0][0] as string
      expect(url).toContain("workspace_id=ws-1")
      expect(url).toContain("path=%2Ftmp%2Fa+b.tar.zst")
    })
  })

  describe("useCreateBackup", () => {
    it("throws when mutateAsync is called without workspaceId", async () => {
      const { result } = renderHook(() => useCreateBackup(undefined), {
        wrapper: makeWrapper(qc),
      })

      await expect(
        result.current.mutateAsync({ scope: "workspace" }),
      ).rejects.toThrow(/workspaceId is required/)
      expect(mockFetch).not.toHaveBeenCalled()
    })

    it("POSTs the body and invalidates backups + backup-status on success", async () => {
      mockFetch.mockResolvedValueOnce(
        okJSON({
          path: "/new.tar.zst",
          size_bytes: 10,
          payload_sha256: "sha256:aaa",
          format_version: 1,
          scope: "workspace",
          created_at: "",
          encrypted: false,
        }),
      )
      const invalidateSpy = vi.spyOn(qc, "invalidateQueries")

      const { result } = renderHook(() => useCreateBackup("ws-1"), {
        wrapper: makeWrapper(qc),
      })

      await act(async () => {
        await result.current.mutateAsync({ scope: "workspace" })
      })

      const [url, init] = mockFetch.mock.calls[0] as [string, RequestInit]
      expect(url).toBe("/api/v1/admin/backups?workspace_id=ws-1")
      expect(init.method).toBe("POST")
      expect((init.headers as Record<string, string>)["Content-Type"]).toBe("application/json")
      expect(JSON.parse(init.body as string)).toEqual({ scope: "workspace" })

      const invalidated = invalidateSpy.mock.calls.map((c) => (c[0] as { queryKey: unknown[] }).queryKey[0])
      expect(invalidated).toContain("backups")
      expect(invalidated).toContain("backup-status")
    })
  })

  describe("useRestoreBackup", () => {
    it("POSTs to /restore and invalidates both caches on success", async () => {
      mockFetch.mockResolvedValueOnce(okJSON({ ok: true }))
      const invalidateSpy = vi.spyOn(qc, "invalidateQueries")

      const { result } = renderHook(() => useRestoreBackup("ws-1"), {
        wrapper: makeWrapper(qc),
      })
      await act(async () => {
        await result.current.mutateAsync({ path: "/p" })
      })

      const [url, init] = mockFetch.mock.calls[0] as [string, RequestInit]
      expect(url).toBe("/api/v1/admin/backups/restore?workspace_id=ws-1")
      expect(init.method).toBe("POST")

      const invalidated = invalidateSpy.mock.calls.map((c) => (c[0] as { queryKey: unknown[] }).queryKey[0])
      expect(invalidated).toEqual(expect.arrayContaining(["backups", "backup-status"]))
    })
  })

  describe("useDeleteBackup", () => {
    it("sends DELETE with path as a query param", async () => {
      mockFetch.mockResolvedValueOnce(okJSON(null))

      const { result } = renderHook(() => useDeleteBackup("ws-1"), {
        wrapper: makeWrapper(qc),
      })
      await act(async () => {
        await result.current.mutateAsync("/backup/a.tar.zst")
      })

      const [url, init] = mockFetch.mock.calls[0] as [string, RequestInit]
      expect(url).toBe("/api/v1/admin/backups?workspace_id=ws-1&path=%2Fbackup%2Fa.tar.zst")
      expect(init.method).toBe("DELETE")
    })

    it("propagates JSON error messages from the server", async () => {
      mockFetch.mockResolvedValueOnce(errJSON(403, { error: "forbidden" }))

      const { result } = renderHook(() => useDeleteBackup("ws-1"), {
        wrapper: makeWrapper(qc),
      })
      await expect(result.current.mutateAsync("/p")).rejects.toThrow("forbidden")
    })
  })
})
