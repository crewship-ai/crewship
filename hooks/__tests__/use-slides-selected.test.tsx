import { act, renderHook, waitFor } from "@testing-library/react"
import { beforeEach, expect, it, vi } from "vitest"
import { useSlidesSelected, type SelectedAttempt } from "../use-slides-selected"
vi.mock("@/lib/api-fetch", () => ({ apiFetch: vi.fn() }))
import { apiFetch } from "@/lib/api-fetch"
const fetch = vi.mocked(apiFetch)
const page = () => ({ items: [], next_cursor: "", watermark: 1, has_more: false, can_dispatch: true, can_cancel: true, eligible_chats: [{ chat_id: "private_chat", agent_name: "Private agent", title: "Private title" }] })
const response = (body: unknown, status = 200) => new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } })
const admit = (): SelectedAttempt => ({ kind: "admit", path: "", body: { page_id: "page_one", type_id: "slides", schema_version: 1, base_rev: 1, submission_id: "original_submission", chat_id: "private_chat", instruction: "Improve wording", selection: { selection_schema_version: 1, kind: "text_range", slide_id: "slide_one", element_id: "element_one", element_hash: "a".repeat(64), from: 0, to: 1, quote: "A" } } })
async function loaded() { fetch.mockResolvedValue(response(page())); const h = renderHook(({ slug, pageId }) => useSlidesSelected("workspace", slug, pageId), { initialProps: { slug: "report", pageId: "page_one" } }); await waitFor(() => expect(h.result.current.page?.can_dispatch).toBe(true)); return h }
beforeEach(() => { fetch.mockReset() })
it("does not repopulate private chat metadata from delayed GET after POST authority denial", async () => {
 const h = await loaded(); let resolveRead!: (value: Response) => void; let old!: Promise<void>
 fetch.mockImplementationOnce(() => new Promise(resolve => { resolveRead = resolve })); act(() => { old = h.result.current.refresh() })
 fetch.mockResolvedValue(response({ error: "revoked exact credential" }, 403)); await act(async () => { await h.result.current.send(admit()) })
 expect(h.result.current.page).toBeUndefined(); expect(h.result.current.blocked).toBe(true)
 await act(async () => { resolveRead(response(page())); await old })
 expect(h.result.current.page).toBeUndefined(); expect(h.result.current.items).toEqual([]); expect(h.result.current.pending?.body).toEqual(admit().body)
})
it("retries the frozen original payload after lost ACK and same-Page rename", async () => {
 const h = await loaded(); let original: unknown
 fetch.mockImplementation(async (_url, options) => { original = options?.body; throw new Error("lost ACK") }); await act(async () => { await h.result.current.send(admit()) })
 h.rerender({ slug: "renamed", pageId: "page_one" })
 fetch.mockImplementation(async (url, options) => { expect(String(url)).toContain("/renamed/slides/requests"); if (options?.method === "POST") { expect(options.body).toBe(original); return response({ id: "request_one", request_id: "request_one", version: 1, status: "queued", duplicate: true }) } return response(page()) })
 await act(async () => { expect(await h.result.current.send({ ...admit(), body: { ...admit().body, page_id: "wrong_new_page" } })).toBe(true) })
 expect(h.result.current.pending).toBeUndefined()
})
it("rejects mismatched continuation watermark and clears attempts on Page identity change", async () => {
 const h = await loaded(); fetch.mockResolvedValue(response({ ...page(), has_more: true, next_cursor: "cursor_one" })); await act(async () => { await h.result.current.refresh() })
 fetch.mockResolvedValue(response({ ...page(), watermark: 2 })); await act(async () => { await h.result.current.refresh("cursor_one") }); expect(h.result.current.error).toContain("continuation changed"); expect(h.result.current.page?.watermark).toBe(1)
 fetch.mockRejectedValue(new Error("lost ACK")); await act(async () => { await h.result.current.send(admit()) }); expect(h.result.current.pending).toBeDefined()
 fetch.mockResolvedValue(response(page())); h.rerender({ slug: "new", pageId: "page_two" }); await waitFor(() => expect(h.result.current.pending).toBeUndefined()); expect(h.result.current.items).toEqual([])
})
