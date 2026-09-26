import { act, renderHook, waitFor } from "@testing-library/react"
import { expect, it, vi } from "vitest"
import { useSidebarSessionSearch } from "../use-sidebar-session-search"
const api = vi.hoisted(() => vi.fn())
vi.mock("@/lib/api-fetch", () => ({ apiFetch: api }))

it("searches beyond the initial agent fanout and paginates matches on the server", async () => {
  api.mockReset()
  const agents = Array.from({ length: 14 }, (_, i) => ({ id: `a${i}`, name: `Agent ${i}`, slug: `agent-${i}`, status: "IDLE" }))
  api.mockImplementation(async (url: string) => {
    const match = url.includes("/a13/")
    const next = url.includes("offset=100")
    return Response.json(match ? [{ id: next ? "older" : "found", title: "Needle", started_at: "2026-09-25", kind: "routine" }] : [], { headers: { "X-Total-Count": match ? "101" : "0" } })
  })
  const { result } = renderHook(() => useSidebarSessionSearch("ws", agents, "Needle", "routine"))
  await waitFor(() => expect(result.current.loading).toBe(false))
  expect(result.current.rows[0].agent.id).toBe("a13")
  expect(api.mock.calls.every(([url]) => url.includes("q=Needle") && url.includes("kind=routine"))).toBe(true)
  expect(result.current.more).toBe(true)
  act(() => result.current.loadMore())
  await waitFor(() => expect(result.current.rows).toHaveLength(2))
  expect(result.current.more).toBe(false)
})
