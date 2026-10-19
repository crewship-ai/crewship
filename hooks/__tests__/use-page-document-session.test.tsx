import { createElement, type ReactNode } from "react"
import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import { act, renderHook, waitFor } from "@testing-library/react"
import { afterEach, expect, it, vi } from "vitest"
import * as Y from "yjs"
import { documentSchema } from "@/lib/documents/schema"
import { usePageDocument } from "../use-page-document"

const account = vi.hoisted(() => ({ user: "human-A" as string | null, status: "authenticated" }))
const fetchMock = vi.hoisted(() => vi.fn())
vi.mock("@/lib/api-fetch", () => ({ apiFetch: fetchMock }))
vi.mock("@/hooks/use-auth", () => ({ useSessionSafe: () => ({ status: account.status, data: account.user ? { user: { id: account.user } } : null }) }))
afterEach(() => { fetchMock.mockReset(); account.user = "human-A"; account.status = "authenticated" })

function saved(text: string) {
  return documentSchema.create({ type: "doc", content: [{ type: "paragraph", attrs: { id: "paragraph" }, content: [{ type: "text", text }] }] }, 3)
}
function state(doc: Y.Doc, canWrite: boolean) {
  return { page_id: "page", schema: 3, rev: 1, epoch: "opaque", generation: 1, incarnation: "incarnation", snapshot: documentSchema.snapshot(doc), snapshotRev: 1, updates: [], blocks: [], content: { type: "doc" }, outline: [], can_write: canWrite, can_comment: false, can_resolve: false }
}
function response(value: unknown, status = 200) {
  return { ok: status < 400, status, json: async () => value }
}

it.each(["read-only", "denied"])("isolates a new human from an unmounted human's in-flight document read: %s", async (access) => {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false, gcTime: 0 } } })
  const wrapper = ({ children }: { children: ReactNode }) => createElement(QueryClientProvider, { client }, children)
  const privateA = saved("A-only private content"), authorizedB = saved("B-authorized content")
  let resolveA!: (value: ReturnType<typeof response>) => void
  fetchMock.mockImplementation((_url: string, init?: RequestInit) => {
    if (init?.method === "POST") throw new Error("This read-only scenario must not write")
    if (account.user === "human-A") return new Promise<ReturnType<typeof response>>(resolve => { resolveA = resolve })
    return Promise.resolve(access === "denied" ? response({ error: "B has no Page access" }, 403) : response(state(authorizedB, false)))
  })
  const first = renderHook(() => usePageDocument("workspace", "slug", "page"), { wrapper })
  try {
    await waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(1))
    first.unmount()
    account.user = "human-B"
    const next = renderHook(() => usePageDocument("workspace", "slug", "page"), { wrapper })
    try {
      await waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(2))
      await waitFor(() => expect(access === "denied" ? next.result.current.denied : next.result.current.state?.can_write === false).toBe(true))
      await act(async () => { resolveA(response(state(privateA, true))) })
      await waitFor(() => expect(Boolean(next.result.current.state) || next.result.current.denied).toBe(true))
      expect.soft(next.result.current.doc.getXmlFragment("default").toString()).not.toContain("A-only private content")
      expect.soft(next.result.current.editable).toBe(false)
      if (access === "denied") {
        expect.soft(next.result.current.denied).toBe(true)
        expect.soft(next.result.current.state).toBeUndefined()
      } else {
        expect.soft(documentSchema.textNode(next.result.current.doc, "paragraph")?.toString()).toBe("B-authorized content")
        expect.soft(next.result.current.state?.can_write).toBe(false)
      }
    } finally { next.unmount() }
  } finally { first.unmount(); client.clear(); privateA.destroy(); authorizedB.destroy() }
})

it.each(["loading", "unauthenticated", "missing-human"])("makes no private request without a known authenticated human: %s", async (status) => {
  account.status = status === "missing-human" ? "authenticated" : status
  account.user = status === "missing-human" ? null : "human-A"
  const client = new QueryClient()
  const wrapper = ({ children }: { children: ReactNode }) => createElement(QueryClientProvider, { client }, children)
  const view = renderHook(() => usePageDocument("workspace", "slug", "page"), { wrapper })
  try {
    await act(async () => { await new Promise(resolve => setTimeout(resolve, 550)) })
    expect(fetchMock).not.toHaveBeenCalled()
    expect(view.result.current.state).toBeUndefined()
    expect(view.result.current.editable).toBe(false)
    expect(view.result.current.pending).toBe(0)
    expect(view.result.current.recovery).toBe(false)
  } finally { view.unmount(); client.clear() }
})

it("discards the former human's mounted draft and confirmed state on an identity switch", async () => {
  const client = new QueryClient()
  const wrapper = ({ children }: { children: ReactNode }) => createElement(QueryClientProvider, { client }, children)
  const a = saved("A-only saved"), b = saved("B-only saved")
  fetchMock.mockImplementation(() => Promise.resolve(response(state(account.user === "human-A" ? a : b, account.user === "human-A"))))
  const view = renderHook(() => usePageDocument("workspace", "slug", "page"), { wrapper })
  try {
    await waitFor(() => expect(view.result.current.editable).toBe(true))
    const old = view.result.current.doc
    act(() => {
      const text = documentSchema.textNode(old, "paragraph")
      if (!(text instanceof Y.XmlText)) throw new Error("Expected text")
      text.insert(0, "A-only unsaved ")
    })
    expect(view.result.current.pending).toBe(1)
    account.user = null; account.status = "unauthenticated"; view.rerender()
    expect(view.result.current.doc).not.toBe(old)
    expect(view.result.current.state).toBeUndefined()
    expect(view.result.current.pending).toBe(0)
    expect(view.result.current.recovery).toBe(false)
    expect(view.result.current.doc.getXmlFragment("default").length).toBe(0)
    account.user = "human-B"; account.status = "authenticated"; view.rerender()
    await waitFor(() => expect(view.result.current.state?.can_write).toBe(false))
    expect(documentSchema.textNode(view.result.current.doc, "paragraph")?.toString()).toBe("B-only saved")
    expect(view.result.current.pending).toBe(0)
    expect(fetchMock.mock.calls.some(([, init]) => init?.method === "POST")).toBe(false)
  } finally { view.unmount(); client.clear(); a.destroy(); b.destroy() }
})
