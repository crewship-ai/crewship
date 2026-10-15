import React from "react"
import { act, renderHook, waitFor, cleanup } from "@testing-library/react"
import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import { afterEach, describe, expect, it, vi } from "vitest"
import page from "@/internal/api/testdata/chat-entity-refs/page.json"
import continuation from "@/internal/api/testdata/chat-entity-refs/continuation.json"
import empty from "@/internal/api/testdata/chat-entity-refs/empty.json"
import { chatEntityKeys, useChatEntityRefs } from "../use-chat-entity-refs"

const { fetch, events } = vi.hoisted(() => ({ fetch: vi.fn(), events: new Map<string, (e: { payload: Record<string, unknown> }) => void>() }))
vi.mock("@/lib/api-fetch", () => ({ apiFetch: fetch }))
vi.mock("@/hooks/use-realtime", () => ({
  useRealtimeChannelSafe: vi.fn(), useRealtimeStatusSafe: () => "connected",
  useRealtimeEventSafe: (type: string, callback: (e: { payload: Record<string, unknown> }) => void) => { events.set(type, callback) },
}))
const props = { workspaceId: "test-workspace-id", chatId: "chat-1", userId: "viewer", enabled: true }
const response = (data: unknown, status = 200) => new Response(JSON.stringify(data), { status })
function setup(initial = props) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false, gcTime: 0 } } })
  const wrapper = ({ children }: { children: React.ReactNode }) => <QueryClientProvider client={client}>{children}</QueryClientProvider>
  return { ...renderHook((p) => useChatEntityRefs(p), { wrapper, initialProps: initial }), client }
}
afterEach(() => { cleanup(); fetch.mockReset(); events.clear() })

describe("authorized chat entity snapshots", () => {
  it("loads real handler pages as one complete snapshot and keeps opaque cursors", async () => {
    fetch.mockResolvedValueOnce(response(page)).mockResolvedValueOnce(response(continuation))
    const { result } = setup()
    await waitFor(() => expect(result.current.state).toBe("ready"))
    expect(result.current.refs).toHaveLength(3)
    expect(fetch.mock.calls[1][0]).toContain("cursor=%3Copaque+cursor%3E")
    expect(result.current.live).toBe(true)
  })
  it("immediately hides old data during reset and removes protected detail cache", async () => {
    fetch.mockResolvedValueOnce(response({ ...page, has_more: false, snapshot_complete: true }))
    const { result, client } = setup()
    await waitFor(() => expect(result.current.refs).toHaveLength(2))
    client.setQueryData([...chatEntityKeys.detail(props.workspaceId, props.chatId, props.userId), "entity-1"], { title: "protected" })
    let complete!: (r: Response) => void
    fetch.mockImplementationOnce(() => new Promise<Response>((resolve) => { complete = resolve }))
    act(() => result.current.reconcile())
    await waitFor(() => expect(result.current.state).toBe("rechecking"))
    expect(result.current.refs).toEqual([])
    expect(client.getQueriesData({ queryKey: chatEntityKeys.detail(props.workspaceId, props.chatId, props.userId) })).toEqual([])
    await act(async () => complete(response(empty)))
    await waitFor(() => expect(result.current.state).toBe("ready"))
    expect(result.current.refs).toEqual([])
  })
  it("recovers an expired cursor and fails closed after revocation", async () => {
    fetch.mockResolvedValueOnce(response({ ...page, has_more: false, snapshot_complete: true }))
    const { result } = setup()
    await waitFor(() => expect(result.current.state).toBe("ready"))
    fetch.mockResolvedValueOnce(response({ error: "cursor_invalid" }, 400)).mockResolvedValueOnce(response(empty))
    act(() => result.current.reconcile())
    await waitFor(() => expect(fetch).toHaveBeenCalledTimes(3))
    expect(fetch.mock.calls[2][0]).not.toContain("cursor=")
    await waitFor(() => expect(result.current.state).toBe("ready"))
    fetch.mockResolvedValueOnce(response({}, 404))
    act(() => events.get("chat.entity_refs_changed")?.({ payload: { contract_version: 1 } }))
    await waitFor(() => expect(result.current.state).toBe("unavailable"))
    expect(result.current.refs).toEqual([])
    expect(result.current.live).toBe(false)
  })
  it("does not request cards for unsupported viewers or reuse another viewer's data", async () => {
    const { result, rerender } = setup({ ...props, enabled: false })
    expect(result.current.state).toBe("unavailable")
    expect(fetch).not.toHaveBeenCalled()
    fetch.mockResolvedValueOnce(response(empty))
    rerender({ ...props, userId: "other" })
    await waitFor(() => expect(result.current.state).toBe("ready"))
    expect(result.current.refs).toEqual([])
  })
  it("rejects mixed pages and unknown contracts instead of publishing partial cards", async () => {
    fetch.mockResolvedValueOnce(response(page)).mockResolvedValueOnce(response({ ...continuation, snapshot_id: "different" }))
    const { result } = setup()
    await waitFor(() => expect(fetch).toHaveBeenCalledTimes(2))
    await waitFor(() => expect(result.current.state).toBe("rechecking"))
    expect(result.current.refs).toEqual([])
    expect(result.current.live).toBe(false)
  })
})
