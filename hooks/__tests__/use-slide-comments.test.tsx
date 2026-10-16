import { act, renderHook, waitFor } from "@testing-library/react"
import { beforeEach, describe, expect, it, vi } from "vitest"
import { useSlideComments, type CommentAttempt } from "../use-slide-comments"
import { page, thread } from "@/lib/slides/__tests__/comment-fixtures"
vi.mock("@/lib/api-fetch", () => ({ apiFetch: vi.fn() }))
import { apiFetch } from "@/lib/api-fetch"
const fetch = vi.mocked(apiFetch)
const response = (body: unknown, status = 200) => new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } })
const reply = (): CommentAttempt => ({ path: "/thread_one/replies", threadId: "thread_one", body: { page_id: "page_one", schema_version: 1, mutation_id: "reply_one", if_version: 1, body: "retained draft" } })
async function loaded() { fetch.mockResolvedValue(response(page())); const h = renderHook(({ slug }) => useSlideComments("workspace", slug, "page_one"), { initialProps: { slug: "report" } }); await waitFor(() => expect(h.result.current.threads).toHaveLength(1)); return h }
beforeEach(() => { fetch.mockReset() })
describe("immutable human comment flow", () => {
 it("retries original body after uncertain acknowledgement and same-Page rename", async () => {
  const h = await loaded(); let original: unknown
  fetch.mockImplementation(async (_url, options) => { original = options?.body; throw new Error("ack lost") }); await act(async () => { await h.result.current.send(reply()) })
  expect(h.result.current.pending?.body).toEqual(reply().body); h.rerender({ slug: "renamed" }); fetch.mockClear()
  fetch.mockImplementation(async (url, options) => { expect(String(url)).toContain("/renamed/slides/comments"); if (options?.method === "POST") { expect(options.body).toBe(original); return response({ mutation_id: "reply_one", thread_id: "thread_one", version: 2, duplicate: true }) } return response(String(url).includes("history") ? { ...page(), items: [thread().first_comment] } : page()) })
  await act(async () => { expect(await h.result.current.send({ ...reply(), body: { ...reply().body, mutation_id: "wrong_new" } })).toBe(true) }); expect(h.result.current.pending).toBeUndefined()
 })
 it("retains stale thread preconditions until explicit discard and review", async () => {
  const h = await loaded(); fetch.mockResolvedValue(response({ error: "stale version" }, 409)); await act(async () => { await h.result.current.send(reply()) }); expect(h.result.current.blocked).toBe(true); expect(h.result.current.pending?.body).toEqual(reply().body)
  fetch.mockResolvedValue(response({ ...page(), items: [{ ...thread(), version: 4 }] })); await act(async () => { await h.result.current.refresh() }); expect(h.result.current.pending?.body).toEqual(reply().body); expect(h.result.current.blocked).toBe(true)
  await act(async () => { await h.result.current.discardAttempt() }); expect(h.result.current.pending).toBeUndefined(); expect(h.result.current.blocked).toBe(false)
 })
 it("clears private threads/history and disables retry after access revocation", async () => {
  const h = await loaded(); fetch.mockResolvedValue(response({ ...page(), items: [thread().first_comment] })); await act(async () => { await h.result.current.loadHistory("thread_one") }); expect(h.result.current.history).toBeDefined()
  fetch.mockResolvedValue(response({ error: "revoked" }, 403)); await act(async () => { await h.result.current.send(reply()) }); expect(h.result.current.threads).toEqual([]); expect(h.result.current.history).toBeUndefined(); expect(h.result.current.page).toBeUndefined(); expect(h.result.current.pending?.body).toEqual(reply().body); expect(h.result.current.blocked).toBe(true)
 })
 it("does not repopulate private history from a read pending at revocation", async () => {
  const h = await loaded(); let resolveHistory!: (value: Response) => void
  fetch.mockImplementationOnce(() => new Promise(resolve => { resolveHistory = resolve })); let old!: Promise<void>; act(() => { old = h.result.current.loadHistory("thread_one") })
  fetch.mockResolvedValue(response({ error: "revoked" }, 403)); await act(async () => { await h.result.current.refresh() })
  await act(async () => { resolveHistory(response({ ...page(), items: [thread().first_comment] })); await old }); expect(h.result.current.history).toBeUndefined(); expect(h.result.current.threads).toEqual([])
 })
 it("invalidates pending thread list when history reports access loss", async () => {
  const h = await loaded(); let resolveList!: (value: Response) => void
  fetch.mockImplementationOnce(() => new Promise(resolve => { resolveList = resolve })); let old!: Promise<void>; act(() => { old = h.result.current.refresh() })
  fetch.mockResolvedValue(response({ error: "revoked" }, 403)); await act(async () => { await h.result.current.loadHistory("thread_one") })
  await act(async () => { resolveList(response(page())); await old }); expect(h.result.current.threads).toEqual([]); expect(h.result.current.page).toBeUndefined(); expect(h.result.current.blocked).toBe(true)
  fetch.mockResolvedValue(response(page())); await act(async () => { await h.result.current.refresh() }); expect(h.result.current.blocked).toBe(false); expect(h.result.current.threads).toHaveLength(1)
 })
 it("rejects late earlier reads after a newer thread snapshot", async () => {
  const h = await loaded(); let resolveOld!: (value: Response) => void; fetch.mockImplementationOnce(() => new Promise(resolve => { resolveOld = resolve }))
  let old!: Promise<void>; act(() => { old = h.result.current.refresh() }); fetch.mockResolvedValue(response({ ...page(), items: [{ ...thread(), version: 3 }] })); await act(async () => { await h.result.current.refresh() }); await act(async () => { resolveOld(response(page())); await old }); expect(h.result.current.threads[0].version).toBe(3)
 })
 it("keeps cursor pagination bounded to the original response", async () => {
  const h = await loaded(); fetch.mockResolvedValue(response({ ...page(), has_more: true, next_cursor: "opaque_cursor", watermark: 2 })); await act(async () => { await h.result.current.refresh() }); fetch.mockImplementation(async url => { expect(String(url)).toContain("cursor=opaque_cursor"); return response({ ...page(), items: [{ ...thread(), id: "thread_two", first_comment: { ...thread().first_comment, mutation_id: "thread_two" } }], watermark: 2 }) }); await act(async () => { await h.result.current.refresh(h.result.current.page?.next_cursor) }); expect(h.result.current.threads.map(item => item.id)).toEqual(["thread_one", "thread_two"])
 })
})
