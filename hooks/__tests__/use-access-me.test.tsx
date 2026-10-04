import { act, renderHook, waitFor } from "@testing-library/react"
import { beforeEach, describe, expect, it, vi } from "vitest"
import { useAccessMe } from "../use-access-me"

const fetchAccess = vi.hoisted(() => vi.fn())
vi.mock("@/lib/api-fetch", () => ({ apiFetch: fetchAccess }))

const answer = (state: string) => ({ ok: true, json: async () => ({ actions: { run: { state, reason: "runtime_preflight_required" } } }) })

beforeEach(() => fetchAccess.mockReset())

describe("useAccessMe", () => {
  it("discards a prior allowed answer on refresh failure and recovers", async () => {
    fetchAccess.mockResolvedValueOnce(answer("conditional"))
    const view = renderHook(() => useAccessMe("/access/me"))
    await waitFor(() => expect(view.result.current.access?.actions.run.state).toBe("conditional"))
    fetchAccess.mockResolvedValueOnce({ ok: false, status: 500 })
    await act(async () => { await view.result.current.refresh() })
    expect(view.result.current.access).toBeNull()
    expect(view.result.current.error).toBe(true)
    fetchAccess.mockResolvedValueOnce(answer("denied"))
    await act(async () => { await view.result.current.refresh() })
    expect(view.result.current.access?.actions.run.state).toBe("denied")
  })

  it("never exposes an answer from the previous identity", async () => {
    let answerOld!: (value: ReturnType<typeof answer>) => void
    fetchAccess.mockImplementationOnce(() => new Promise(resolve => { answerOld = resolve }))
    fetchAccess.mockResolvedValueOnce(answer("denied"))
    const view = renderHook(({ url }) => useAccessMe(url), { initialProps: { url: "/old" } })
    view.rerender({ url: "/new" })
    expect(view.result.current.access).toBeNull()
    await waitFor(() => expect(view.result.current.access?.actions.run.state).toBe("denied"))
    await act(async () => answerOld(answer("allowed")))
    expect(view.result.current.access?.actions.run.state).toBe("denied")
  })

  it("treats malformed responses as unknown", async () => {
    fetchAccess.mockResolvedValue({ ok: true, json: async () => ({ actions: { run: { state: "sure", reason: "role" } } }) })
    const view = renderHook(() => useAccessMe("/access/me"))
    await waitFor(() => expect(view.result.current.error).toBe(true))
    expect(view.result.current.access).toBeNull()
  })
})

it.each([null, false, {}, { actions: null }, { actions: [] }, { actions: "allowed" }, { actions: { run: null } }, { actions: { run: "allowed" } }, { actions: { run: { state: "allowed", reason: 1 } } }])("rejects an unreadable access envelope %j", async (body) => {
  fetchAccess.mockResolvedValue({ ok: true, json: async () => body })
  const { result } = renderHook(() => useAccessMe("/access/me"))
  await waitFor(() => expect(result.current.loading).toBe(false))
  expect(result.current.access).toBeNull()
  expect(result.current.error).toBe(true)
})

it("clears permissions when the identity URL disappears and ignores an old rejection", async () => {
  let reject!: (error: Error) => void
  fetchAccess.mockResolvedValueOnce(answer("allowed")).mockImplementationOnce(() => new Promise((_, fail) => { reject = fail }))
  const { result, rerender } = renderHook(({ url }: { url?: string }) => useAccessMe(url), { initialProps: { url: "/access/me" } as { url?: string } })
  await waitFor(() => expect(result.current.access?.actions.run.state).toBe("allowed"))
  act(() => { void result.current.refresh() })
  expect(result.current.access).toBeNull()
  const signal = fetchAccess.mock.calls[1][1].signal as AbortSignal
  rerender({ url: undefined })
  expect(signal.aborted).toBe(true)
  expect(result.current).toMatchObject({ access: null, loading: false, error: false })
  await act(async () => { reject(new Error("old connection lost")) })
  expect(result.current).toMatchObject({ access: null, loading: false, error: false })
  expect(fetchAccess).toHaveBeenCalledTimes(2)
})
