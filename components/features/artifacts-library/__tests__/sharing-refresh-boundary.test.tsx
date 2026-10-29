import React from "react"
import { act, renderHook, waitFor } from "@testing-library/react"
import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import { afterEach, beforeEach, expect, it, vi } from "vitest"

const state = vi.hoisted(() => ({ human: "A" }))
vi.mock("@/hooks/use-auth", () => ({ useSessionSafe: () => ({ status: "authenticated", data: { user: { id: state.human } } }) }))
vi.mock("@/lib/feature-flags", () => ({ artifactsSharingMock: () => false }))

import { useSharingMutation, type ShareInput } from "../sharing/sharing-api"
import type { LibraryItem } from "../library-items"

/*
 * The real apiFetch retries a write once after refreshing an expired token.
 * A sharing write retired while that refresh is pending (its dialog closed,
 * another item or another person) must not be sent again afterwards.
 */
const item = { slug: "private", name: "Private", folder: null } as unknown as LibraryItem
const input: ShareInput = { subjectType: "user", subjectId: "reader", label: "Reader", level: "read" }
const json = (body: unknown, status = 200) => new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } })

let releaseRefresh: () => void
let puts: number
beforeEach(() => {
  state.human = "A"; puts = 0
  const refreshed = new Promise<Response>(resolve => { releaseRefresh = () => resolve(json({ ok: true })) })
  vi.stubGlobal("fetch", vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
    // Like the browser: an aborted request never goes out.
    if (init?.signal?.aborted) return Promise.reject(new DOMException("aborted", "AbortError"))
    const url = String(input)
    if (url.includes("/api/auth/token/refresh")) return refreshed
    if (init?.method === "PUT") { puts++; return Promise.resolve(puts === 1 ? json({ error: "session_expired" }, 401) : json({})) }
    return Promise.resolve(json({ grants: [] }))
  }))
})
afterEach(() => { vi.unstubAllGlobals() })

function mount(scope: string) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } })
  const wrapper = ({ children }: { children: React.ReactNode }) => <QueryClientProvider client={client}>{children}</QueryClientProvider>
  return renderHook(({ scope: current }) => useSharingMutation("ws", (api, body: ShareInput) => api.share(item, body), current), { wrapper, initialProps: { scope } })
}

it.each([
  ["its caller unmounts", (hook: ReturnType<typeof mount>) => hook.unmount()],
  ["another item is shown", (hook: ReturnType<typeof mount>) => hook.rerender({ scope: "other" })],
  ["another person signs in", (hook: ReturnType<typeof mount>) => { state.human = "B"; hook.rerender({ scope: "private" }) }],
])("a write waiting on a token refresh is not sent again once %s", async (_name, retire) => {
  const hook = mount("private")
  act(() => hook.result.current.mutate(input))
  await waitFor(() => expect(puts).toBe(1))
  await waitFor(() => expect(vi.mocked(fetch).mock.calls.some(([url]) => String(url).includes("/api/auth/token/refresh"))).toBe(true))
  retire(hook)
  await act(async () => { releaseRefresh(); await new Promise(resolve => setTimeout(resolve, 20)) })
  expect(puts).toBe(1)
})

it("the same write is retried after the refresh while it is still current", async () => {
  const hook = mount("private")
  act(() => hook.result.current.mutate(input))
  await waitFor(() => expect(puts).toBe(1))
  await act(async () => { releaseRefresh() })
  await waitFor(() => expect(hook.result.current.isSuccess).toBe(true))
  expect(puts).toBe(2)
})
