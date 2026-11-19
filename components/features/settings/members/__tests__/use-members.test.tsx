import type { ReactNode } from "react"
import { act, cleanup, renderHook, waitFor } from "@testing-library/react"
import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"

import { useMembers } from "../use-members"
import { useMemberCapabilities } from "@/components/admin/member-capabilities"

const api = vi.hoisted(() => vi.fn())
vi.mock("@/lib/api-fetch", () => ({ apiFetch: (...args: unknown[]) => api(...args) }))

let client: QueryClient
function Wrapper({ children }: { children: ReactNode }) {
  return <QueryClientProvider client={client}>{children}</QueryClientProvider>
}
const member = (id: string) => ({ id, role: "MEMBER", created_at: "2026-10-13T00:00:00Z", user: { id, email: `${id}@example.com`, full_name: id, avatar_url: null } })
const response = (body: unknown, status = 200) => new Response(JSON.stringify(body), { status })

beforeEach(() => {
  api.mockReset()
  client = new QueryClient({ defaultOptions: { queries: { retry: false, gcTime: 0 } } })
})
afterEach(() => { cleanup(); client.clear() })

describe("workspace roster isolation", () => {
  it("refreshes capabilities alongside a roster that gained a member", async () => {
    let added = false
    api.mockImplementation(async (url: string) => url.includes("/members/capabilities")
      ? response({ members: added ? [{ user_id: "new-member", capabilities: ["chat", "issue.create"] }] : [] })
      : response(added ? [member("new-member")] : []))
    const { result } = renderHook(() => ({ roster: useMembers("first"), caps: useMemberCapabilities("first", true) }), { wrapper: Wrapper })
    await waitFor(() => expect(result.current.caps.data).toEqual({}))
    added = true
    act(() => { result.current.roster.refresh() })
    await waitFor(() => expect(result.current.roster.members[0]?.id).toBe("new-member"))
    await waitFor(() => expect(result.current.caps.data?.["new-member"]).toEqual(["chat", "issue.create"]))
  })

  it("hides the previous roster while the next workspace loads, including when its read fails", async () => {
    let finish!: (value: Response) => void
    api.mockResolvedValueOnce(response([member("first-member")]))
      .mockImplementationOnce(() => new Promise<Response>((resolve) => { finish = resolve }))
    const { result, rerender } = renderHook(({ ws }) => useMembers(ws), { wrapper: Wrapper, initialProps: { ws: "first" } })
    await waitFor(() => expect(result.current.members[0]?.id).toBe("first-member"))
    rerender({ ws: "second" })
    expect(result.current.members).toEqual([])
    expect(result.current.loading).toBe(true)
    await act(async () => { finish(response({}, 403)) })
    await waitFor(() => expect(result.current.error).toBe("Failed to load members"))
    expect(result.current.members).toEqual([])
    expect(result.current.loading).toBe(false)
  })

  it("keeps the current roster mounted during a refresh", async () => {
    let finish!: (value: Response) => void
    api.mockResolvedValueOnce(response([member("first-member")]))
      .mockImplementationOnce(() => new Promise<Response>((resolve) => { finish = resolve }))
    const { result } = renderHook(() => useMembers("first"), { wrapper: Wrapper })
    await waitFor(() => expect(result.current.members).toHaveLength(1))
    act(() => { result.current.refresh() })
    await waitFor(() => expect(api).toHaveBeenCalledTimes(2))
    expect(result.current.loading).toBe(false)
    expect(result.current.members[0]?.id).toBe("first-member")
    await act(async () => { finish(response([member("new-member")])) })
    await waitFor(() => expect(result.current.members[0]?.id).toBe("new-member"))
  })

  it("clears the roster when no workspace is selected", async () => {
    api.mockResolvedValue(response([member("first-member")]))
    const { result, rerender } = renderHook(({ ws }: { ws: string | null }) => useMembers(ws), { wrapper: Wrapper, initialProps: { ws: "first" } as { ws: string | null } })
    await waitFor(() => expect(result.current.members).toHaveLength(1))
    rerender({ ws: null })
    expect(result.current.members).toEqual([])
    expect(api).toHaveBeenCalledTimes(1)
  })
})

