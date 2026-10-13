import type { ReactNode } from "react"
import { act, cleanup, renderHook, waitFor } from "@testing-library/react"
import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"

import { useMembers } from "../use-members"

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
