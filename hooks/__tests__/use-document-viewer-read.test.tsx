import { act, cleanup, renderHook, waitFor } from "@testing-library/react"
import { afterEach, beforeEach, expect, it, vi } from "vitest"
import { apiFetch } from "@/lib/api-fetch"
import { useDocumentViewerRead, type DocumentViewerReadInput } from "../use-document-viewer-read"

const events = vi.hoisted(() => new Map<string, () => void>())
vi.mock("@/lib/api-fetch", () => ({ apiFetch: vi.fn() }))
vi.mock("@/hooks/use-realtime", () => ({ useRealtimeEventSafe: (event: string, callback: () => void) => events.set(event, callback) }))
const body = { type: "doc", content: [{ type: "paragraph", attrs: { id: "p1" }, content: [{ type: "text", text: "Current saved body" }] }] }
const response = (value: unknown, status = 200) => new Response(JSON.stringify(value), { status })
const metadata = (id = "page-1") => ({ id, name: "Current authorized title", has_document: true })
const saved = (page_id = "page-1") => ({ page_id, schema: 3, rev: 8, content: body })
const input = (): DocumentViewerReadInput => ({ workspaceId: "ws /&", chatId: "chat-1", currentUserId: "human-1", activationKey: "open-auth-1", enabled: true, target: { workspaceId: "ws /&", pageId: "page-1", type: "document", mode: "latest" } })
function deferred() {
  let resolve!: (value: Response) => void
  const promise = new Promise<Response>(done => { resolve = done })
  return { promise, resolve }
}
beforeEach(() => {
  events.clear(); vi.mocked(apiFetch).mockReset()
  vi.mocked(apiFetch).mockImplementation(async url => response(String(url).includes("/document?") ? saved() : metadata()))
})
afterEach(cleanup)

it.each([2,3] as const)("publishes schema %s title and latest body only after both no-store immutable-ID reads", async schema => {
  const pending = deferred()
  vi.mocked(apiFetch).mockResolvedValueOnce(response(metadata())).mockReturnValueOnce(pending.promise)
  const { result } = renderHook(() => useDocumentViewerRead(input()))
  await waitFor(() => expect(apiFetch).toHaveBeenCalledTimes(2))
  expect(result.current.read).toEqual({ status: "loading" })
  expect(vi.mocked(apiFetch).mock.calls.map(call => call[0])).toEqual([
    "/api/v1/pages/%40page-1?workspace_id=ws+%2F%26",
    "/api/v1/pages/%40page-1/document?workspace_id=ws+%2F%26",
  ])
  for (const [, init] of vi.mocked(apiFetch).mock.calls) {
    expect(init?.cache).toBe("no-store"); expect(init?.signal).toBeInstanceOf(AbortSignal)
    expect(init?.method).toBeUndefined()
  }
  await act(async () => { pending.resolve(response({ ...saved(), schema })) })
  await waitFor(() => expect(result.current.read.status).toBe("ready"))
  expect(result.current.read).toMatchObject({ title: "Current authorized title", content: { schema, rev: 8, content: body }, scopeKey: result.current.scopeKey, target: input().target })
})

it.each(["disabled", "not exactly enabled", "no epoch", "no human", "no chat", "no workspace", "closed", "wrong workspace", "empty Page", "wrong type", "pinned mode"])("makes no request for invalid activation: %s", kind => {
  const p = input()
  if (kind === "disabled") p.enabled = false
  if (kind === "not exactly enabled") p.enabled = 1 as unknown as boolean
  if (kind === "no epoch") p.activationKey = " "
  if (kind === "no human") p.currentUserId = ""
  if (kind === "no chat") p.chatId = ""
  if (kind === "no workspace") p.workspaceId = ""
  if (kind === "closed") p.target = null
  if (kind === "wrong workspace") p.target = { ...p.target!, workspaceId: "other" }
  if (kind === "empty Page") p.target = { ...p.target!, pageId: "" }
  if (kind === "wrong type") p.target = { ...p.target!, type: "app" } as unknown as typeof p.target
  if (kind === "pinned mode") p.target = { ...p.target!, mode: "pinned" } as unknown as typeof p.target
  const { result } = renderHook(() => useDocumentViewerRead(p))
  expect(apiFetch).not.toHaveBeenCalled(); expect(result.current.read).toEqual({ status: "unavailable" })
})

it.each([{ id: "other" }, { has_document: false }, { has_document: 1 }, { name: " " }, { name: "x".repeat(513) }])("refuses unconfirmed Page metadata without requesting a body: %j", change => {
  vi.mocked(apiFetch).mockResolvedValue(response({ ...metadata(), ...change }))
  return (async () => {
    const { result } = renderHook(() => useDocumentViewerRead(input()))
    await waitFor(() => expect(result.current.read.status).toBe("unavailable"))
    expect(apiFetch).toHaveBeenCalledTimes(1)
    expect(result.current.read).not.toHaveProperty("title")
  })()
})

it.each([
  { page_id: "other" }, { schema: 99 }, { rev: -1 }, { rev: 9007199254740992 },
  { content: null }, { content: body.content[0] },
  { content: { type: "doc", content: [{ ...body.content[0], content: [{ type: "text", text: "Unsafe", marks: [{ type: "link", attrs: { href: "javascript:alert(1)" } }] }] }] } },
])("rejects invalid saved identity/schema/content: %j", change => {
  vi.mocked(apiFetch).mockResolvedValueOnce(response(metadata())).mockResolvedValueOnce(response({ ...saved(), ...change }))
  return (async () => {
    const { result } = renderHook(() => useDocumentViewerRead(input()))
    await waitFor(() => expect(result.current.read.status).toBe("unavailable"))
    expect(result.current.read).not.toHaveProperty("title"); expect(result.current.read).not.toHaveProperty("content")
  })()
})

