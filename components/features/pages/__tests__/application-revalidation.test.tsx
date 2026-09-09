import React, { type ReactNode } from "react"
import { afterEach, expect, it, vi } from "vitest"
import { act, cleanup, renderHook, waitFor } from "@testing-library/react"
import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import { usePageApplication } from "@/hooks/use-page-application"
const api = vi.hoisted(() => vi.fn())
vi.mock("@/lib/api-fetch", () => ({ apiFetch: api }))
vi.mock("@/hooks/use-realtime", () => ({ useRealtimeEventSafe: vi.fn() }))
afterEach(() => { cleanup(); api.mockReset() })
it("revalidates the opened artifact conditionally but exposes a later authorization failure", async () => {
  const application = { publication: { version: 1 }, can_publish: false, artifact: { javascript: "compiled" } }
  api.mockResolvedValueOnce(new Response(JSON.stringify(application), { headers: { ETag: '"release-one"' } }))
  api.mockResolvedValueOnce(new Response(null, { status: 304 }))
  api.mockResolvedValueOnce(new Response(JSON.stringify({ error: "Page not found" }), { status: 404 }))
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  const wrapper = ({ children }: { children: ReactNode }) => <QueryClientProvider client={client}>{children}</QueryClientProvider>
  const { result, unmount } = renderHook(() => usePageApplication("ws", "health"), { wrapper })
  await waitFor(() => expect(result.current.query.isSuccess).toBe(true))
  await act(async () => { await result.current.query.refetch() })
  expect(api.mock.calls[1][1].headers).toEqual({ "If-None-Match": '"release-one"' })
  expect(result.current.query.data?.artifact?.javascript).toBe("compiled")
  await act(async () => { await result.current.query.refetch() })
  await waitFor(() => expect(result.current.query.isError).toBe(true))
  expect(result.current.query.error?.message).toBe("Page not found")
  unmount(); client.clear()
})
