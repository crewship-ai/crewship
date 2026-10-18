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
