/**
 * The delivery ledger's data layer (§5, §9).
 *
 * Two properties are load-bearing. The cache is keyed by workspace, so one
 * tenant's ledger can never be served from another's entry — a delivery id is
 * a receipt, not a capability, and the fence has to hold on the client too.
 * And an IGNORED delivery is a row, not an absence: before the ledger, "we
 * never received it" and "we received it and a filter decided not to act on
 * it" were the same empty screen.
 */
import { describe, it, expect, vi, beforeEach, afterEach } from "vitest"
import React from "react"
import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import { renderHook, waitFor, act } from "@testing-library/react"

const h = vi.hoisted(() => ({
  apiFetch: vi.fn(),
  subs: new Map<string, Array<(event: unknown) => void>>(),
}))

vi.mock("@/lib/api-fetch", () => ({ apiFetch: h.apiFetch }))
vi.mock("@/hooks/use-realtime", () => ({
  useRealtimeEventSafe: (type: string, cb: (event: unknown) => void) => { h.subs.set(type, [cb]) },
}))

import {
  deliveryQueryParams,
  useWebhookDeliveries,
  useWebhookDelivery,
  webhookDeliveryKeys,
  type WebhookDelivery,
} from "@/hooks/use-webhook-deliveries"

function delivery(over: Partial<WebhookDelivery> = {}): WebhookDelivery {
  return {
    id: "cdlv0000000000000001",
    workspace_id: "ws-1",
    endpoint_id: "e-1",
    endpoint_kind: "routine",
    profile: "github",
    source_delivery_id: "gh-1",
    event_type: "push",
    event_action: "",
    signing_key_id: "key-1",
    body_sha256: "b".repeat(64),
    body_bytes: 1024,
    filter_decision: "accepted",
    filter_reason: "",
    target_revision: "rev-7",
    work_id: "cwork0000000000000001",
    received_at: "2026-09-10T09:00:00.000000000Z",
    dedup_expires_at: "2026-10-10T09:00:00.000000000Z",
    raw_body_available: true,
    raw_body_expires_at: "2026-09-17T09:00:00.000000000Z",
    ...over,
  }
}

function okJSON(body: unknown): Response {
  return { ok: true, status: 200, json: async () => body } as unknown as Response
}

function newQueryClient(): QueryClient {
  return new QueryClient({ defaultOptions: { queries: { retry: false, gcTime: 0 } } })
}

function wrapper(qc: QueryClient) {
  return function Wrapper({ children }: { children: React.ReactNode }) {
    return <QueryClientProvider client={qc}>{children}</QueryClientProvider>
  }
}

describe("delivery query keys", () => {
  it("uses [resource, workspaceId, params]", () => {
    expect(webhookDeliveryKeys.all("ws-1")).toEqual(["webhook-deliveries", "ws-1"])
    expect(webhookDeliveryKeys.list("ws-1")).toEqual(["webhook-deliveries", "ws-1", { view: "list" }])
    expect(webhookDeliveryKeys.detail("ws-1", "d-1")).toEqual(["webhook-deliveries", "ws-1", { id: "d-1" }])
  })

  it("keys two workspaces apart", () => {
    expect(webhookDeliveryKeys.list("ws-1")).not.toEqual(webhookDeliveryKeys.list("ws-2"))
    expect(webhookDeliveryKeys.detail("ws-1", "d-1")).not.toEqual(webhookDeliveryKeys.detail("ws-2", "d-1"))
  })

  it("omits absent filters so {} and {decision:null} share one entry", () => {
    expect(deliveryQueryParams({ decision: null, endpointId: null })).toEqual({})
    expect(webhookDeliveryKeys.list("ws-1", {})).toEqual(webhookDeliveryKeys.list("ws-1", { decision: null }))
  })
})

describe("useWebhookDeliveries", () => {
  let qc: QueryClient
  beforeEach(() => { h.apiFetch.mockReset(); h.subs.clear(); qc = newQueryClient() })
  afterEach(() => qc.clear())

  it("fires nothing without a workspace", () => {
    renderHook(() => useWebhookDeliveries(null), { wrapper: wrapper(qc) })
    expect(h.apiFetch).not.toHaveBeenCalled()
  })

  it("does not share a cache entry between workspaces", async () => {
    h.apiFetch.mockImplementation((url: string) =>
      Promise.resolve(okJSON({
        items: [delivery({ id: url.includes("ws-2") ? "d-two" : "d-one" })],
        next_cursor: null,
      })),
    )
    const one = renderHook(() => useWebhookDeliveries("ws-1"), { wrapper: wrapper(qc) })
    const two = renderHook(() => useWebhookDeliveries("ws-2"), { wrapper: wrapper(qc) })
    await waitFor(() => expect(one.result.current.deliveries).toHaveLength(1))
    await waitFor(() => expect(two.result.current.deliveries).toHaveLength(1))
    expect(one.result.current.deliveries[0].id).toBe("d-one")
    expect(two.result.current.deliveries[0].id).toBe("d-two")
  })

  it("keeps an ignored delivery as a row, with its decision and reason", async () => {
    h.apiFetch.mockResolvedValue(okJSON({
      items: [delivery({ filter_decision: "ignored", filter_reason: "ping event", work_id: null })],
      next_cursor: null,
    }))
    const { result } = renderHook(() => useWebhookDeliveries("ws-1", { decision: "ignored" }), { wrapper: wrapper(qc) })
    await waitFor(() => expect(result.current.deliveries).toHaveLength(1))
    expect(String(h.apiFetch.mock.calls[0][0])).toBe("/api/v1/workspaces/ws-1/webhook-deliveries?decision=ignored")
    expect(result.current.deliveries[0].work_id).toBeNull()
    expect(result.current.deliveries[0].filter_reason).toBe("ping event")
  })

  it("re-reads the authoritative snapshot after a reconnect", async () => {
    h.apiFetch.mockResolvedValue(okJSON({ items: [delivery()], next_cursor: null }))
    const { result } = renderHook(() => useWebhookDeliveries("ws-1"), { wrapper: wrapper(qc) })
    await waitFor(() => expect(result.current.deliveries).toHaveLength(1))
    await act(async () => { h.subs.get("realtime.reconnected")?.forEach((cb) => cb({})) })
    await waitFor(() => expect(h.apiFetch).toHaveBeenCalledTimes(2))
  })
})

describe("useWebhookDelivery", () => {
  let qc: QueryClient
  beforeEach(() => { h.apiFetch.mockReset(); h.subs.clear(); qc = newQueryClient() })
  afterEach(() => qc.clear())

  it("tells 'the ledger does not have it' apart from 'we could not read it'", async () => {
    h.apiFetch.mockResolvedValue({
      ok: false, status: 404, json: async () => ({ error: "Webhook delivery not found" }),
    } as unknown as Response)
    const { result } = renderHook(() => useWebhookDelivery("ws-1", "d-gone"), { wrapper: wrapper(qc) })
    await waitFor(() => expect(result.current.notFound).toBe(true))
    expect(result.current.error).toBeNull()
    expect(result.current.delivery).toBeNull()
  })

  it("reports a dropped payload, which is what decides whether replay is offered", async () => {
    h.apiFetch.mockResolvedValue(okJSON(delivery({ raw_body_available: false, raw_body_expires_at: "2026-09-01T09:00:00Z" })))
    const { result } = renderHook(() => useWebhookDelivery("ws-1", "cdlv0000000000000001"), { wrapper: wrapper(qc) })
    await waitFor(() => expect(result.current.delivery).toBeTruthy())
    expect(result.current.delivery?.raw_body_available).toBe(false)
  })
})
