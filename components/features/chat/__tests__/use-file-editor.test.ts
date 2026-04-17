import { describe, it, expect, vi, beforeEach, afterEach } from "vitest"

const toastSuccess = vi.fn()
const toastError = vi.fn()
vi.mock("sonner", () => ({
  toast: {
    success: (...args: unknown[]) => toastSuccess(...args),
    error: (...args: unknown[]) => toastError(...args),
  },
}))

import { renderHook, act } from "@testing-library/react"
import { useFileEditor } from "../hooks/use-file-editor"

async function flushAsync() {
  for (let i = 0; i < 6; i++) await Promise.resolve()
}

function okText(body: string): Response {
  return { ok: true, status: 200, text: async () => body } as unknown as Response
}

function errText(status: number): Response {
  return {
    ok: false,
    status,
    text: async () => "err",
  } as unknown as Response
}

describe("useFileEditor", () => {
  let mockFetch: ReturnType<typeof vi.fn>

  beforeEach(() => {
    toastSuccess.mockReset()
    toastError.mockReset()
    mockFetch = vi.fn()
    vi.stubGlobal("fetch", mockFetch)
  })

  afterEach(() => {
    vi.unstubAllGlobals()
  })

  it("starts with everything closed and empty", () => {
    const { result } = renderHook(() => useFileEditor({ agentId: "a1", workspaceId: "ws-1" }))
    expect(result.current.editorFile).toBeNull()
    expect(result.current.editorContent).toBeNull()
    expect(result.current.editorLoading).toBe(false)
    expect(result.current.editorDirty).toBe(false)
    expect(result.current.editorExpanded).toBe(false)
    expect(result.current.editorSaving).toBe(false)
    expect(result.current.saveRef.current).toBeNull()
  })

  it("openFileEditor is a no-op when workspaceId is null", () => {
    const { result } = renderHook(() => useFileEditor({ agentId: "a1", workspaceId: null }))

    act(() => {
      result.current.openFileEditor({ path: "/w/a.ts", name: "a.ts" })
    })

    expect(mockFetch).not.toHaveBeenCalled()
    expect(result.current.editorFile).toBeNull()
  })

  it("openFileEditor issues an encoded GET to the download endpoint", () => {
    mockFetch.mockResolvedValueOnce(okText("console.log('hello')"))

    const { result } = renderHook(() => useFileEditor({ agentId: "a1", workspaceId: "ws-1" }))

    act(() => {
      result.current.openFileEditor({ path: "src/app with space.ts", name: "app with space.ts" })
    })

    const [url, init] = mockFetch.mock.calls[0] as [string, RequestInit]
    expect(url).toBe(
      "/api/v1/agents/a1/files/download?workspace_id=ws-1&path=src%2Fapp%20with%20space.ts",
    )
    expect(init.signal).toBeInstanceOf(AbortSignal)
    expect(result.current.editorFile).toEqual({ path: "src/app with space.ts", name: "app with space.ts" })
    expect(result.current.editorLoading).toBe(true)
  })

  it("populates editorContent on a successful fetch", async () => {
    mockFetch.mockResolvedValueOnce(okText("the contents"))

    const { result } = renderHook(() => useFileEditor({ agentId: "a1", workspaceId: "ws-1" }))

    await act(async () => {
      result.current.openFileEditor({ path: "a.ts", name: "a.ts" })
      await flushAsync()
    })

    expect(result.current.editorContent).toBe("the contents")
    expect(result.current.editorLoading).toBe(false)
  })

  it("toast.errors and clears content on a non-OK response", async () => {
    mockFetch.mockResolvedValueOnce(errText(500))

    const { result } = renderHook(() => useFileEditor({ agentId: "a1", workspaceId: "ws-1" }))
    await act(async () => {
      result.current.openFileEditor({ path: "a.ts", name: "a.ts" })
      await flushAsync()
    })

    expect(toastError).toHaveBeenCalledWith("Failed to load file")
    expect(result.current.editorContent).toBeNull()
    expect(result.current.editorLoading).toBe(false)
  })

  it("toast.errors when the request rejects", async () => {
    mockFetch.mockRejectedValueOnce(new Error("net down"))

    const { result } = renderHook(() => useFileEditor({ agentId: "a1", workspaceId: "ws-1" }))
    await act(async () => {
      result.current.openFileEditor({ path: "a.ts", name: "a.ts" })
      await flushAsync()
    })

    expect(toastError).toHaveBeenCalledWith("Failed to load file")
  })

  it("aborts the in-flight request when openFileEditor is called again", () => {
    // Never-resolving fetch so we can observe the abort signal.
    mockFetch.mockImplementation((_, init: RequestInit) => {
      return new Promise((_resolve, reject) => {
        const sig = init.signal as AbortSignal
        sig.addEventListener("abort", () => reject(new DOMException("aborted", "AbortError")))
      })
    })

    const { result } = renderHook(() => useFileEditor({ agentId: "a1", workspaceId: "ws-1" }))

    act(() => {
      result.current.openFileEditor({ path: "first.ts", name: "first.ts" })
    })
    const firstSignal = (mockFetch.mock.calls[0][1] as RequestInit).signal as AbortSignal
    expect(firstSignal.aborted).toBe(false)

    act(() => {
      result.current.openFileEditor({ path: "second.ts", name: "second.ts" })
    })
    expect(firstSignal.aborted).toBe(true)
  })

  it("closeEditor aborts and resets all editor state", () => {
    mockFetch.mockImplementation((_, init: RequestInit) => {
      return new Promise((_resolve, reject) => {
        ;(init.signal as AbortSignal).addEventListener("abort", () =>
          reject(new DOMException("aborted", "AbortError")),
        )
      })
    })

    const { result } = renderHook(() => useFileEditor({ agentId: "a1", workspaceId: "ws-1" }))
    act(() => {
      result.current.openFileEditor({ path: "a.ts", name: "a.ts" })
    })
    const signal = (mockFetch.mock.calls[0][1] as RequestInit).signal as AbortSignal

    act(() => {
      result.current.setEditorDirty(true)
      result.current.setEditorExpanded(true)
      result.current.closeEditor()
    })

    expect(signal.aborted).toBe(true)
    expect(result.current.editorFile).toBeNull()
    expect(result.current.editorContent).toBeNull()
    expect(result.current.editorLoading).toBe(false)
    expect(result.current.editorDirty).toBe(false)
    expect(result.current.editorExpanded).toBe(false)
  })

  describe("handleEditorSave", () => {
    it("is a no-op when workspaceId is null", () => {
      const { result } = renderHook(() => useFileEditor({ agentId: "a1", workspaceId: null }))
      act(() => { result.current.handleEditorSave("x") })
      expect(mockFetch).not.toHaveBeenCalled()
    })

    it("is a no-op when no editorFile is open", () => {
      const { result } = renderHook(() => useFileEditor({ agentId: "a1", workspaceId: "ws-1" }))
      act(() => { result.current.handleEditorSave("x") })
      expect(mockFetch).not.toHaveBeenCalled()
    })

    it("PUTs to the save endpoint and clears dirty on success", async () => {
      mockFetch.mockResolvedValueOnce(okText("download")) // initial open
      mockFetch.mockResolvedValueOnce({ ok: true } as Response) // save

      const { result } = renderHook(() => useFileEditor({ agentId: "a1", workspaceId: "ws-1" }))
      await act(async () => {
        result.current.openFileEditor({ path: "src/a.ts", name: "a.ts" })
        await flushAsync()
      })
      act(() => {
        result.current.setEditorDirty(true)
      })
      await act(async () => {
        result.current.handleEditorSave("new content")
        await flushAsync()
      })

      const [url, init] = mockFetch.mock.calls[1] as [string, RequestInit]
      expect(url).toBe("/api/v1/agents/a1/files/save?workspace_id=ws-1&path=src%2Fa.ts")
      expect(init.method).toBe("PUT")
      expect((init.headers as Record<string, string>)["Content-Type"]).toBe("text/plain")
      expect(init.body).toBe("new content")
      expect(toastSuccess).toHaveBeenCalledWith("File saved")
      expect(result.current.editorDirty).toBe(false)
      expect(result.current.editorSaving).toBe(false)
    })

    it("toasts an error when the save endpoint returns non-OK", async () => {
      mockFetch.mockResolvedValueOnce(okText("c")) // open
      mockFetch.mockResolvedValueOnce({ ok: false, status: 403 } as Response) // save

      const { result } = renderHook(() => useFileEditor({ agentId: "a1", workspaceId: "ws-1" }))
      await act(async () => {
        result.current.openFileEditor({ path: "a.ts", name: "a.ts" })
        await flushAsync()
      })
      await act(async () => {
        result.current.handleEditorSave("x")
        await flushAsync()
      })

      expect(toastError).toHaveBeenCalledWith("Save failed")
      expect(result.current.editorSaving).toBe(false)
    })

    it("toasts an error when the save endpoint rejects", async () => {
      mockFetch.mockResolvedValueOnce(okText("c"))
      mockFetch.mockRejectedValueOnce(new Error("boom"))

      const { result } = renderHook(() => useFileEditor({ agentId: "a1", workspaceId: "ws-1" }))
      await act(async () => {
        result.current.openFileEditor({ path: "a.ts", name: "a.ts" })
        await flushAsync()
      })
      await act(async () => {
        result.current.handleEditorSave("x")
        await flushAsync()
      })

      expect(toastError).toHaveBeenCalledWith("Save failed")
    })
  })

  it("resets editor state when agentId or workspaceId changes", async () => {
    mockFetch.mockImplementation((_, init: RequestInit) => {
      return new Promise((_resolve, reject) => {
        ;(init.signal as AbortSignal).addEventListener("abort", () =>
          reject(new DOMException("aborted", "AbortError")),
        )
      })
    })

    const { result, rerender } = renderHook(
      ({ agentId, workspaceId }: { agentId: string; workspaceId: string | null }) =>
        useFileEditor({ agentId, workspaceId }),
      { initialProps: { agentId: "a1", workspaceId: "ws-1" } },
    )

    act(() => {
      result.current.openFileEditor({ path: "a.ts", name: "a.ts" })
    })
    const firstSignal = (mockFetch.mock.calls[0][1] as RequestInit).signal as AbortSignal
    act(() => {
      result.current.setEditorDirty(true)
      result.current.setEditorExpanded(true)
    })

    rerender({ agentId: "a2", workspaceId: "ws-1" })

    expect(firstSignal.aborted).toBe(true)
    expect(result.current.editorFile).toBeNull()
    expect(result.current.editorContent).toBeNull()
    expect(result.current.editorDirty).toBe(false)
    expect(result.current.editorExpanded).toBe(false)
  })
})
