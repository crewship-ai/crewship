/** UI/capability fixtures only; no native/SQL or live qualification. */
import React from "react"
import { act, cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react"
import { afterEach, beforeEach, expect, it, vi } from "vitest"
import { WordCraftCommandEditorHost } from "../wordcraft-command-editor-host"
import type { CommandSessionCallbacks } from "@/lib/mooring/wordcraft-command-session"
const mocks = vi.hoisted(() => ({ open: vi.fn(), factory: vi.fn(), start: vi.fn(), submit: vi.fn(), applied: vi.fn(), reconcile: vi.fn(), stop: vi.fn(), close: vi.fn(), human: "alice" }))
vi.mock("@/hooks/use-auth", () => ({ useSessionSafe: () => ({ status: "authenticated", data: { user: { id: mocks.human } } }) }))
vi.mock("@/lib/editor-lifecycle/use-leave-guard", () => ({ useLeaveGuard: vi.fn() }))
vi.mock("@/lib/mooring/wordcraft-command-session", () => ({ commandRequest: mocks.open, createCommandSession: mocks.factory }))
const digest = "a".repeat(64), frameWindow = { postMessage: vi.fn() }
const credentialless = Object.getOwnPropertyDescriptor(HTMLIFrameElement.prototype, "credentialless")
const contentWindow = Object.getOwnPropertyDescriptor(HTMLIFrameElement.prototype, "contentWindow")
const ports: MessagePort[] = []
let callbacks: CommandSessionCallbacks
const projection = { generation: "g1", revision: 0, ids: ["doc:g1:block:1"], native_json: '{"opaque":18446744073709551615}' }
beforeEach(() => {
  vi.clearAllMocks(); mocks.human = "alice"
  Object.defineProperty(HTMLIFrameElement.prototype, "credentialless", { configurable: true, get() { return (this as HTMLIFrameElement).hasAttribute("credentialless") } })
  Object.defineProperty(HTMLIFrameElement.prototype, "contentWindow", { configurable: true, get: () => frameWindow })
  mocks.open.mockResolvedValue({ view: "view", actor: "human:alice", projection, outcome: null, editor: { url: `/_editor/b/${digest}/index.html`, bundle_digest: digest } })
  mocks.factory.mockImplementation((_workspace, _page, _opened, _write, cb) => {
    callbacks = cb
    return { start: mocks.start, submit: mocks.submit, applied: mocks.applied, reconcile: mocks.reconcile, stop: mocks.stop, close: mocks.close }
  })
  mocks.submit.mockReturnValue(true)
})
afterEach(() => {
  cleanup(); ports.splice(0).forEach(port => port.close())
  if (credentialless) Object.defineProperty(HTMLIFrameElement.prototype, "credentialless", credentialless)
  else delete (HTMLIFrameElement.prototype as { credentialless?: unknown }).credentialless
  if (contentWindow) Object.defineProperty(HTMLIFrameElement.prototype, "contentWindow", contentWindow)
})
async function open() {
  const dirty = vi.fn(), view = render(<WordCraftCommandEditorHost workspaceId="ws" pageId="page" name="Original" onDirtyChange={dirty} />)
  await waitFor(() => expect(document.querySelector("iframe")).not.toBeNull())
  const frame = document.querySelector("iframe")!
  await act(async () => {
    frame.dispatchEvent(new Event("load"))
    window.dispatchEvent(new MessageEvent("message", { source: frameWindow as unknown as Window, origin: "null", data: { type: "wordcraft-command:hello", protocol: 1, bundle: digest } }))
  })
  const port = frameWindow.postMessage.mock.calls[0][2][0] as MessagePort
  ports.push(port)
  const send = async (value: object) => { await act(async () => { port.postMessage(JSON.stringify({ protocol: 1, ...value })); await new Promise(resolve => setTimeout(resolve, 10)) }) }
  return { frame, view, port, send, dirty }
}
it("opens the actual C1 endpoint and isolates its pinned bundle/channel from legacy WordCraft", async () => {
  const f = await open(), url = new URL(f.frame.src)
  expect(mocks.open).toHaveBeenCalledWith("ws", "page", { action: "open", write: true }, expect.any(AbortSignal))
  expect(url.searchParams.get("commands")).toBe("1"); expect(url.searchParams.get("mooring")).toBe("1")
  expect(f.frame.getAttribute("sandbox")).toBe("allow-scripts"); expect(f.frame.hasAttribute("credentialless")).toBe(true)
  await f.send({ type: "wordcraft-command:ready" }); expect(mocks.start).toHaveBeenCalledOnce()
  await f.send({ type: "wordcraft-command:command", raw: "EXACT native proposal", revision: 0 })
  expect(mocks.submit).toHaveBeenCalledWith("EXACT native proposal", 0)
  expect(screen.getByText(/Undo and rapid typing are not available/)).toBeInTheDocument()
})
it("keeps the same frame/input paused on unknown and offers only explicit original-outcome lookup", async () => {
  const f = await open(); await f.send({ type: "wordcraft-command:ready" })
  await act(async () => { callbacks.state("pending"); callbacks.state("unknown") })
  expect(f.frame.hidden).toBe(false); expect(f.dirty).toHaveBeenLastCalledWith(true)
  expect(screen.queryByText("Saved")).toBeNull()
  fireEvent.click(screen.getByRole("button", { name: "Check original save" }))
  expect(mocks.reconcile).toHaveBeenCalledOnce(); expect(mocks.submit).not.toHaveBeenCalled()
  f.view.rerender(<WordCraftCommandEditorHost workspaceId="ws" pageId="page" name="Renamed" />)
  expect(document.querySelector("iframe")).toBe(f.frame); expect(mocks.open).toHaveBeenCalledOnce()
})
it("child Saved assertions cannot mint acknowledgements", async () => {
  const f = await open(); await f.send({ type: "wordcraft-command:ready" })
  await f.send({ type: "wordcraft-command:saved", revision: 1 })
  expect(mocks.stop).toHaveBeenCalledOnce(); expect(f.frame.hidden).toBe(true)
  expect(screen.queryByText("Saved")).toBeNull(); expect(f.frame.isConnected).toBe(true)
})
it("a current Page denial stops and retains the original producer and does not restart after recovery", async () => {
  const f = await open()
  f.view.rerender(<WordCraftCommandEditorHost workspaceId="ws" pageId="page" unavailable />)
  await waitFor(() => expect(f.frame.hidden).toBe(true))
  f.view.rerender(<WordCraftCommandEditorHost workspaceId="ws" pageId="page" />)
  expect(document.querySelector("iframe")).toBe(f.frame); expect(mocks.open).toHaveBeenCalledOnce()
})
it("uses a distinct fresh reader admission after writer denial and never enables writes", async () => {
  mocks.open.mockRejectedValueOnce(new Error("access_unavailable"))
  const f = await open()
  expect(mocks.open.mock.calls.map(call => call[2])).toEqual([{ action: "open", write: true }, { action: "open", write: false }])
  expect(mocks.factory.mock.calls[0][3]).toBe(false)
  expect(f.frame.isConnected).toBe(true)
})

it("explains unsupported isolation before opening a command session or mounting an editor", async () => {
  delete (HTMLIFrameElement.prototype as { credentialless?: unknown }).credentialless
  render(<WordCraftCommandEditorHost workspaceId="ws" pageId="page" />)
  expect(await screen.findByRole("alert")).toHaveTextContent("Tento prohlížeč zatím není podporován")
  expect(screen.getByRole("status")).toHaveTextContent("Editor unavailable")
  expect(document.querySelector("iframe")).toBeNull()
  expect(mocks.open).not.toHaveBeenCalled()
  expect(mocks.factory).not.toHaveBeenCalled()
  expect(screen.queryByText("Saved")).toBeNull()
})
