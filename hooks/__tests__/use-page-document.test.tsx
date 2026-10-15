import { act, renderHook, waitFor } from "@testing-library/react"
import { afterEach, describe, expect, it, vi } from "vitest"
import * as Y from "yjs"
import { documentRequest, encodeUpdate, decodeUpdate, usePageDocument } from "../use-page-document"
const fetchMock = vi.hoisted(() => vi.fn())
vi.mock("@/lib/api-fetch", () => ({ apiFetch: fetchMock }))
afterEach(() => { vi.restoreAllMocks(); fetchMock.mockReset() })
function response(body: unknown, status = 200) { return Promise.resolve({ ok: status < 400, status, json: async () => body }) }
describe("document durable synchronization", () => {
  it("round-trips binary updates", () => { expect(decodeUpdate(encodeUpdate(new Uint8Array([0, 255, 128])))).toEqual(new Uint8Array([0, 255, 128])) })
  it("merges remote changes while retrying the same unacknowledged local update", async () => {
    const server = new Y.Doc(); server.getText("text").insert(0, "base")
    const ids: string[] = []; let fail = true
    fetchMock.mockImplementation((_url: string, init?: RequestInit) => {
      if (init?.method === "POST") { const body = JSON.parse(init.body as string); ids.push(body.id); if (fail) return response({ error: "offline" }, 503); Y.applyUpdate(server, decodeUpdate(body.update)); return response({ rev: 2 }) }
      return response({ schema: 1, rev: 1, epoch: "go:node:1", can_write: true, snapshot: encodeUpdate(Y.encodeStateAsUpdate(server)) })
    })
    const { result, unmount } = renderHook(() => usePageDocument("w", "p"))
    await waitFor(() => expect(result.current.state).toBeDefined())
    act(() => { for (let i = 0; i < 100; i++) result.current.doc.getText("text").insert(4, " local") })
    await waitFor(() => expect(ids.length).toBeGreaterThan(0), { timeout: 4000 })
    server.getText("text").insert(0, "remote "); fail = false
    await waitFor(() => expect(result.current.pending).toBe(0), { timeout: 4000 })
    expect(new Set(ids).size).toBe(1)
    expect(result.current.doc.getText("text").toString()).toContain("remote")
    expect(result.current.doc.getText("text").toString()).toContain("local")
    unmount(); server.destroy()
  })
  it("refreshes durable metadata before Saved so an immediate checkpoint uses the committed revision", async () => {
    let rev = 1
    fetchMock.mockImplementation((url: string, init?: RequestInit) => {
      if (url.includes("/patch")) { rev = 2; return response({ rev }) }
      if (url.includes("/checkpoint")) { const body = JSON.parse(init!.body as string); return response({ rev }, body.ifRev === rev ? 200 : 409) }
      return response({ schema: 2, rev, epoch: "go:node:1", can_write: true, snapshot: "", content: { savedRevision: rev }, outline: [{ id: "heading", text: `Revision ${rev}` }] })
    })
    const { result, unmount } = renderHook(() => usePageDocument("w", "p"))
    await waitFor(() => expect(result.current.status).toBe("Saved"))
    act(() => result.current.doc.getText("text").insert(0, "edit"))
    await waitFor(() => { expect(result.current.status).toBe("Saved"); expect(result.current.state?.rev).toBe(2) })
    expect(result.current.state?.content).toEqual({ savedRevision: 2 })
    expect(result.current.state?.outline?.[0].text).toBe("Revision 2")
    await expect(documentRequest("w", "p", "/checkpoint", { ifRev: result.current.state?.rev })).resolves.toEqual({ rev: 2 })
    unmount()
  })
  it("does not recommit an acknowledged batch when its metadata refresh fails", async () => {
    let rev = 1, readFailed = true, commits = 0
    fetchMock.mockImplementation((url: string) => {
      if (url.includes("/patch")) { commits++; rev = 2; return response({ rev }) }
      if (rev === 2 && readFailed) return response({ error: "read temporarily unavailable" }, 503)
      return response({ schema: 2, rev, epoch: "go:node:1", can_write: true, snapshot: "" })
    })
    const { result, unmount } = renderHook(() => usePageDocument("w", "p"))
    await waitFor(() => expect(result.current.status).toBe("Saved"))
    act(() => result.current.doc.getText("text").insert(0, "edit"))
    await waitFor(() => expect(result.current.status).toBe("Disconnected"))
    expect(result.current.pending).toBe(0)
    readFailed = false
    await waitFor(() => expect(result.current.state?.rev).toBe(2))
    expect(commits).toBe(1)
    unmount()
  })
  it.each([{ status: 400 }, { status: 413 }, { status: 422 }, { status: 429, code: "document_quota" }])("retains a rejected draft and stops retries for $status $code", async ({ status, code }) => {
    let commits = 0
    fetchMock.mockImplementation((url: string) => {
      if (url.includes("/patch")) { commits++; return response({ error: "unsupported document content", code }, status) }
      return response({ schema: 2, rev: 1, epoch: "go:node:1", can_write: true, snapshot: "" })
    })
    const { result, unmount } = renderHook(() => usePageDocument("w", "p"))
    await waitFor(() => expect(result.current.editable).toBe(true))
    act(() => result.current.doc.getText("text").insert(0, "Recover this draft"))
    await waitFor(() => expect(result.current.recovery).toBe(true))
    expect(result.current.editable).toBe(false)
    expect(result.current.status).toContain("rejected")
    expect(result.current.pending).toBe(1)
    const recovered = new Y.Doc(); Y.applyUpdate(recovered, Y.encodeStateAsUpdate(result.current.doc))
    expect(recovered.getText("text").toString()).toBe("Recover this draft")
    await act(async () => { await new Promise(resolve => setTimeout(resolve, 1100)) })
    expect(commits).toBe(1)
    recovered.destroy(); unmount()
  })
  it("keeps the draft and disables editing after a restore changes the generation", async () => {
    let epoch = "go:node:1"
    fetchMock.mockImplementation(() => response({ schema: 1, rev: 1, epoch, can_write: true, snapshot: "" }))
    const { result, unmount } = renderHook(() => usePageDocument("w", "p"))
    await waitFor(() => expect(result.current.editable).toBe(true))
    act(() => result.current.doc.getText("text").insert(0, "draft")); epoch = "go:new-node:1"
    await waitFor(() => expect(result.current.recovery).toBe(true), { timeout: 4000 })
    expect(result.current.editable).toBe(false)
    expect(result.current.doc.getText("text").toString()).toBe("draft")
    expect(fetchMock.mock.calls.some(([, init]) => init?.method === "POST")).toBe(false)
    unmount()
  })
})
