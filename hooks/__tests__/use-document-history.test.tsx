import React from "react"
import { act, cleanup, renderHook, waitFor } from "@testing-library/react"
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"
import { apiFetch } from "@/lib/api-fetch"
import { useDocumentHistory, type DocumentHistoryScope } from "../use-document-history"

const events = vi.hoisted(() => new Map<string, Set<(event: unknown) => void>>())
vi.mock("@/lib/api-fetch", () => ({ apiFetch: vi.fn() }))
vi.mock("@/hooks/use-realtime", () => ({ useRealtimeEventSafe: (name: string, callback: (event: unknown) => void) => {
  React.useEffect(() => { const set = events.get(name) ?? new Set(); set.add(callback); events.set(name, set); return () => { set.delete(callback) } }, [name, callback])
} }))
const fetchMock = vi.mocked(apiFetch)
const scope: DocumentHistoryScope = { enabled: true, workspaceId: "ws", humanId: "human", pageId: "page", activationKey: "open-1" }
const pin = { id: "pin", rev: 2, schema: 3, actor: "user:human", created_at: "2026-10-18T00:00:00Z" }
const content = { type: "doc", content: [{ type: "paragraph", attrs: { id: "text" }, content: [{ type: "text", text: "CONFIRMED" }] }] }
const snapshot = { page_id: "page", id: "pin", rev: 2, schema: 3, content }
const response = (body: unknown, status = 200) => new Response(JSON.stringify(body), { status })
const pending = () => { let resolve!: (value: Response) => void; const promise = new Promise<Response>(done => { resolve = done }); return { promise, resolve } }
const setup = (input = scope) => renderHook((props: DocumentHistoryScope) => useDocumentHistory(props), { initialProps: input })
const ready = async (result: ReturnType<typeof setup>["result"]) => waitFor(() => expect(result.current.list.status).toBe("ready"))
const selected = async (result: ReturnType<typeof setup>["result"]) => { act(() => result.current.onSelect("pin")); await waitFor(() => expect(result.current.snapshot.status).toBe("ready")) }
beforeEach(() => { fetchMock.mockReset(); events.clear() })
afterEach(cleanup)

