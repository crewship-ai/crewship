import React from "react"
import { act, cleanup, renderHook } from "@testing-library/react"
import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import { afterEach, expect, it, vi } from "vitest"
import { useChatIssueMutation } from "../use-chat-issue-mutation"
const { fetch } = vi.hoisted(() => ({ fetch: vi.fn() }))
vi.mock("@/lib/api-fetch", () => ({ apiFetch: fetch }))
afterEach(() => { cleanup(); fetch.mockReset(); sessionStorage.clear() })
function setup(scope?: string) {
  const client = new QueryClient({ defaultOptions: { mutations: { retry: false } } })
  const changed = vi.fn(), unauthorized = vi.fn()
  const wrapper = ({ children }: { children: React.ReactNode }) => <QueryClientProvider client={client}>{children}</QueryClientProvider>
  return { ...renderHook(() => useChatIssueMutation(changed, unauthorized, scope), { wrapper }), changed, unauthorized }
}
it("retains the exact precondition and retry key after a lost acknowledgement", async () => {
  fetch.mockRejectedValueOnce(new Error("network"))
  const { result, changed } = setup()
  await act(async () => { await result.current.submit("/issue?workspace_id=w", "PATCH", { title: "draft", expected_revision: 7 }) })
  expect(result.current.state).toBe("unknown")
  await act(async () => { expect(await result.current.submit("/issue", "PATCH", { title: "different" })).toBe(false) })
  expect(fetch).toHaveBeenCalledTimes(1)
  fetch.mockResolvedValueOnce(new Response("{}", { status: 200 }))
  await act(async () => { await result.current.retry() })
  expect(fetch.mock.calls[1]).toEqual(fetch.mock.calls[0])
  expect(result.current.state).toBe("saved")
  expect(changed).toHaveBeenCalledTimes(1)
})
it("requires explicit conflict resolution before a fresh operation", async () => {
  fetch.mockResolvedValueOnce(new Response('{"error":"revision_conflict","revision":2}', { status: 409 }))
  const { result } = setup()
  await act(async () => { await result.current.submit("/issue", "PATCH", { title: "draft", expected_revision: 1 }) })
  expect(result.current.state).toBe("conflict")
  act(() => result.current.resolveConflict())
  fetch.mockResolvedValueOnce(new Response("{}"))
  await act(async () => { await result.current.submit("/issue", "PATCH", { title: "draft", expected_revision: 2 }) })
  expect(fetch.mock.calls[1][1].headers["Idempotency-Key"]).not.toEqual(fetch.mock.calls[0][1].headers["Idempotency-Key"])
})
it("signals reauthorization without accepting a denied action", async () => {
  fetch.mockResolvedValueOnce(new Response("{}", { status: 403 }))
  const { result, unauthorized, changed } = setup()
  await act(async () => { await result.current.submit("/issue", "POST", { body: "draft" }) })
  expect(result.current.state).toBe("rechecking")
  expect(unauthorized).toHaveBeenCalledOnce()
  expect(changed).not.toHaveBeenCalled()
})

it.each([401, 403, 404])("retains an uncertain write after a retry is denied with %s", async (status) => {
  fetch.mockRejectedValueOnce(new Error("lost acknowledgement"))
  const { result, unauthorized, changed } = setup()
  await act(async () => { await result.current.submit("/issue?workspace_id=w", "POST", { body: "One comment" }) })
  const original = fetch.mock.calls[0]
  fetch.mockResolvedValueOnce(new Response("{}", { status }))
  await act(async () => { await result.current.retry() })
  expect(result.current.state).toBe("unknown")
  expect(unauthorized).toHaveBeenCalledOnce()
  await act(async () => { expect(await result.current.submit("/issue?workspace_id=w", "POST", { body: "One comment" })).toBe(false) })
  expect(fetch).toHaveBeenCalledTimes(2)
  fetch.mockResolvedValueOnce(new Response("{}"))
  await act(async () => { await result.current.retry() })
  expect(fetch.mock.calls[1]).toEqual(original)
  expect(fetch.mock.calls[2]).toEqual(original)
  expect(result.current.state).toBe("saved")
  expect(changed).toHaveBeenCalledOnce()
})

it("restores an uncertain comment after remount and retries the original key", async () => {
  fetch.mockRejectedValueOnce(new Error("lost acknowledgement"))
  const first = setup("viewer/workspace/issue")
  await act(async () => { await first.result.current.submit("/issue?workspace_id=w", "POST", { body: "Only once" }) })
  const original = fetch.mock.calls[0]
  first.unmount()
  const second = setup("viewer/workspace/issue")
  expect(second.result.current.state).toBe("unknown")
  await act(async () => { expect(await second.result.current.submit("/issue", "POST", { body: "Only once" })).toBe(false) })
  fetch.mockResolvedValueOnce(new Response("{}"))
  await act(async () => { await second.result.current.retry() })
  expect(fetch.mock.calls[1]).toEqual(original)
  expect(sessionStorage.length).toBe(0)
})
it("does not restore another viewer's intent", async () => {
  fetch.mockRejectedValueOnce(new Error("lost acknowledgement"))
  const first = setup("viewer-a/workspace/issue")
  await act(async () => { await first.result.current.submit("/issue", "POST", { body: "Private draft" }) })
  first.unmount()
  const other = setup("viewer-b/workspace/issue")
  expect(other.result.current.state).toBe("idle")
  await act(async () => { expect(await other.result.current.retry()).toBe(false) })
  expect(fetch).toHaveBeenCalledTimes(1)
})
it("sends no write when recovery storage cannot be recorded", async () => {
  const set = vi.spyOn(Storage.prototype, "setItem").mockImplementation(() => { throw new Error("unavailable") })
  try {
    const { result } = setup("viewer/workspace/issue")
    await act(async () => { expect(await result.current.submit("/issue", "POST", { body: "Do not duplicate" })).toBe(false) })
    expect(result.current.state).toBe("validation")
    expect(fetch).not.toHaveBeenCalled()
  } finally { set.mockRestore() }
})
