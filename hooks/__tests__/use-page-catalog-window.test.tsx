import React from "react"
import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import { cleanup, renderHook, waitFor } from "@testing-library/react"
import { afterEach, expect, it, vi } from "vitest"
import { usePageCatalogWindow } from "@/hooks/use-pages"

const identity = vi.hoisted(() => ({ userId: "human-a" as string | null }))
const request = vi.hoisted(() => vi.fn())
vi.mock("@/hooks/use-auth", () => ({ useSessionSafe: () => ({ data: identity.userId ? { user: { id: identity.userId } } : null, status: "authenticated" }) }))
vi.mock("@/lib/api-fetch", () => ({ apiFetch: request }))
vi.mock("@/hooks/use-realtime", () => ({ useRealtimeChannelSafe: vi.fn(), useRealtimeEventSafe: vi.fn(), useRealtimeStatusSafe: () => "disconnected" }))

afterEach(() => { cleanup(); request.mockReset(); identity.userId = "human-a" })
function mount(params: URLSearchParams | null, offset = 0) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  const wrapper = ({ children }: { children: React.ReactNode }) => <QueryClientProvider client={client}>{children}</QueryClientProvider>
  return renderHook(() => usePageCatalogWindow("ws1", params, offset, 50), { wrapper })
}

it("sends nothing while idle, so the flag-off library makes no extra request", async () => {
  const view = mount(null)
  await new Promise(resolve => setTimeout(resolve, 10))
  expect(request).not.toHaveBeenCalled()
  expect(view.result.current).toMatchObject({ pages: [], total: 0, loading: false })
})

it("asks for one filtered window and reads the authorized total from X-Total-Count", async () => {
  request.mockImplementation(async () => new Response(JSON.stringify([{ id: "p1", slug: "plan", name: "Plan", owner: "user/human-a" }]), { headers: { "Content-Type": "application/json", "X-Total-Count": "120" } }))
  const view = mount(new URLSearchParams({ empty: "exclude", sort: "name", q: "plan" }), 50)
  await waitFor(() => expect(view.result.current.total).toBe(120))
  expect(view.result.current.pages.map(page => page.slug)).toEqual(["plan"])
  const url = new URL(request.mock.calls[0][0] as string, "http://x")
  expect(url.pathname).toBe("/api/v1/pages")
  expect(Object.fromEntries(url.searchParams)).toEqual({ empty: "exclude", sort: "name", q: "plan", workspace_id: "ws1", limit: "50", offset: "50" })
})

it("drops the window on a refusal instead of keeping earlier private metadata", async () => {
  request.mockImplementation(async () => Response.json({ error: "Forbidden" }, { status: 403 }))
  const view = mount(new URLSearchParams({ sort: "updated" }))
  await waitFor(() => expect(view.result.current.error).not.toBeNull())
  expect(view.result.current).toMatchObject({ pages: [], total: 0 })
})
