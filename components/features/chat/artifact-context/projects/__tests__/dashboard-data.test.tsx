import { act, renderHook, waitFor } from "@testing-library/react"
import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import { afterEach, beforeEach, expect, it, vi } from "vitest"
import type { ReactNode } from "react"
import { AUTH_EVENT } from "@/lib/api-fetch"
import { useArtifactDashboardData } from "../use-artifact-dashboard-data"
const state = vi.hoisted(() => ({ user: "alice", send: vi.fn() }))
vi.mock("@/hooks/use-auth", () => ({ useSessionSafe: () => ({ data: { user: { id: state.user } } }) }))
vi.mock("@/lib/api-fetch", () => ({ AUTH_EVENT: "auth:session-expired", apiFetch: (...args: unknown[]) => state.send(...args) }))
const scope = { workspaceId: "workspace", pageId: "page", codeVersion: 2, files: [{ path: "dashboard.json", media_type: "application/json", content: '{"schema_version":1,"collections":["sales"]}' }] }
const data = (value = 12) => ({ collection: "sales", code_version: 2, documents: [{ id: "north", version: 1, value: { revenue: value }, updated_at: "2026-10-28T08:00:00Z", source_label: "Agent", source_kind: "agent" }] })
const response = (body: unknown, status = 200) => new Response(JSON.stringify(body), { status })
function setup(props = scope) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false, gcTime: 0 } } })
  const wrapper = ({ children }: { children: ReactNode }) => <QueryClientProvider client={client}>{children}</QueryClientProvider>
  return { client, ...renderHook(options => useArtifactDashboardData(options), { initialProps: props, wrapper }) }
}
beforeEach(() => { state.user = "alice"; state.send.mockReset(); state.send.mockImplementation(() => Promise.resolve(response(data()))); vi.stubEnv("NEXT_PUBLIC_ARTIFACT_DASHBOARDS", "true") })
afterEach(() => vi.unstubAllEnvs())
it("never fetches with the flag off, invalid manifest or unresolved human", async () => {
  vi.stubEnv("NEXT_PUBLIC_ARTIFACT_DASHBOARDS", "false"); const off = setup(); await act(async () => {}); expect(state.send).not.toHaveBeenCalled(); off.unmount()
  vi.stubEnv("NEXT_PUBLIC_ARTIFACT_DASHBOARDS", "true"); const invalid = setup({ ...scope, files: [] }); await act(async () => {}); expect(state.send).not.toHaveBeenCalled(); invalid.unmount()
  state.user = ""; const anonymous = setup(); await act(async () => {}); expect(state.send).not.toHaveBeenCalled(); anonymous.unmount()
})
it("reads only the selected code version and projects data without code publication", async () => {
  const view = setup(); await waitFor(() => expect(view.result.current.collections?.[0].documents[0].value.revenue).toBe(12))
  expect(state.send.mock.calls[0][0]).toContain("/pages/%40page/artifact/data/sales?")
  expect(state.send.mock.calls[0][0]).toContain("version=2")
  state.send.mockImplementation(() => Promise.resolve(response(data(24))))
  await act(async () => { await view.client.invalidateQueries() })
  await waitFor(() => expect(view.result.current.collections?.[0].documents[0].value.revenue).toBe(24))
  expect(state.send.mock.calls.every(([, init]) => init.method === "GET")).toBe(true)
})
it.each([401, 403, 404, 409])("removes earlier private data on authoritative denial %s", async status => {
  const view = setup(); await waitFor(() => expect(view.result.current.collections).toHaveLength(1))
  state.send.mockImplementation(() => Promise.resolve(response({ error: "denied" }, status)))
  await act(async () => { await view.client.invalidateQueries() })
  await waitFor(() => expect(view.result.current.collections).toBeUndefined())
  await waitFor(() => expect(view.client.getQueryCache().getAll().some(query => query.state.data !== undefined)).toBe(false))
})
it("rejects a late old user's response and clears active data on auth retirement", async () => {
  let finish!: (response: Response) => void
  state.send.mockImplementationOnce(() => new Promise<Response>(resolve => { finish = resolve }))
  const view = setup(); await waitFor(() => expect(finish).toBeTypeOf("function"))
  state.user = "bob"; state.send.mockImplementation(() => Promise.resolve(response(data(22)))); view.rerender(scope)
  await waitFor(() => expect(view.result.current.collections?.[0].documents[0].value.revenue).toBe(22))
  await act(async () => { finish(response(data(99))) })
  expect(view.result.current.collections?.[0].documents[0].value.revenue).toBe(22)
  act(() => window.dispatchEvent(new Event(AUTH_EVENT)))
  await waitFor(() => expect(view.result.current.collections).toBeUndefined())
})

it("aborts the old shared pin and ignores its late collection response", async () => {
  let finish!: (response: Response) => void, oldSignal!: AbortSignal
  state.send.mockImplementationOnce((_url, init) => { oldSignal = init.signal; return new Promise<Response>(resolve => { finish = resolve }) })
  const view = setup(); await waitFor(() => expect(finish).toBeTypeOf("function"))
  state.send.mockImplementation(() => Promise.resolve(response({ ...data(22), code_version: 3 })))
  view.rerender({ ...scope, codeVersion: 3 })
  await waitFor(() => expect(oldSignal.aborted).toBe(true))
  await waitFor(() => expect(view.result.current.collections?.[0].code_version).toBe(3))
  await act(async () => { finish(response(data(99))) })
  expect(view.result.current.collections?.[0].documents[0].value.revenue).toBe(22)
  expect(state.send.mock.calls[1][0]).toContain("version=3")
})

it("aborts an in-flight data GET on unmount and never caches its late private answer", async () => {
  let finish!: (response: Response) => void, signal!: AbortSignal
  state.send.mockImplementationOnce((_url, init) => { signal = init.signal; return new Promise<Response>(resolve => { finish = resolve }) })
  const view = setup(); await waitFor(() => expect(finish).toBeTypeOf("function")); view.unmount()
  expect(signal.aborted).toBe(true)
  await act(async () => { finish(response(data(99))) })
  await waitFor(() => expect(view.client.getQueryCache().getAll().some(query => query.state.data !== undefined)).toBe(false))
  expect(state.send).toHaveBeenCalledTimes(1)
})

it("defaults the new Dashboard feature to off without a build-time opt-in", async () => {
 vi.stubEnv("NEXT_PUBLIC_ARTIFACT_DASHBOARDS", undefined)
 const view = setup(); await act(async () => {})
 expect(state.send).not.toHaveBeenCalled(); expect(view.result.current.enabled).toBe(false)
})
