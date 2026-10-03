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

it.each([
  { body: { error: "Consent is required" }, message: "Consent is required" },
  { body: { error: "x".repeat(450) }, message: "x".repeat(400) },
  { body: { error: 42 }, message: "Check the group name, accounts and owner consent." },
  { body: null, message: "Check the group name, accounts and owner consent." },
])("reports a bounded server refusal for $body", async ({ body, message }) => {
  const { wrapper } = setup()
  const { result } = renderHook(() => useProviderPools("ws", true), { wrapper })
  await waitFor(() => expect(result.current.loading).toBe(false))
  vi.mocked(apiFetch).mockResolvedValueOnce(Response.json(body, { status: 400 }))
  await expect(result.current.detail("group /one")).rejects.toThrow(message)
  expect(vi.mocked(apiFetch).mock.lastCall?.[0]).toBe("/api/v1/provider-logins/pools/group%20%2Fone")
})

it.each([400, 502])("reports a malformed or unavailable %i response without leaking its body", async (status) => {
  const { wrapper } = setup()
  const { result } = renderHook(() => useProviderPools("ws", true), { wrapper })
  await waitFor(() => expect(result.current.loading).toBe(false))
  vi.mocked(apiFetch).mockResolvedValueOnce(new Response("PRIVATE upstream diagnostic", { status }))
  await expect(result.current.detail("group")).rejects.toThrow(status === 400 ? "Check the group name, accounts and owner consent." : "Account group request failed (502). Please retry.")
})

it("creates, revises and removes only the selected workspace group using its revision", async () => {
  const { client, wrapper } = setup()
  const invalidate = vi.spyOn(client, "invalidateQueries")
  const { result } = renderHook(() => useProviderPools("ws", true), { wrapper })
  await waitFor(() => expect(result.current.loading).toBe(false))
  const draft = { name: "Pool", provider: "OPENAI", mode: "api_key" as const, allow_cross_owner: false, members: [{ credential_id: "key", priority: 1 }] }
  const current = { ...draft, id: "pool /one", revision: 7, member_count: 1 }
  await act(async () => { await result.current.save(draft) })
  await act(async () => { await result.current.save({ ...draft, name: "Renamed" }, current) })
  await act(async () => { await result.current.remove(current) })
  const writes = vi.mocked(apiFetch).mock.calls.filter(([, init]) => ["POST", "PUT", "DELETE"].includes(init?.method ?? ""))
  expect(writes).toHaveLength(3)
  const [create, update, remove] = writes
  expect(create[0]).toBe("/api/v1/provider-logins/pools")
  expect(JSON.parse(String(create[1]?.body))).toEqual(draft)
  expect(new Headers(create[1]?.headers).has("If-Match")).toBe(false)
  expect(update[0]).toBe("/api/v1/provider-logins/pools/pool%20%2Fone")
  expect(JSON.parse(String(update[1]?.body))).toEqual({ name: "Renamed", allow_cross_owner: false, members: draft.members })
  expect(remove[0]).toBe(update[0])
  expect(remove[1]?.body).toBeUndefined()
  for (const [, init] of writes) expect(new Headers(init?.headers).get("X-Workspace-ID")).toBe("ws")
  for (const [, init] of [update, remove]) expect(new Headers(init?.headers).get("If-Match")).toBe('"7"')
  expect(invalidate).toHaveBeenCalledTimes(3)
  expect(invalidate).toHaveBeenLastCalledWith({ queryKey: poolKeys.all("ws") })
})

it("blocks every management action after access is disabled", async () => {
  const { wrapper } = setup()
  const { result, rerender } = renderHook(({ enabled }) => useProviderPools("ws", enabled), { wrapper, initialProps: { enabled: true } })
  await waitFor(() => expect(result.current.loading).toBe(false))
  rerender({ enabled: false })
  vi.mocked(apiFetch).mockClear()
  const draft = { name: "Pool", provider: "OPENAI", mode: "api_key" as const, allow_cross_owner: false, members: [] }
  await expect(result.current.save(draft)).rejects.toThrow("not available")
  await expect(result.current.remove({ ...draft, id: "pool", revision: 1, member_count: 0 })).rejects.toThrow("not available")
  expect(() => result.current.reload()).toThrow("not available")
  expect(result.current).toMatchObject({ pools: undefined, accounts: undefined, loading: false, error: null })
  expect(apiFetch).not.toHaveBeenCalled()
})

it("stays idle without a workspace even when management is enabled", async () => {
  const { client, wrapper } = setup()
  client.setQueryData(poolKeys.list("", ""), { items: [{ id: "unscoped" }] })
  client.setQueryData(poolKeys.accounts(""), [{ id: "unscoped-account" }])
  const { result } = renderHook(() => useProviderPools("", true), { wrapper })
  expect(result.current).toMatchObject({ pools: undefined, accounts: undefined, loading: false, error: null })
  expect(apiFetch).not.toHaveBeenCalled()
  await expect(result.current.detail("pool")).rejects.toThrow("not available")
})
