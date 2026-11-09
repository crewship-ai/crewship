// #5166: the presence heartbeat stops when the server refuses it (access gone)
// and tells the server at once when the editor leaves the document, also when
// a heartbeat is still in flight at that moment.
import { act, renderHook, waitFor } from "@testing-library/react"
import { afterEach, expect, it, vi } from "vitest"
import * as Y from "yjs"
import { Editor } from "@tiptap/core"
import { documentSessionExtensions } from "@/components/features/pages/page-document"
import { useDocumentPresence } from "../use-document-presence"

const request = vi.hoisted(() => vi.fn())
vi.mock("@/lib/api-fetch", () => ({ apiFetch: request, broadcastSessionExpired: vi.fn() }))

const editors: Editor[] = []
afterEach(() => { for (const editor of editors.splice(0)) editor.destroy(); request.mockReset(); document.body.innerHTML = "" })

function editor() {
  const doc = new Y.Doc({ gc: false })
  const element = document.createElement("div"); document.body.append(element)
  const created = new Editor({ element, extensions: documentSessionExtensions(doc, 2) })
  editors.push(created)
  return created
}

const reply = (status: number, body: unknown = {}) => ({ ok: status < 400, status, json: async () => body })
const peer = { client: "tab-erin-0001", user_id: "erin", name: "Erin Editor", color: 1 }
const bodies = () => request.mock.calls.map(([, init]) => JSON.parse(String((init as RequestInit).body)) as { client: string; leave?: boolean })

it("clears everybody and stops on a refusal", async () => {
  let refuse = false
  request.mockImplementation(async () => refuse ? reply(404) : reply(200, { peers: [peer], color: 0, ttl_ms: 6000, interval_ms: 500 }))
  const hook = renderHook(() => useDocumentPresence({ workspaceId: "w", slug: "notes", editor: editors[0] ?? editor(), enabled: true }))
  await waitFor(() => expect(hook.result.current.map(p => p.name)).toEqual(["Erin Editor"]))
  refuse = true
  await waitFor(() => expect(hook.result.current).toEqual([]), { timeout: 3000 })
  const sent = request.mock.calls.length
  await act(async () => { await new Promise(resolve => setTimeout(resolve, 1300)) })
  expect(request.mock.calls.length).toBe(sent)
  hook.unmount()
})

it("leaves on unmount, again after a heartbeat that was still in flight", async () => {
  let release: (() => void) | undefined
  request.mockImplementation(async (_url: string, init: RequestInit) => {
    const body = JSON.parse(String(init.body)) as { leave?: boolean }
    if (body.leave) return reply(200, { peers: [] })
    await new Promise<void>(resolve => { release = resolve })
    return reply(200, { peers: [peer], color: 0, ttl_ms: 6000, interval_ms: 500 })
  })
  const hook = renderHook(() => useDocumentPresence({ workspaceId: "w", slug: "notes", editor: editors[0] ?? editor(), enabled: true }))
  await waitFor(() => expect(release).toBeDefined())
  hook.unmount()
  expect(bodies().filter(body => body.leave)).toHaveLength(1)
  await act(async () => { release?.(); await new Promise(resolve => setTimeout(resolve, 20)) })
  const all = bodies()
  expect(all.filter(body => body.leave)).toHaveLength(2)
  expect(all.at(-1)?.leave).toBe(true)
  expect(new Set(all.map(body => body.client)).size).toBe(1)
  expect(request.mock.calls.every(([url, init]) => String(url).includes("/document/presence?workspace_id=w") && (init as RequestInit).method === "POST")).toBe(true)
})