it.each(["human", "workspace", "chat", "activation", "target"])("masks the previous read in the very first render after %s changes", async kind => {
  const seen: string[] = []
  const { result, rerender } = renderHook(p => {
    const current = useDocumentViewerRead(p); seen.push(current.read.status); return current
  }, { initialProps: input() })
  await waitFor(() => expect(result.current.read.status).toBe("ready"))
  const oldScope = result.current.scopeKey
  vi.mocked(apiFetch).mockReturnValue(new Promise(() => {}))
  const next = input()
  if (kind === "human") next.currentUserId = "human-2"
  if (kind === "workspace") { next.workspaceId = "ws-2"; next.target = { ...next.target!, workspaceId: "ws-2" } }
  if (kind === "chat") next.chatId = "chat-2"
  if (kind === "activation") next.activationKey = "open-auth-2"
  if (kind === "target") next.target = { ...next.target!, pageId: "page-2" }
  seen.length = 0; rerender(next)
  expect(seen[0]).toBe("loading"); expect(result.current.scopeKey).not.toBe(oldScope)
  expect(result.current.read).not.toHaveProperty("title")
})

it.each([[401, "denied"], [403, "denied"], [404, "denied"], [503, "unavailable"], [500, "error"]] as const)("refresh %s clears all protected data", async (status, expected) => {
  const { result } = renderHook(() => useDocumentViewerRead(input()))
  await waitFor(() => expect(result.current.read.status).toBe("ready"))
  vi.mocked(apiFetch).mockResolvedValue(response({ error: "Private error" }, status))
  act(() => events.get("page.updated")?.())
  expect(result.current.read).toEqual({ status: "loading" })
  await waitFor(() => expect(result.current.read).toEqual({ status: expected }))
})

it("refreshes on reconnect and never reuses a previous ready body after close/reopen", async () => {
  const { result, rerender } = renderHook(p => useDocumentViewerRead(p), { initialProps: input() })
  await waitFor(() => expect(result.current.read.status).toBe("ready"))
  const scope = result.current.scopeKey
  act(() => events.get("realtime.reconnected")?.())
  expect(result.current.scopeKey).not.toBe(scope)
  await waitFor(() => expect(result.current.read.status).toBe("ready"))
  rerender({ ...input(), enabled: false })
  expect(result.current.read.status).toBe("unavailable")
  vi.mocked(apiFetch).mockReturnValue(new Promise(() => {}))
  rerender(input())
  expect(result.current.read).toEqual({ status: "loading" })
})

it("aborts obsolete reads and ignores late bodies even when transport ignores cancellation", async () => {
  const old = deferred()
  vi.mocked(apiFetch).mockResolvedValueOnce(response(metadata())).mockReturnValueOnce(old.promise)
  const { result, rerender, unmount } = renderHook(p => useDocumentViewerRead(p), { initialProps: input() })
  await waitFor(() => expect(apiFetch).toHaveBeenCalledTimes(2))
  const signal = vi.mocked(apiFetch).mock.calls[1][1]?.signal
  vi.mocked(apiFetch).mockResolvedValueOnce(response(metadata("page-2"))).mockResolvedValueOnce(response(saved("page-2")))
  rerender({ ...input(), target: { ...input().target!, pageId: "page-2" } })
  await waitFor(() => expect(result.current.read.status).toBe("ready"))
  expect(signal?.aborted).toBe(true)
  await act(async () => { old.resolve(response(saved())) })
  expect(result.current.read).toMatchObject({ target: { pageId: "page-2" } })
  const currentSignal = vi.mocked(apiFetch).mock.calls[3][1]?.signal
  unmount(); expect(currentSignal?.aborted).toBe(true)
})

it("does not start an obsolete body read after late metadata or expose data after network failure", async () => {
  const old = deferred()
  vi.mocked(apiFetch).mockReturnValueOnce(old.promise).mockRejectedValueOnce(new Error("Offline"))
  const { result, rerender } = renderHook(p => useDocumentViewerRead(p), { initialProps: input() })
  rerender({ ...input(), activationKey: "new-open" })
  await waitFor(() => expect(result.current.read.status).toBe("error"))
  await act(async () => { old.resolve(response(metadata())) })
  expect(apiFetch).toHaveBeenCalledTimes(2)
  expect(result.current.read).toEqual({ status: "error" })
})

it.each([[403,"denied"],[503,"unavailable"]] as const)("withholds authorized metadata when the second read returns %s", async (status, expected) => {
  vi.mocked(apiFetch).mockResolvedValueOnce(response(metadata())).mockResolvedValueOnce(response({error:"Private body failure"},status))
  const {result}=renderHook(()=>useDocumentViewerRead(input()))
  await waitFor(()=>expect(result.current.read).toEqual({status:expected}))
})

it("fails closed for malformed JSON and encodes the Page identity as one path segment", async () => {
  const p=input();p.target={...p.target!,pageId:"page /&"}
  vi.mocked(apiFetch).mockResolvedValueOnce(response(metadata("page /&"))).mockResolvedValueOnce(new Response("not JSON"))
  const {result}=renderHook(()=>useDocumentViewerRead(p))
  await waitFor(()=>expect(result.current.read.status).toBe("unavailable"))
  expect(vi.mocked(apiFetch).mock.calls[1][0]).toBe("/api/v1/pages/%40page%20%2F%26/document?workspace_id=ws+%2F%26")
})
