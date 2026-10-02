import { act, renderHook, waitFor } from "@testing-library/react"
import { beforeEach, describe, expect, it, vi } from "vitest"
import { apiFetch } from "@/lib/api-fetch"
import { findNode, useTreeState, type UseTreeStateArgs } from "@/hooks/use-tree-state"
import type { FileEntry } from "@/lib/types/agent"

vi.mock("@/lib/api-fetch", () => ({ apiFetch: vi.fn() }))
const fetchMock = vi.mocked(apiFetch)
const scope: UseTreeStateArgs = { agentId: "agent", workspaceId: "ws", wsLoading: false }
function file(name: string, is_dir = false, prefix = "/workspace/"): FileEntry {
  return { name, path: `${prefix}${name}`, is_dir, size: 10, mod_time: "2026-10-02T00:00:00Z" }
}
function response(body: unknown) { return new Response(JSON.stringify(body)) }
function deferred<T>() {
  let resolve!: (value: T) => void
  const promise = new Promise<T>((r) => { resolve = r })
  return { promise, resolve }
}
beforeEach(() => { fetchMock.mockReset() })

describe("useTreeState", () => {
  it("waits for workspace resolution and does not request an unresolved agent", () => {
    const { result, rerender } = renderHook((args: UseTreeStateArgs) => useTreeState(args), { initialProps: { ...scope, wsLoading: true } })
    expect(result.current.loading).toBe(true)
    expect(fetchMock).not.toHaveBeenCalled()
    rerender({ ...scope, wsLoading: false, workspaceId: null })
    expect(result.current).toMatchObject({ loading: false, error: "No workspace selected" })
    rerender({ ...scope, agentId: null })
    expect(result.current).toMatchObject({ tree: [], loading: false, error: null })
    expect(fetchMock).not.toHaveBeenCalled()
  })

  it("sorts directories before files and lazily loads nested children once", async () => {
    fetchMock.mockResolvedValueOnce(response([file("z.txt"), file("src", true), file("a.txt")]))
      .mockResolvedValueOnce(response([file("z.ts", false, "/workspace/src/"), file("lib", true, "/workspace/src/")]))
      .mockResolvedValueOnce(response([file("deep.ts", false, "/workspace/src/lib/")]))
    const { result } = renderHook(() => useTreeState(scope))
    await waitFor(() => expect(result.current.loading).toBe(false))
    expect(result.current.tree.map((n) => n.name)).toEqual(["src", "a.txt", "z.txt"])
    expect(result.current.basePrefix).toBe("/workspace/")
    act(() => result.current.toggleFolder("/workspace/src"))
    await waitFor(() => expect(findNode(result.current.tree, "/workspace/src")?.childrenLoaded).toBe(true))
    expect(findNode(result.current.tree, "/workspace/src")?.children.map((n) => n.name)).toEqual(["lib", "z.ts"])
    expect(fetchMock.mock.calls[1][0]).toBe("/api/v1/agents/agent/files?workspace_id=ws&subdir=src")
    act(() => result.current.toggleFolder("/workspace/src/lib"))
    await waitFor(() => expect(findNode(result.current.tree, "/workspace/src/lib/deep.ts")?.name).toBe("deep.ts"))
    expect(findNode(result.current.tree, "/workspace/missing")).toBeUndefined()
    act(() => result.current.toggleFolder("/workspace/src"))
    expect(result.current.expandedPaths.has("/workspace/src")).toBe(false)
    act(() => result.current.toggleFolder("/workspace/src"))
    expect(fetchMock).toHaveBeenCalledTimes(3)
    act(() => result.current.setSelectedPath("/workspace/src/lib/deep.ts"))
    expect(result.current.selectedPath).toBe("/workspace/src/lib/deep.ts")
  })

  it("refreshes top-level metadata without losing loaded children and drops deleted roots", async () => {
    fetchMock.mockResolvedValueOnce(response([file("src", true), file("deleted.txt")]))
      .mockResolvedValueOnce(response([file("code.ts", false, "/workspace/src/")]))
      .mockResolvedValueOnce(response([{ ...file("src", true), size: 99, mod_time: "new" }, file("new.txt")]))
    const { result } = renderHook(() => useTreeState(scope))
    await waitFor(() => expect(result.current.loading).toBe(false))
    act(() => result.current.toggleFolder("/workspace/src"))
    await waitFor(() => expect(findNode(result.current.tree, "/workspace/src/code.ts")).toBeDefined())
    act(() => result.current.refresh())
    await waitFor(() => expect(findNode(result.current.tree, "/workspace/new.txt")).toBeDefined())
    expect(findNode(result.current.tree, "/workspace/deleted.txt")).toBeUndefined()
    expect(findNode(result.current.tree, "/workspace/src")).toMatchObject({ size: 99, mod_time: "new", childrenLoaded: true })
    expect(findNode(result.current.tree, "/workspace/src/code.ts")).toBeDefined()
  })

  it("clears tree, selection and expansion on agent switch and ignores old folder results", async () => {
    const folder = deferred<Response>()
    fetchMock.mockResolvedValueOnce(response([file("src", true)]))
      .mockReturnValueOnce(folder.promise).mockResolvedValueOnce(response([file("other.txt")]))
    const { result, rerender } = renderHook((args: UseTreeStateArgs) => useTreeState(args), { initialProps: scope })
    await waitFor(() => expect(result.current.loading).toBe(false))
    act(() => {
      result.current.setSelectedPath("/workspace/src")
      result.current.toggleFolder("/workspace/src")
    })
    expect(result.current.loadingDirs.has("/workspace/src")).toBe(true)
    const signal = fetchMock.mock.calls[1][1]!.signal!
    rerender({ ...scope, agentId: "new-agent" })
    expect(signal.aborted).toBe(true)
    expect(result.current.selectedPath).toBeNull()
    expect(result.current.expandedPaths.size).toBe(0)
    expect(result.current.loadingDirs.size).toBe(0)
    await waitFor(() => expect(result.current.tree[0]?.name).toBe("other.txt"))
    await act(async () => { folder.resolve(response([file("old.txt", false, "/workspace/src/")])) })
    expect(result.current.tree.map((n) => n.name)).toEqual(["other.txt"])
  })

  it.each([null, []])("accepts an empty file listing (%j)", async (body) => {
    fetchMock.mockResolvedValue(response(body))
    const { result } = renderHook(() => useTreeState(scope))
    await waitFor(() => expect(result.current.loading).toBe(false))
    expect(result.current).toMatchObject({ tree: [], basePrefix: "", error: null })
  })

  it("reports failed initial loads and recovers on refresh", async () => {
    fetchMock.mockResolvedValueOnce(new Response("", { status: 500 })).mockRejectedValueOnce(new Error("offline"))
      .mockResolvedValueOnce(response([file("ready.txt")]))
    const { result } = renderHook(() => useTreeState(scope))
    await waitFor(() => expect(result.current.error).toBe("Failed to load files"))
    act(() => result.current.refresh())
    await waitFor(() => expect(result.current.error).toBe("Network error. Is the engine running?"))
    act(() => result.current.refresh())
    await waitFor(() => expect(result.current.tree[0]?.name).toBe("ready.txt"))
    expect(result.current.error).toBeNull()
  })

  it.each(["http", "network"])("clears the folder spinner on a %s failure and permits retry", async (kind) => {
    fetchMock.mockResolvedValueOnce(response([file("src", true)]))
    if (kind === "http") fetchMock.mockResolvedValueOnce(new Response("", { status: 500 }))
    else fetchMock.mockRejectedValueOnce(new Error("offline"))
    fetchMock.mockResolvedValueOnce(response(null))
    const { result } = renderHook(() => useTreeState(scope))
    await waitFor(() => expect(result.current.loading).toBe(false))
    act(() => result.current.toggleFolder("/workspace/src"))
    await waitFor(() => expect(result.current.loadingDirs.size).toBe(0))
    expect(findNode(result.current.tree, "/workspace/src")?.childrenLoaded).toBe(false)
    act(() => result.current.toggleFolder("/workspace/src"))
    act(() => result.current.toggleFolder("/workspace/src"))
    await waitFor(() => expect(findNode(result.current.tree, "/workspace/src")?.childrenLoaded).toBe(true))
    expect(findNode(result.current.tree, "/workspace/src")?.children).toEqual([])
  })

  it.each(["response", "body"])("ignores an obsolete root %s after switching agent", async (stage) => {
    const pendingResponse = deferred<Response>()
    const pendingBody = deferred<unknown>()
    const json = vi.fn(() => pendingBody.promise)
    fetchMock.mockReturnValueOnce(stage === "response" ? pendingResponse.promise : Promise.resolve({
      ok: true, json,
    } as unknown as Response)).mockResolvedValueOnce(response([file("new.txt")]))
    const { result, rerender } = renderHook((args: UseTreeStateArgs) => useTreeState(args), { initialProps: scope })
    if (stage === "body") await waitFor(() => expect(json).toHaveBeenCalledOnce())
    rerender({ ...scope, agentId: "new-agent" })
    await waitFor(() => expect(result.current.tree[0]?.name).toBe("new.txt"))
    await act(async () => {
      pendingResponse.resolve(response([file("old.txt")]))
      pendingBody.resolve([file("old.txt")])
    })
    expect(result.current.tree.map((n) => n.name)).toEqual(["new.txt"])
  })

  it("ignores an abort rejection from the previous agent", async () => {
    let reject!: (reason: Error) => void
    const pending = new Promise<Response>((_, r) => { reject = r })
    fetchMock.mockReturnValueOnce(pending).mockResolvedValueOnce(response([file("new.txt")]))
    const { result, rerender } = renderHook((args: UseTreeStateArgs) => useTreeState(args), { initialProps: scope })
    rerender({ ...scope, agentId: "new-agent" })
    await waitFor(() => expect(result.current.tree[0]?.name).toBe("new.txt"))
    await act(async () => { reject(new DOMException("aborted", "AbortError")) })
    expect(result.current).toMatchObject({ loading: false, error: null })
  })

  it("aborts an active root load on unmount", () => {
    fetchMock.mockReturnValue(new Promise(() => {}))
    const { unmount } = renderHook(() => useTreeState(scope))
    const signal = fetchMock.mock.calls[0][1]!.signal!
    unmount()
    expect(signal.aborted).toBe(true)
  })
})
