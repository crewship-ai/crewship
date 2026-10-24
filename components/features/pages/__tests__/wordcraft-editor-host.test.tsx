/** Synthetic private-port lifecycle tests; no Rust/SQL/WordApp acceptance. */
import React from "react"
import { act, cleanup, render, screen, waitFor } from "@testing-library/react"
import { afterEach, beforeEach, expect, it, vi } from "vitest"
import { WordCraftEditorHost } from "../wordcraft-editor-host"
const mocks = vi.hoisted(() => ({ open: vi.fn(), connect: vi.fn(), sdk: vi.fn(), ready: vi.fn(), draft: vi.fn(), submit: vi.fn(), stop: vi.fn(), close: vi.fn() }))
vi.mock("@/lib/mooring/wordcraft-host-session", () => ({ openWordCraftSession: mocks.open, connectWordCraftHost: mocks.connect, loadWordCraftReceiptSDK: mocks.sdk, WORDCRAFT_FRAME_BYTES: 262144, WORDCRAFT_BOOTSTRAP_BYTES: 2097152 }))
vi.mock("@/lib/editor-lifecycle/use-leave-guard", () => ({ useLeaveGuard: vi.fn() }))
const digest = "b".repeat(64)
const windowMock = { postMessage: vi.fn() }
const credentialless = Object.getOwnPropertyDescriptor(HTMLIFrameElement.prototype, "credentialless")
const contentWindow = Object.getOwnPropertyDescriptor(HTMLIFrameElement.prototype, "contentWindow")
const ports: MessagePort[] = []
beforeEach(() => {
  vi.clearAllMocks()
  Object.defineProperty(HTMLIFrameElement.prototype, "credentialless", { configurable: true, get() { return (this as HTMLIFrameElement).hasAttribute("credentialless") } })
  Object.defineProperty(HTMLIFrameElement.prototype, "contentWindow", { configurable: true, get: () => windowMock })
  mocks.open.mockResolvedValue({ editor: { url: `https://editor.example.test/b/${digest}/index.html`, bundle_digest: digest }, sdk_prefix: `/mooring-sdk/generated/${"a".repeat(64)}` })
  mocks.sdk.mockResolvedValue({})
  mocks.connect.mockReturnValue({ ready: mocks.ready, draft: mocks.draft, submit: mocks.submit, stop: mocks.stop, close: mocks.close })
})
afterEach(() => {
  cleanup(); ports.splice(0).forEach(port => port.close())
  if (credentialless) Object.defineProperty(HTMLIFrameElement.prototype, "credentialless", credentialless)
  else delete (HTMLIFrameElement.prototype as { credentialless?: unknown }).credentialless
  if (contentWindow) Object.defineProperty(HTMLIFrameElement.prototype, "contentWindow", contentWindow)
})
async function open() {
  const view = render(<WordCraftEditorHost workspaceId="workspace" pageId="page" name="Original title" />)
  await waitFor(() => expect(document.querySelector("iframe")).not.toBeNull())
  const frame = document.querySelector("iframe")!
  expect(new URL(frame.src).searchParams.get("mooring")).toBe("1")
  expect(new URL(frame.src).searchParams.get("webgl")).toBe("1")
  await act(async () => { frame.dispatchEvent(new Event("load")); window.dispatchEvent(new MessageEvent("message", { source: windowMock as unknown as Window, origin: "null", data: { type: "wordcraft:hello", protocol: 1, bundle: digest } })) })
  expect(windowMock.postMessage).toHaveBeenCalledOnce()
  const port = windowMock.postMessage.mock.calls[0][2][0] as MessagePort
  ports.push(port)
  const send = async (message: object) => { await act(async () => { port.postMessage(JSON.stringify({ protocol: 1, ...message })); await new Promise(resolve => setTimeout(resolve, 10)) }) }
  return { frame, send, view, port }
}
it("rejects a child Saved assertion and hides the editor without destroying its producer", async () => {
  const { frame, send } = await open()
  await send({ type: "wordcraft:saved", receipt: { issuer: "crewship" } })
  expect(mocks.stop).toHaveBeenCalledWith("invalid_editor_sequence")
  expect(frame.hidden).toBe(true)
  expect(frame.isConnected).toBe(true)
  expect(screen.queryByText("Saved")).toBeNull()
})
it("preserves the same frame and original connection when Page metadata is renamed", async () => {
  const { frame, view } = await open()
  view.rerender(<WordCraftEditorHost workspaceId="workspace" pageId="page" name="Renamed title" />)
  expect(document.querySelector("iframe")).toBe(frame)
  expect(mocks.open).toHaveBeenCalledTimes(1)
  expect(mocks.close).not.toHaveBeenCalled()
})
it("forwards only scoped submit data, not child-selected network methods", async () => {
  const { frame, send } = await open()
  const raw = '  {"type":"submit_changes","request_id":"original"} \n'
  await send({ type: "wordcraft:submit", raw })
  expect(mocks.submit).toHaveBeenCalledWith(raw)
  await send({ type: "wordcraft:submit", raw, url: "https://attacker.example" })
  expect(mocks.submit).toHaveBeenCalledTimes(1)
  expect(frame.hidden).toBe(true)
})

