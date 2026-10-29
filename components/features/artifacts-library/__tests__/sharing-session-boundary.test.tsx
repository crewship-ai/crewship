import React from "react"
import { act, renderHook, waitFor } from "@testing-library/react"
import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import { beforeEach, describe, expect, it, vi } from "vitest"

const state = vi.hoisted(() => ({ human: "A", fetch: vi.fn() }))
vi.mock("@/hooks/use-auth", () => ({ useSessionSafe: () => ({ status: "authenticated", data: { user: { id: state.human } } }) }))
vi.mock("@/lib/feature-flags", () => ({ artifactsSharingMock: () => false }))
vi.mock("@/lib/api-fetch", () => ({ apiFetch: state.fetch }))

import { SESSION_CHANGED, useSharingMutation, type SharingApi } from "../sharing/sharing-api"
import type { LibraryItem } from "../library-items"

const item = { slug: "private", name: "Private", folder: null, pagesVersion: 3 } as unknown as LibraryItem
const held = { grants: [{ subject_type: "user", subject_id: "reader", subject: "reader@example.test", level: "write", live: true }] }
const json = (body: unknown) => new Response(JSON.stringify(body), { status: 200 })

/*
 * An ACL change is several requests. Once its session is gone (another human,
 * the same human again after someone else, or an unmounted caller), no further
 * request of it may be sent: a check after the whole operation cannot undo writes.
 */
function harness(run: (api: SharingApi) => Promise<unknown>, first: unknown = held) {
  let release!: (response: Response) => void
  const firstRead = new Promise<Response>(resolve => { release = resolve })
  const sent: { human: string; method: string }[] = []
  let reads = 0
  state.fetch.mockImplementation((_url: string, init?: RequestInit) => {
    const method = init?.method ?? "GET"
    if (method === "GET" && reads++ === 0) return firstRead
    sent.push({ human: state.human, method })
    return Promise.resolve(json(method === "GET" ? held : { acl: [], acl_version: 1 }))
  })
  const client = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } })
  const wrapper = ({ children }: { children: React.ReactNode }) => <QueryClientProvider client={client}>{children}</QueryClientProvider>
  const hook = renderHook(() => useSharingMutation("ws", (api, _input: null) => run(api)), { wrapper })
  return { hook, sent, release: () => act(async () => { release(json(first)) }) }
}

const operations: [string, (api: SharingApi) => Promise<unknown>, unknown][] = [
  ["share", api => api.share(item, { subjectType: "user", subjectId: "reader", label: "Reader", level: "read" }), held],
  ["revoke", api => api.revoke(item, "user", "reader"), held],
  ["folder save", api => api.saveFolderSettings("lab", { defaultAudience: null, entries: [{ subjectType: "user", subjectId: "reader", label: "Reader", level: "read", via: "folder" }] }), { acl: [], acl_version: 1 }],
  ["bulk move preview", api => api.bulk({ action: "move", items: [item, { ...item, slug: "second" } as LibraryItem], folder: { slug: "lab", name: "Lab" }, dryRun: true }), { slug: "lab", acl_version: 1 }],
]

beforeEach(() => { state.human = "A"; state.fetch.mockReset() })

describe("sharing writes stay with the session that started them", () => {
  it.each(operations)("%s sends nothing as another human", async (_name, run, first) => {
    const { hook, sent, release } = harness(run, first)
    act(() => hook.result.current.mutate(null))
    await waitFor(() => expect(state.fetch).toHaveBeenCalledTimes(1))
    state.human = "B"; hook.rerender()
    await release()
    await waitFor(() => expect(hook.result.current.isError).toBe(true))
    expect(hook.result.current.error?.message).toBe(SESSION_CHANGED)
    expect(sent).toEqual([])
  })

  it.each(operations)("%s sends nothing after A → B → A", async (_name, run, first) => {
    const { hook, sent, release } = harness(run, first)
    act(() => hook.result.current.mutate(null))
    await waitFor(() => expect(state.fetch).toHaveBeenCalledTimes(1))
    state.human = "B"; hook.rerender()
    state.human = "A"; hook.rerender()
    await release()
    await waitFor(() => expect(hook.result.current.isError).toBe(true))
    expect(sent).toEqual([])
  })

  it.each(operations)("%s sends nothing after its caller unmounts", async (_name, run, first) => {
    const { hook, sent, release } = harness(run, first)
    act(() => hook.result.current.mutate(null))
    await waitFor(() => expect(state.fetch).toHaveBeenCalledTimes(1))
    hook.unmount()
    await release()
    await new Promise(resolve => setTimeout(resolve, 20))
    expect(sent).toEqual([])
  })

  it("a share in one session completes all its requests", async () => {
    const { hook, sent, release } = harness(operations[0][1])
    act(() => hook.result.current.mutate(null))
    await waitFor(() => expect(state.fetch).toHaveBeenCalledTimes(1))
    await release()
    await waitFor(() => expect(hook.result.current.isSuccess).toBe(true))
    expect(sent).toEqual([{ human: "A", method: "PUT" }, { human: "A", method: "DELETE" }])
  })
})
