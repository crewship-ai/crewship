import React, { type ReactNode } from "react"
import { act, cleanup, renderHook, waitFor } from "@testing-library/react"
import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import { afterEach, beforeEach, expect, it, vi } from "vitest"
import type { FencedPublishRequest } from "@/lib/pages/editor-contract"

const { api, events } = vi.hoisted(() => ({ api: vi.fn(), events: new Map<string, () => void>() }))
vi.mock("@/lib/api-fetch", () => ({ apiFetch: api }))
vi.mock("@/hooks/use-realtime", () => ({ useRealtimeEventSafe: (name: string, handler: () => void) => events.set(name, handler) }))
import { usePageApplication } from "@/hooks/use-page-application"

const clients: QueryClient[] = []
function setup() {
 const client = new QueryClient({ defaultOptions: { queries: { retry: false, gcTime: 0 }, mutations: { retry: false } } })
 clients.push(client)
 return { client, wrapper: ({ children }: { children: ReactNode }) => <QueryClientProvider client={client}>{children}</QueryClientProvider> }
}
const application = { publication: { version: 1 }, can_publish: false, artifact: { javascript: "compiled" } }
const request = { expected_source_revision: 4, expected_publication_version: 1 } as unknown as FencedPublishRequest
beforeEach(() => { api.mockReset(); events.clear() })
afterEach(() => { cleanup(); clients.splice(0).forEach(client => client.clear()); vi.restoreAllMocks() })

it.each([["", "health"], ["ws", ""]])("does not request an application without workspace/page identity (%s, %s)", async (workspace, slug) => {
 api.mockResolvedValue(new Response(JSON.stringify({ publication: null, can_publish: false })))
 const { wrapper } = setup()
 const { result } = renderHook(() => usePageApplication(workspace, slug), { wrapper })
 await act(async () => { await Promise.resolve() })
 expect(api).not.toHaveBeenCalled()
 expect(result.current.query.fetchStatus).toBe("idle")
})

it("starts a disabled application read only after it is enabled", async () => {
 api.mockResolvedValue(new Response(JSON.stringify({ publication: null, can_publish: false })))
 const { wrapper } = setup()
 const { result, rerender } = renderHook(({ enabled }) => usePageApplication("ws & one", "health / live", enabled), { wrapper, initialProps: { enabled: false } })
 expect(api).not.toHaveBeenCalled()
 rerender({ enabled: true })
 await waitFor(() => expect(result.current.query.isSuccess).toBe(true))
 const [url, options] = api.mock.calls[0]
 expect(url).toBe("/api/v1/pages/health%20%2F%20live/application?workspace_id=ws+%26+one")
 expect(options.signal).toBeInstanceOf(AbortSignal)
 expect(options.headers).toBeUndefined()
 expect(result.current.query.data?.etag).toBeUndefined()
})

it.each([null, {}, { error: "Access revoked" }, "invalid JSON"])("retains HTTP refusal status for body %j", async body => {
 api.mockResolvedValue(new Response(body === "invalid JSON" ? "not JSON" : JSON.stringify(body), { status: 403 }))
 const { wrapper } = setup()
 const { result } = renderHook(() => usePageApplication("ws", "health"), { wrapper })
 await waitFor(() => expect(result.current.query.isError).toBe(true))
 expect(result.current.query.error).toMatchObject({ status: 403, message: typeof body === "object" && body && "error" in body ? body.error : "Could not load the Page application." })
})

it.each([{ publication: null, artifact: { javascript: "orphan" } }, { publication: { version: 1 } }])("does not treat an incomplete cached artifact as a valid 304: %j", async cached => {
 const { client, wrapper } = setup()
 client.setQueryData(["page-application", "ws", "health"], { ...cached, etag: '"old"' })
 api.mockResolvedValue(new Response(null, { status: 304 }))
 const { result } = renderHook(() => usePageApplication("ws", "health"), { wrapper })
 await waitFor(() => expect(result.current.query.isError).toBe(true))
 expect(result.current.query.error).toMatchObject({ status: 304 })
})

it.each(["page.updated", "page.deleted", "realtime.reconnected"])("refreshes the selected application on %s", async event => {
 api.mockImplementation(async () => new Response(JSON.stringify(application)))
 const { wrapper } = setup()
 const { result } = renderHook(() => usePageApplication("ws", "health"), { wrapper })
 await waitFor(() => expect(result.current.query.isSuccess).toBe(true))
 await act(async () => { events.get(event)!() })
 await waitFor(() => expect(api).toHaveBeenCalledTimes(2))
})

it("publishes the exact fenced request and invalidates application, history and page lists", async () => {
 const { client, wrapper } = setup()
 const invalidate = vi.spyOn(client, "invalidateQueries")
 const publication = { version: 2, published: true }
 api.mockResolvedValue(new Response(JSON.stringify(publication)))
 const { result } = renderHook(() => usePageApplication("ws", "health", false), { wrapper })
 await act(async () => { expect(await result.current.publish.mutateAsync(request)).toEqual(publication) })
 expect(api).toHaveBeenCalledWith("/api/v1/pages/health/project/publish?workspace_id=ws", { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify(request) })
 for (const queryKey of [["page-application", "ws", "health"], ["page-publications", "ws", "health"], ["pages", "ws"]]) expect(invalidate).toHaveBeenCalledWith({ queryKey })
})

it.each([{ error: "Candidate moved" }, null])("reports refused publication without manufacturing a receipt: %j", async body => {
 const { wrapper } = setup()
 api.mockResolvedValue(new Response(JSON.stringify(body), { status: 409 }))
 const { result } = renderHook(() => usePageApplication("ws", "health", false), { wrapper })
 await act(async () => { await expect(result.current.publish.mutateAsync(request)).rejects.toThrow(body?.error ?? "Publication failed.") })
 expect(result.current.publish.data).toBeUndefined()
})
