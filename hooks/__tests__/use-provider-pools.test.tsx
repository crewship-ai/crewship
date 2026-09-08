import React from "react"
import { act, renderHook, waitFor } from "@testing-library/react"
import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import { beforeEach, expect, it, vi } from "vitest"
import { apiFetch } from "@/lib/api-fetch"
import { poolKeys, useProviderPools } from "../use-provider-pools"
vi.mock("@/lib/api-fetch", () => ({ apiFetch: vi.fn() }))
function setup() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false, gcTime: 0 } } })
  return { client, wrapper: ({ children }: { children: React.ReactNode }) => <QueryClientProvider client={client}>{children}</QueryClientProvider> }
}
beforeEach(() => {
  vi.mocked(apiFetch).mockReset()
  vi.mocked(apiFetch).mockImplementation(async (url, init) => Response.json(String(url).includes("credentials?") ? [] : { items: [{ id: new Headers(init?.headers).get("X-Workspace-ID") }], next_cursor: "next" }))
})
it("does not fetch or expose cached data when disabled", async () => {
  const { client, wrapper } = setup()
  client.setQueryData(poolKeys.list("ws", ""), { items: [{ id: "private" }] })
  const { result } = renderHook(() => useProviderPools("ws", false), { wrapper })
  expect(result.current.pools).toBeUndefined()
  expect(apiFetch).not.toHaveBeenCalled()
  await expect(result.current.detail("private")).rejects.toThrow("not available")
  expect(apiFetch).not.toHaveBeenCalled()
})
it("isolates cached pages and forwards cursors and cancellation signals", async () => {
  const { wrapper } = setup()
  const { result, rerender } = renderHook(({ ws, after }) => useProviderPools(ws, true, after), { wrapper, initialProps: { ws: "one", after: "" } })
  await waitFor(() => expect(result.current.pools?.items[0].id).toBe("one"))
  rerender({ ws: "two", after: "cursor +/" })
  expect(result.current.pools).toBeUndefined()
  await waitFor(() => expect(result.current.pools?.items[0].id).toBe("two"))
  expect(vi.mocked(apiFetch).mock.calls.some(([url, init]) => String(url).includes("after=cursor%20%2B%2F") && !!init?.signal)).toBe(true)
})
it("hides cached metadata after a permission failure", async () => {
  const { wrapper } = setup()
  const { result } = renderHook(() => useProviderPools("ws", true), { wrapper })
  await waitFor(() => expect(result.current.pools).toBeDefined())
  vi.mocked(apiFetch).mockResolvedValue(new Response(null, { status: 403 }))
  act(() => result.current.reload())
  await waitFor(() => expect(result.current.error).toBeTruthy())
  expect(result.current.pools).toBeUndefined()
})
