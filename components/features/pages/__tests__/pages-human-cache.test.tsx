import React from "react"
import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import { act, cleanup, renderHook, waitFor } from "@testing-library/react"
import { afterEach, beforeEach, expect, it, vi } from "vitest"
import { usePage, usePages } from "@/hooks/use-pages"

const identity = vi.hoisted(() => ({ userId: "human-a" as string | null, status: "authenticated" }))
const request = vi.hoisted(() => vi.fn())
vi.mock("@/hooks/use-auth", () => ({ useSessionSafe: () => ({ data: identity.userId ? { user: { id: identity.userId } } : null, status: identity.status }) }))
vi.mock("@/lib/api-fetch", () => ({ apiFetch: request }))
vi.mock("@/hooks/use-realtime", () => ({ useRealtimeChannelSafe: vi.fn(), useRealtimeEventSafe: vi.fn(), useRealtimeStatusSafe: () => "disconnected" }))

const page = { id: "private-page", slug: "private", name: "Human A private title", panels: [] }
const clients: QueryClient[] = []
beforeEach(() => {
  identity.userId = "human-a"
  identity.status = "authenticated"
  request.mockImplementation(async (url: string) => Response.json(url.includes("/pages/private?") ? page : [page]))
})
afterEach(() => { cleanup(); clients.splice(0).forEach(client => client.clear()); request.mockReset() })
function mount() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false, staleTime: 30_000, refetchOnWindowFocus: false } } })
  clients.push(client)
  const wrapper = ({ children }: { children: React.ReactNode }) => <QueryClientProvider client={client}>{children}</QueryClientProvider>
  return { ...renderHook(() => ({ list: usePages("workspace"), detail: usePage("workspace", "private") }), { wrapper }), client }
}
async function loaded(view: ReturnType<typeof mount>) {
  await waitFor(() => expect(view.result.current.list.pages[0]?.name).toBe(page.name))
  await waitFor(() => expect(view.result.current.detail.raw?.name).toBe(page.name))
}
function hidden(view: ReturnType<typeof mount>) {
  expect(view.result.current.list.pages).toEqual([])
  expect(view.result.current.detail.page).toBeNull()
  expect(view.result.current.detail.raw).toBeNull()
}

it("masks fresh list, detail and raw immediately when another human is pending", async () => {
  const view = mount()
  await loaded(view)
  request.mockImplementation(() => new Promise<Response>(() => {}))
  identity.userId = "human-b"
  view.rerender()
  hidden(view)
  await waitFor(() => expect(request).toHaveBeenCalledTimes(4))
  hidden(view)
})

it("never falls back to the previous human's fresh metadata after the next human gets 503", async () => {
  const view = mount()
  await loaded(view)
  request.mockImplementation(async () => Response.json({ error: "Unavailable" }, { status: 503 }))
  identity.userId = "human-b"
  view.rerender()
  await act(async () => { await view.client.refetchQueries({ queryKey: ["pages", "workspace"] }) })
  await waitFor(() => expect(view.result.current.detail.error).not.toBeNull())
  hidden(view)
})

it.each(["loading", "unauthenticated"])("masks all cached metadata and sends no reads for an unknown human (%s)", async status => {
  const view = mount()
  await loaded(view)
  const calls = request.mock.calls.length
  identity.userId = null
  identity.status = status
  view.rerender()
  hidden(view)
  await act(async () => { await view.client.refetchQueries({ queryKey: ["pages", "workspace"] }) })
  expect(request).toHaveBeenCalledTimes(calls)
})

it.each([401, 403, 404])("masks list and raw detail after current authorization is refused (%s)", async status => {
  const view = mount()
  await loaded(view)
  request.mockImplementation(async () => Response.json({ error: "Denied" }, { status }))
  await act(async () => { await view.client.refetchQueries({ queryKey: ["pages", "workspace"] }) })
  await waitFor(() => expect(view.result.current.detail.error).not.toBeNull())
  hidden(view)
})

it("retains the same human's confirmed metadata on 503 and preserves workspace-prefix refreshes", async () => {
  const view = mount()
  await loaded(view)
  request.mockImplementation(async () => Response.json({ error: "Unavailable" }, { status: 503 }))
  await act(async () => { await view.client.refetchQueries({ queryKey: ["pages", "workspace"] }) })
  await waitFor(() => expect(view.result.current.detail.error).not.toBeNull())
  expect(view.result.current.list.pages[0]?.name).toBe(page.name)
  expect(view.result.current.detail.page?.name).toBe(page.name)
  expect(view.result.current.detail.raw?.name).toBe(page.name)
  request.mockImplementation(async (url: string) => Response.json(url.includes("/pages/private?") ? { ...page, name: "Updated title" } : [{ ...page, name: "Updated title" }]))
  await act(async () => { await view.client.invalidateQueries({ queryKey: ["pages", "workspace"] }) })
  await waitFor(() => expect(view.result.current.list.pages[0]?.name).toBe("Updated title"))
  expect(view.result.current.detail.raw?.name).toBe("Updated title")
})

it.each([401, 403, 404])("keeps denied metadata hidden through a later outage until an authorized success (%s)", async status => {
  const view = mount()
  await loaded(view)
  request.mockImplementation(async () => Response.json({ error: "Denied" }, { status }))
  await act(async () => { await view.client.refetchQueries({ queryKey: ["pages", "workspace"] }) })
  await waitFor(() => expect(view.result.current.detail.accessDenied).toBe(true))
  hidden(view)
  request.mockImplementation(async () => Response.json({ error: "Unavailable" }, { status: 503 }))
  await act(async () => { await view.client.refetchQueries({ queryKey: ["pages", "workspace"] }) })
  await waitFor(() => expect(view.result.current.detail.error).toBe("Unavailable"))
  hidden(view)
  request.mockImplementation(async (url: string) => Response.json(url.includes("/pages/private?") ? { ...page, name: "Fresh authorized title" } : [{ ...page, name: "Fresh authorized title" }]))
  await act(async () => { await view.client.refetchQueries({ queryKey: ["pages", "workspace"] }) })
  await waitFor(() => expect(view.result.current.list.pages[0]?.name).toBe("Fresh authorized title"))
  expect(view.result.current.detail.page?.name).toBe("Fresh authorized title")
  expect(view.result.current.detail.raw?.name).toBe("Fresh authorized title")
})