describe("human Document history controller", () => {
  it.each([undefined, false])("requires explicit enable (%s)", enabled => {
    const { result } = setup({ ...scope, enabled }); expect(result.current.list.status).toBe("idle"); expect(fetchMock).not.toHaveBeenCalled()
  })
  it.each(["workspaceId", "humanId", "pageId", "activationKey"] as const)("requires %s", field => {
    setup({ ...scope, [field]: "" }); expect(fetchMock).not.toHaveBeenCalled()
  })
  it("reads immutable identity and validates content without any mutation", async () => {
    fetchMock.mockResolvedValueOnce(response({ checkpoints: [pin] })).mockResolvedValueOnce(response(snapshot))
    const { result } = setup(); await ready(result); await selected(result)
    expect(result.current.snapshot).toEqual({ status: "ready", scopeKey: result.current.scopeKey, data: snapshot })
    expect(fetchMock.mock.calls.map(([url]) => url)).toEqual(["/api/v1/pages/%40page/document/checkpoints?workspace_id=ws", "/api/v1/pages/%40page/document/checkpoints/pin?workspace_id=ws"])
    expect(fetchMock.mock.calls.every(([, options]) => options?.signal instanceof AbortSignal && !options.method && !options.body)).toBe(true)
  })
  it.each(["workspaceId", "humanId", "pageId", "activationKey"] as const)("masks synchronously and aborts on %s change", async field => {
    const late = pending(); fetchMock.mockResolvedValueOnce(response({ checkpoints: [pin] })).mockReturnValueOnce(late.promise).mockResolvedValueOnce(response({ checkpoints: [] }))
    const { result, rerender } = setup(); await ready(result); act(() => result.current.onSelect("pin"))
    const signal = fetchMock.mock.calls[1][1]!.signal!; rerender({ ...scope, [field]: "other" })
    expect(result.current.list.status).not.toBe("ready"); expect(result.current.snapshot.status).not.toBe("ready"); expect(signal.aborted).toBe(true)
    await ready(result); await act(async () => late.resolve(response(snapshot)))
    expect(result.current.snapshot.status).not.toBe("ready")
  })
  it("masks an already rendered body when closed and reopened, including same identifiers", async () => {
    fetchMock.mockResolvedValueOnce(response({ checkpoints: [pin] })).mockResolvedValueOnce(response(snapshot)).mockReturnValueOnce(pending().promise)
    const { result, rerender } = setup(); await ready(result); await selected(result)
    rerender({ ...scope, enabled: false }); expect(result.current.list.status).toBe("idle"); expect(result.current.snapshot.status).toBe("idle")
    rerender(scope); expect(result.current.list.status).not.toBe("ready"); expect(result.current.snapshot.status).not.toBe("ready")
  })
  it("hides the previous selection immediately and rejects stale selected delivery", async () => {
    const late = pending(); fetchMock.mockResolvedValueOnce(response({ checkpoints: [pin, { ...pin, id: "second" }] })).mockReturnValueOnce(late.promise).mockResolvedValueOnce(response({ ...snapshot, id: "second" }))
    const { result } = setup(); await ready(result); act(() => result.current.onSelect("pin")); const signal = fetchMock.mock.calls[1][1]!.signal!
    act(() => result.current.onSelect("second")); expect(result.current.snapshot.status).not.toBe("ready"); expect(signal.aborted).toBe(true)
    await waitFor(() => expect(result.current.snapshot.status).toBe("ready")); await act(async () => late.resolve(response(snapshot)))
    expect(result.current.snapshot.status === "ready" && result.current.snapshot.data.id).toBe("second")
  })
  it.each([1, 99])("never requests unsupported listed schema %s", async schema => {
    fetchMock.mockResolvedValueOnce(response({ checkpoints: [{ ...pin, schema }] })); const { result } = setup(); await ready(result)
    act(() => result.current.onSelect("pin")); expect(result.current.snapshot.status).toBe("unavailable"); expect(fetchMock).toHaveBeenCalledTimes(1)
  })
  it.each([
    { checkpoints: null }, { checkpoints: [pin, pin] }, { checkpoints: [{ ...pin, id: "../pin" }] },
    { checkpoints: [{ ...pin, rev: Number.MAX_SAFE_INTEGER + 1 }] }, { checkpoints: [{ ...pin, rev: -1 }] },
    { checkpoints: Array.from({ length: 1001 }, (_, index) => ({ ...pin, id: `p${index}` })) },
    { checkpoints: [{ ...pin, snapshot: "SECRET" }] },
  ])("fails closed for malformed list %#", async body => {
    fetchMock.mockResolvedValueOnce(response(body)); const { result } = setup(); await waitFor(() => expect(result.current.list.status).toBe("error")); expect(result.current.snapshot.status).not.toBe("ready")
  })
  it.each([
    { ...snapshot, page_id: "other" }, { ...snapshot, id: "other" }, { ...snapshot, rev: 3 }, { ...snapshot, schema: 2 },
    { ...snapshot, schema: 99 }, { ...snapshot, content: content.content[0] }, { ...snapshot, epoch: "SECRET" }, { ...snapshot, content: { ...content, html: "SECRET" } },
    { ...snapshot, content: { type: "doc", content: [{ type: "image", attrs: { src: "https://example.invalid/" } }] } },
  ])("clears list and body for mismatched or unclosed selected response %#", async body => {
    fetchMock.mockResolvedValueOnce(response({ checkpoints: [pin] })).mockResolvedValueOnce(response(body))
    const { result } = setup(); await ready(result); act(() => result.current.onSelect("pin"))
    await waitFor(() => expect(result.current.snapshot.status).toBe("error")); expect(result.current.list.status).toBe("error")
  })
  it.each([401, 403, 404, 503, 500])("clears old data on failed refresh %s", async status => {
    fetchMock.mockResolvedValueOnce(response({ checkpoints: [pin] })).mockResolvedValueOnce(response(snapshot)).mockResolvedValueOnce(response({ error: "SECRET" }, status))
    const { result } = setup(); await ready(result); await selected(result); act(() => result.current.refresh())
    expect(result.current.snapshot.status).not.toBe("ready")
    await waitFor(() => expect(result.current.list.status).toBe(status === 503 ? "unavailable" : status === 500 ? "error" : "denied"))
    expect(result.current.snapshot.status).not.toBe("ready")
  })
  it.each(["page.updated", "realtime.reconnected"])("refreshes on %s and cleans subscriptions", async type => {
    fetchMock.mockResolvedValueOnce(response({ checkpoints: [pin] })).mockResolvedValueOnce(response({ checkpoints: [] }))
    const { result, unmount } = setup(); await ready(result)
    act(() => events.get(type)?.forEach(callback => callback({ type, payload: { page_id: "other", workspace_id: "other" }, channel: "workspace:other" })))
    expect(fetchMock).toHaveBeenCalledTimes(1)
    act(() => events.get(type)?.forEach(callback => callback({ type, payload: { page_id: "page", workspace_id: "ws" }, channel: "workspace:ws" })))
    await waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(2))
    await ready(result); unmount(); expect([...events.values()].every(set => set.size === 0)).toBe(true)
  })

  it("accepts schema2 only through the actual closed model", async () => {
    fetchMock.mockResolvedValueOnce(response({ checkpoints: [{ ...pin, schema: 2 }] })).mockResolvedValueOnce(response({ ...snapshot, schema: 2 }))
    const { result } = setup(); await ready(result); await selected(result)
    expect(result.current.snapshot.status === "ready" && result.current.snapshot.data.schema).toBe(2)
  })
  it("never reads a selection absent from the current admitted list", async () => {
    fetchMock.mockResolvedValueOnce(response({ checkpoints: [pin] })); const { result } = setup(); await ready(result)
    act(() => result.current.onSelect("other")); expect(result.current.selectedId).toBeUndefined(); expect(fetchMock).toHaveBeenCalledTimes(1)
  })
  it.each([401, 403, 404, 503])("clears list metadata on selected HTTP failure %s", async status => {
    fetchMock.mockResolvedValueOnce(response({ checkpoints: [pin] })).mockResolvedValueOnce(response({}, status))
    const { result } = setup(); await ready(result); act(() => result.current.onSelect("pin"))
    const expected = status === 503 ? "unavailable" : "denied"
    await waitFor(() => expect(result.current.snapshot.status).toBe(expected)); expect(result.current.list.status).toBe(expected)
  })
  it("refreshes an opaque private update only on the current workspace channel", async () => {
    fetchMock.mockResolvedValueOnce(response({ checkpoints: [pin] })).mockResolvedValueOnce(response({ checkpoints: [] }))
    const { result } = setup(); await ready(result)
    act(() => events.get("page.updated")?.forEach(callback => callback({ type: "page.updated", payload: {}, channel: "workspace:ws" })))
    expect(result.current.list.status).not.toBe("ready"); await ready(result); expect(fetchMock).toHaveBeenCalledTimes(2)
  })

  it("masks protected results in the first render before scope-change effects", async () => {
    fetchMock.mockResolvedValueOnce(response({ checkpoints: [pin] })).mockResolvedValueOnce(response(snapshot)).mockReturnValueOnce(pending().promise)
    const renders: { human: string; list: string; snapshot: string }[] = []
    const { result, rerender } = renderHook((input: DocumentHistoryScope) => {
      const value = useDocumentHistory(input)
      renders.push({ human: input.humanId, list: value.list.status, snapshot: value.snapshot.status })
      return value
    }, { initialProps: scope })
    await ready(result); await selected(result); rerender({ ...scope, humanId: "new-human" })
    const first = renders.find(value => value.human === "new-human")!
    expect(first.list).not.toBe("ready"); expect(first.snapshot).not.toBe("ready")
  })
  it("rejects a late original list after an identical scope is reopened", async () => {
    const late = pending(); fetchMock.mockReturnValueOnce(late.promise).mockResolvedValueOnce(response({ checkpoints: [] }))
    const { result, rerender } = setup(); const signal = fetchMock.mock.calls[0][1]!.signal!
    rerender({ ...scope, enabled: false }); rerender(scope); await ready(result)
    await act(async () => late.resolve(response({ checkpoints: [pin] })))
    expect(signal.aborted).toBe(true)
    expect(result.current.list.status === "ready" && result.current.list.data.checkpoints).toEqual([])
  })
  it("aborts pending work on unmount and ignores delivery even when transport ignores abort", async () => {
    const late = pending(); fetchMock.mockReturnValueOnce(late.promise); const { unmount } = setup(); const signal = fetchMock.mock.calls[0][1]!.signal!
    unmount(); expect(signal.aborted).toBe(true); await act(async () => late.resolve(response({ checkpoints: [pin] })))
  })
})
