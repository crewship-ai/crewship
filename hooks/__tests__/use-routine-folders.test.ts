import { act, renderHook, waitFor } from "@testing-library/react"
import { beforeEach, expect, it, vi } from "vitest"
import { useRoutineFolders } from "../use-routine-folders"

const { fetch } = vi.hoisted(() => ({ fetch: vi.fn() }))
vi.mock("@/lib/api-fetch", () => ({ apiFetch: fetch }))

const json = (body: unknown, status = 200) => new Response(JSON.stringify(body), { status })
beforeEach(() => fetch.mockReset())

it("lists the folders and creates one, then reloads (ODCH-RT-01)", async () => {
  fetch
    .mockResolvedValueOnce(json([{ id: "f1", name: "Ops", routine_count: 1 }]))
    .mockResolvedValueOnce(json({ id: "f2", name: "Intel", routine_count: 0 }, 201))
    .mockResolvedValueOnce(json([{ id: "f1", name: "Ops", routine_count: 1 }, { id: "f2", name: "Intel", routine_count: 0 }]))
  const view = renderHook(() => useRoutineFolders("ws"))
  await waitFor(() => expect(view.result.current.folders).toHaveLength(1))
  let err: string | null = "unset"
  await act(async () => {
    err = await view.result.current.create("Intel")
  })
  expect(err).toBeNull()
  expect(fetch.mock.calls[1][0]).toBe("/api/v1/workspaces/ws/pipeline-folders")
  expect(JSON.parse(fetch.mock.calls[1][1].body)).toEqual({ name: "Intel" })
  expect(view.result.current.byId.get("f2")?.name).toBe("Intel")
})

it("says what the server said when a name is taken", async () => {
  fetch.mockResolvedValueOnce(json([])).mockResolvedValueOnce(json({ error: 'a folder named "Ops" already exists' }, 409))
  const view = renderHook(() => useRoutineFolders("ws"))
  let err: string | null = null
  await act(async () => {
    err = await view.result.current.create("Ops")
  })
  expect(err).toBe('a folder named "Ops" already exists')
})

it("moves routines in or out, then reloads and tells the list to reload", async () => {
  const moved = vi.fn()
  fetch.mockResolvedValue(json([]))
  const view = renderHook(() => useRoutineFolders("ws", moved))
  await act(async () => {
    expect(await view.result.current.move(["a", "b"], null)).toBeNull()
  })
  const call = fetch.mock.calls.find(([url]) => String(url).endsWith("/move"))!
  expect(JSON.parse(call[1].body)).toEqual({ slugs: ["a", "b"], folder_id: null })
  expect(moved).toHaveBeenCalled()
})

it("creates a subfolder, updates and deletes a folder, then reloads (#3768)", async () => {
  fetch.mockResolvedValue(json([]))
  const view = renderHook(() => useRoutineFolders("ws"))
  await act(async () => {
    expect(await view.result.current.create("Daily", "f1")).toBeNull()
    expect(await view.result.current.update("f2", { icon: "rocket", color: "blue" })).toBeNull()
    expect(await view.result.current.update("f2", { parent_id: null })).toBeNull()
  })
  fetch.mockResolvedValueOnce(new Response(null, { status: 204 }))
  await act(async () => {
    expect(await view.result.current.remove("f2")).toBeNull()
  })
  const calls = fetch.mock.calls.filter(([, init]) => init?.method)
  expect(calls.map(([url, init]) => [init.method, url, init.body ? JSON.parse(init.body) : null])).toEqual([
    ["POST", "/api/v1/workspaces/ws/pipeline-folders", { name: "Daily", parent_id: "f1" }],
    ["PATCH", "/api/v1/workspaces/ws/pipeline-folders/f2", { icon: "rocket", color: "blue" }],
    ["PATCH", "/api/v1/workspaces/ws/pipeline-folders/f2", { parent_id: null }],
    ["DELETE", "/api/v1/workspaces/ws/pipeline-folders/f2", null],
  ])
})

it("hands back the server's refusal for an update", async () => {
  fetch.mockResolvedValueOnce(json([])).mockResolvedValueOnce(json({ error: "a folder cannot go inside itself or one of its subfolders" }, 400))
  const view = renderHook(() => useRoutineFolders("ws"))
  let err: string | null = null
  await act(async () => {
    err = await view.result.current.update("f1", { parent_id: "f2" })
  })
  expect(err).toBe("a folder cannot go inside itself or one of its subfolders")
})
