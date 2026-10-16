import React from "react"
import { act, cleanup, renderHook, waitFor } from "@testing-library/react"
import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import { afterEach, expect, it, vi } from "vitest"
import card from "@/internal/api/testdata/chat-e2/proposal-draft.json"
import accepted from "@/internal/api/testdata/chat-e2/proposal-accepted.json"
import { ProposalRefSchema } from "@/lib/chat-e2-cards"
import { proposalRecoveryKey } from "@/hooks/use-proposal-draft"
import { useChatE2Cards } from "../use-chat-e2-cards"

const { fetch, realtime } = vi.hoisted(() => ({ fetch: vi.fn(), realtime: { status: "connected" } }))
vi.mock("@/lib/api-fetch", () => ({ apiFetch: fetch }))
vi.mock("@/hooks/use-realtime", () => ({ useRealtimeChannelSafe: vi.fn(), useRealtimeEventSafe: vi.fn(), useRealtimeStatusSafe: () => realtime.status }))
const props = { kind: "proposal" as const, workspaceId: card.entity.workspace_id, chatId: "chat", userId: "viewer", enabled: true }
const page = (refs: unknown[] = [card]) => ({ contract_version: 1, reset: true, snapshot_id: "snapshot", refs, has_more: false, snapshot_complete: true, next_cursor: "next" })
const response = (value: unknown, status = 200) => new Response(JSON.stringify(value), { status })
function setup() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  const wrapper = ({ children }: { children: React.ReactNode }) => <QueryClientProvider client={client}>{children}</QueryClientProvider>
  return { ...renderHook(value => useChatE2Cards(value), { wrapper, initialProps: props }), client }
}
afterEach(() => { cleanup(); fetch.mockReset(); sessionStorage.clear(); realtime.status = "connected" })

it("reconciles lost hints when the WebSocket reconnects", async () => {
  fetch.mockResolvedValueOnce(response(page()))
  const { result, rerender } = setup()
  await waitFor(() => expect(result.current.state).toBe("ready"))
  realtime.status = "disconnected"
  rerender({ ...props })
  fetch.mockResolvedValueOnce(response(page([accepted])))
  realtime.status = "connected"
  rerender({ ...props })
  await waitFor(() => expect(result.current.refs[0]).toMatchObject({ proposal: { status: "accepted" } }))
  expect(fetch).toHaveBeenCalledTimes(2)
})

it("retains mounted proposal and recovery during a server outage, but purges denial and never revives it on 500", async () => {
  const key = proposalRecoveryKey({ workspaceId: props.workspaceId, viewerId: props.userId, proposalId: card.entity.id, surface: "card" }, "comment")
  sessionStorage.setItem(key, "private draft")
  fetch.mockResolvedValueOnce(response(page()))
  const { result } = setup()
  await waitFor(() => expect(result.current.state).toBe("ready"))
  fetch.mockResolvedValueOnce(response({}, 500))
  act(() => result.current.reconcile())
  await waitFor(() => expect(result.current.state).toBe("rechecking"))
  await waitFor(() => expect(fetch).toHaveBeenCalledTimes(2))
  expect(result.current.refs).toHaveLength(1)
  expect(sessionStorage.getItem(key)).toBe("private draft")
  fetch.mockResolvedValueOnce(response({}, 403))
  act(() => result.current.reconcile())
  await waitFor(() => expect(result.current.state).toBe("unavailable"))
  expect(sessionStorage.getItem(key)).toBeNull()
  fetch.mockResolvedValueOnce(response({}, 500))
  act(() => result.current.reconcile())
  await waitFor(() => expect(fetch).toHaveBeenCalledTimes(4))
  expect(result.current.refs).toEqual([])
  expect(result.current.state).toBe("unavailable")
  await act(async () => result.current.applySnapshot(ProposalRefSchema.parse(card)))
  expect(result.current.refs).toEqual([])
})

it("does not publish a partial replacement snapshot or accept a foreign workspace", async () => {
  fetch.mockResolvedValueOnce(response({ ...page(), has_more: true, snapshot_complete: false }))
    .mockResolvedValueOnce(response({}, 500))
  const { result, client } = setup()
  await waitFor(() => expect(client.getQueryState(["chat-e2-cards", "proposal", props.workspaceId, props.chatId, props.userId])?.status).toBe("error"))
  expect(result.current.refs).toEqual([])
  fetch.mockResolvedValueOnce(response(page([{ ...card, entity: { ...card.entity, workspace_id: "foreign" } }])))
  act(() => result.current.reconcile())
  await waitFor(() => expect(fetch).toHaveBeenCalledTimes(3))
  await waitFor(() => expect(client.getQueryState(["chat-e2-cards", "proposal", props.workspaceId, props.chatId, props.userId])?.fetchStatus).toBe("idle"))
  expect(result.current.refs).toEqual([])
})

it("updates the current card from a committed mutation but rejects a late callback after identity change", async () => {
  fetch.mockResolvedValueOnce(response(page()))
  const { result, rerender, client } = setup()
  await waitFor(() => expect(result.current.state).toBe("ready"))
  fetch.mockImplementationOnce(() => new Promise(() => {}))
  await act(async () => result.current.applySnapshot(ProposalRefSchema.parse(accepted)))
  await waitFor(() => expect(result.current.refs[0]).toMatchObject({ proposal: { status: "accepted" } }))
  const lateCallback = result.current.applySnapshot
  fetch.mockResolvedValueOnce(response(page([])))
  rerender({ ...props, userId: "other" })
  await waitFor(() => expect(result.current.state).toBe("ready"))
  await act(async () => lateCallback(ProposalRefSchema.parse(card)))
  expect(result.current.refs).toEqual([])
  expect(client.getQueryData(["chat-e2-cards", "proposal", props.workspaceId, props.chatId, props.userId])).toBeUndefined()
})