// Removing a member: the server answers 409 `page_ownership_transfer_required`
// when they own private artifacts nobody else owns. An OWNER's answer lists
// the artifacts and who may take each over; anyone else's carries the
// sentence only. Handing one over is POST …/page-ownership/{pageID}.
describe("removing a member", () => {
  const toast = vi.hoisted(() => ({ success: vi.fn(), error: vi.fn(), warning: vi.fn(), info: vi.fn() }))
  vi.mock("sonner", () => ({ toast }))
  const person = member("u-ada") as ReturnType<typeof member>
  const conflict = (extra: Record<string, unknown> = {}) => response({ code: "page_ownership_transfer_required", error: "cannot remove this member: private artifacts", ...extra }, 409)

  async function roster() {
    api.mockImplementation(async (_url: string, init?: RequestInit) => (init?.method ? conflict() : response([person])))
    const hook = renderHook(() => useMembers("ws1"), { wrapper: Wrapper })
    await waitFor(() => expect(hook.result.current.members).toHaveLength(1))
    return hook
  }

  it("hands an OWNER the artifacts and their eligible successors instead of a toast", async () => {
    const { result } = await roster()
    api.mockImplementation(async () => conflict({ pages: [
      { id: "p1", name: "Launch plan", type: "doc", owner_user_id: "u-ada", eligible_successors: [{ user_id: "u-bob", name: "Bob", email: "bob@x.io" }] },
      { id: "bad" , eligible_successors: "nope" },
      { name: "no id" },
    ] }))
    let outcome: Awaited<ReturnType<typeof result.current.tryRemove>> | undefined
    await act(async () => { outcome = await result.current.tryRemove(person) })
    expect(outcome).toEqual({ ok: false, handOver: {
      message: "cannot remove this member: private artifacts",
      pages: [
        { id: "p1", name: "Launch plan", type: "doc", successors: [{ userId: "u-bob", name: "Bob", email: "bob@x.io" }] },
        { id: "bad", name: "Untitled", type: "", successors: [] },
      ],
    } })
    expect(toast.error).not.toHaveBeenCalled()
    const [url, init] = api.mock.calls.at(-1)!
    expect(url).toBe("/api/v1/workspaces/ws1/members/u-ada?workspace_id=ws1")
    expect(init).toEqual({ method: "DELETE" })
  })

  it("gives an admin the server's sentence with no artifacts to choose from", async () => {
    const { result } = await roster()
    api.mockImplementation(async () => conflict({ error: "workspace OWNER must select a private artifact successor before removing this member" }))
    let outcome: Awaited<ReturnType<typeof result.current.tryRemove>> | undefined
    await act(async () => { outcome = await result.current.tryRemove(person) })
    expect(outcome).toEqual({ ok: false, handOver: { message: "workspace OWNER must select a private artifact successor before removing this member", pages: [] } })
  })

  it("still toasts every other refusal, and the plain remove says so for a transfer", async () => {
    const { result } = await roster()
    api.mockImplementation(async () => response({ error: "current workspace administrator and non-owner member required" }, 403))
    await act(async () => { expect((await result.current.tryRemove(person)).ok).toBe(false) })
    expect(toast.error).toHaveBeenCalledWith("current workspace administrator and non-owner member required", expect.anything())
    toast.error.mockClear()
    api.mockImplementation(async () => conflict())
    await act(async () => { expect(await result.current.remove(person)).toBe(false) })
    expect(toast.error).toHaveBeenCalledWith("cannot remove this member: private artifacts", expect.anything())
  })

  it("hands one artifact over with the contract the server expects", async () => {
    const { result } = await roster()
    api.mockImplementation(async () => response({ id: "p1" }))
    await act(async () => { expect(await result.current.handOverPage(person, "p 1", "u-bob")).toBeNull() })
    const [url, init] = api.mock.calls.at(-1)!
    expect(url).toBe("/api/v1/workspaces/ws1/page-ownership/p%201?workspace_id=ws1")
    expect(init.method).toBe("POST")
    // The expected owner is the departing USER, not the membership row.
    expect(JSON.parse(init.body)).toEqual({ expected_owner_user_id: "u-ada", successor_user_id: "u-bob" })
    api.mockImplementation(async () => response({ code: "owner_changed", error: "page ownership changed; refresh before transferring" }, 409))
    await act(async () => { expect(await result.current.handOverPage(person, "p1", "u-bob")).toBe("page ownership changed; refresh before transferring") })
  })
})
