// Docs presence rate: one heartbeat per interval while nothing moves. A
// selection update that does not move the shared caret (a browser re-reading
// its selection after the remote carets were redrawn, for example) must not
// send a heartbeat: answered with one, each reply redrew the carets again and
// the tab looped at the 150 ms selection throttle's ceiling (41 beats in 6 s
// here before the fix). An unchanged reply does not redraw at all.
import { act, renderHook } from "@testing-library/react"
import { afterEach, beforeEach, expect, it, vi } from "vitest"
import * as Y from "yjs"
import { Editor } from "@tiptap/core"
import { TextSelection } from "@tiptap/pm/state"
import { documentSessionExtensions } from "@/components/features/pages/page-document"
import { remoteCaretsKey } from "@/components/features/pages/document-remote-carets"
import { useDocumentPresence } from "../use-document-presence"

const request = vi.hoisted(() => vi.fn())
vi.mock("@/lib/api-fetch", () => ({ apiFetch: request, broadcastSessionExpired: vi.fn() }))

const editors: Editor[] = []
beforeEach(() => { vi.useFakeTimers() })
afterEach(() => { for (const editor of editors.splice(0)) editor.destroy(); request.mockReset(); vi.useRealTimers(); document.body.innerHTML = "" })

function editor() {
  const element = document.createElement("div"); document.body.append(element)
  const created = new Editor({ element, extensions: documentSessionExtensions(new Y.Doc({ gc: false }), 4) })
  created.commands.setContent("<p>hello world</p><p>second line</p>")
  created.commands.setTextSelection(5)
  editors.push(created)
  return created
}

const peer = { client: "tab-erin-0001", user_id: "erin", name: "Erin Editor", color: 1, tabs: 1 }
const reply = () => ({ ok: true, status: 200, json: async () => ({ peers: [peer], color: 0, ttl_ms: 6000, interval_ms: 1000 }) })
const beats = () => request.mock.calls.filter(([, init]) => !JSON.parse(String((init as RequestInit).body)).leave).length

/** What a real browser does after a caret redraw: the DOM selection is read back and lands on the same place by a detour. */
function echoSelectionAfterCaretRedraw(target: Editor) {
  target.on("transaction", ({ transaction }) => {
    if (!transaction.getMeta(remoteCaretsKey)) return
    queueMicrotask(() => {
      if (target.isDestroyed) return
      const at = target.state.selection.anchor
      target.view.dispatch(target.state.tr.setSelection(TextSelection.create(target.state.doc, at + 1)))
      target.view.dispatch(target.state.tr.setSelection(TextSelection.create(target.state.doc, at)))
    })
  })
}

it("sends one heartbeat per interval while the caret does not move, even when a redraw echoes the selection", async () => {
  request.mockImplementation(async () => { await new Promise(resolve => setTimeout(resolve, 40)); return reply() })
  const target = editor()
  echoSelectionAfterCaretRedraw(target)
  const hook = renderHook(() => useDocumentPresence({ workspaceId: "w", slug: "notes", editor: target, enabled: true }))
  await act(async () => { await vi.advanceTimersByTimeAsync(6000) })
  // The first beat at 0 ms, then one per 1 s interval after each reply.
  expect(beats()).toBeGreaterThanOrEqual(5)
  expect(beats()).toBeLessThanOrEqual(7)
  hook.unmount()
})

it("still shares a real caret move within the selection throttle", async () => {
  request.mockImplementation(async () => reply())
  const target = editor()
  const hook = renderHook(() => useDocumentPresence({ workspaceId: "w", slug: "notes", editor: target, enabled: true }))
  await act(async () => { await vi.advanceTimersByTimeAsync(300) })
  const before = beats()
  act(() => { target.commands.setTextSelection(14) })
  await act(async () => { await vi.advanceTimersByTimeAsync(200) })
  expect(beats()).toBe(before + 1)
  hook.unmount()
})

it("does not redraw the carets when the reply did not change", async () => {
  request.mockImplementation(async () => reply())
  const target = editor()
  let redraws = 0
  target.on("transaction", ({ transaction }) => { if (transaction.getMeta(remoteCaretsKey)) redraws++ })
  const hook = renderHook(() => useDocumentPresence({ workspaceId: "w", slug: "notes", editor: target, enabled: true }))
  await act(async () => { await vi.advanceTimersByTimeAsync(5500) })
  expect(beats()).toBeGreaterThanOrEqual(5)
  expect(redraws).toBe(1)
  hook.unmount()
})