it("refuses duplicate private-envelope keys before accepting any child command", async () => {
  const { frame, port } = await open()
  await act(async () => {
    port.postMessage('{"protocol":1,"type":"wordcraft:ready","type":"wordcraft:ready"}')
    await new Promise(resolve => setTimeout(resolve, 10))
  })
  expect(mocks.ready).not.toHaveBeenCalled()
  expect(mocks.stop).toHaveBeenCalledWith("invalid_editor_frame")
  expect(frame.hidden).toBe(true)
})

it("rejects oversized child strings before making a second UTF-8 allocation", async () => {
  const { frame, port } = await open()
  const oversized = "x".repeat(262145)
  const encoder = vi.spyOn(TextEncoder.prototype, "encode")
  try {
    await act(async () => { port.postMessage(oversized); await new Promise(resolve => setTimeout(resolve, 10)) })
    expect(encoder.mock.calls.some(([raw]) => raw === oversized)).toBe(false)
    expect(mocks.stop).toHaveBeenCalledWith("invalid_editor_frame")
    expect(frame.hidden).toBe(true)
  } finally { encoder.mockRestore() }
})

// These private-port tests qualify local explanatory UX only; the state callback
// is a fixture and does not prove a real Rust or SQL receipt.
it.each(["saved", "pending"])("shows a history refusal while keeping %s and the same editor", async state => {
  const { frame, send } = await open()
  const callbacks = mocks.connect.mock.calls[0][5]
  await act(async () => { callbacks.state(state) })
  const priorStatus = screen.getByRole("status").textContent
  await send({ type: "wordcraft:status", state: "history-refused", reason: "unsupported_delete_undo" })
  expect(screen.getByRole("alert")).toHaveTextContent("Undo or redo is unavailable for deletions or changes that overlap another person's deletion. Your document has not been changed.")
  expect(screen.getByRole("status").textContent).toBe(priorStatus)
  expect(frame.hidden).toBe(false)
  expect(frame.isConnected).toBe(true)
  expect(mocks.stop).not.toHaveBeenCalled()
  expect(mocks.submit).not.toHaveBeenCalled()
  expect(mocks.draft).not.toHaveBeenCalled()
  expect(mocks.ready).not.toHaveBeenCalled()
  await send({ type: "wordcraft:status", state: "draft", reason: "local-original" })
  expect(screen.queryByRole("alert")).toBeNull()
  expect(mocks.draft).toHaveBeenCalledOnce()
})
it("rejects an unrecognized history refusal without displaying its untrusted reason", async () => {
  const { frame, send } = await open()
  await send({ type: "wordcraft:status", state: "history-refused", reason: "untrusted-child-text" })
  expect(mocks.stop).toHaveBeenCalledWith("invalid_editor_sequence")
  expect(frame.hidden).toBe(true)
  expect(screen.queryByText("untrusted-child-text")).toBeNull()
})
