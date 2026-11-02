/** Synthetic private-port lifecycle tests; no Rust/SQL/WordApp acceptance. */
import React from "react"
import { act, cleanup, render, screen, waitFor } from "@testing-library/react"
import { afterEach, beforeEach, expect, it, vi } from "vitest"
import { WordCraftEditorHost } from "../wordcraft-editor-host"
const identity = vi.hoisted(() => ({ id: "human-one", status: "authenticated" }))
vi.mock("@/hooks/use-auth", () => ({ useSessionSafe: () => ({ data: { user: { id: identity.id } }, status: identity.status }) }))
const mocks = vi.hoisted(() => ({ open: vi.fn(), connect: vi.fn(), sdk: vi.fn(), ready: vi.fn(), draft: vi.fn(), submit: vi.fn(), stop: vi.fn(), close: vi.fn() }))
vi.mock("@/lib/mooring/wordcraft-host-session", () => ({ openWordCraftSession: mocks.open, connectWordCraftHost: mocks.connect, loadWordCraftReceiptSDK: mocks.sdk, WORDCRAFT_FRAME_BYTES: 262144, WORDCRAFT_BOOTSTRAP_BYTES: 2097152 }))
vi.mock("@/lib/editor-lifecycle/use-leave-guard", () => ({ useLeaveGuard: vi.fn() }))
const drafts = vi.hoisted(() => ({ store: new Map<string, string>() }))
vi.mock("@/lib/artifact-drafts", () => ({
  artifactDraftKey: (kind: string, human: string | null, workspace: string, target: string) => human ? JSON.stringify([human, kind, workspace, target]) : null,
  writeArtifactDraft: async (key: string, text: string) => { drafts.store.set(key, text) },
  loadArtifactDraft: async (key: string) => drafts.store.get(key) ?? "",
  clearArtifactDraft: async (key: string) => { drafts.store.delete(key) },
  artifactDraftRevision: () => 1,
}))
const digest = "b".repeat(64)
const windowMock = { postMessage: vi.fn() }
const credentialless = Object.getOwnPropertyDescriptor(HTMLIFrameElement.prototype, "credentialless")
const contentWindow = Object.getOwnPropertyDescriptor(HTMLIFrameElement.prototype, "contentWindow")
const ports: MessagePort[] = []
beforeEach(() => {
  vi.clearAllMocks(); drafts.store.clear()
  identity.id = "human-one"; identity.status = "authenticated"
  Object.defineProperty(HTMLIFrameElement.prototype, "credentialless", { configurable: true, get() { return (this as HTMLIFrameElement).hasAttribute("credentialless") } })
  Object.defineProperty(HTMLIFrameElement.prototype, "contentWindow", { configurable: true, get: () => windowMock })
  mocks.open.mockResolvedValue({ editor: { url: `https://editor.example.test/b/${digest}/index.html`, bundle_digest: digest }, sdk_prefix: `/mooring-sdk/generated/${"a".repeat(64)}` })
  mocks.sdk.mockResolvedValue({})
  mocks.connect.mockReturnValue({ pendingOriginals: () => ["{\"type\":\"submit_changes\",\"request_id\":\"r1\"}"], ready: mocks.ready, draft: mocks.draft, submit: mocks.submit, stop: mocks.stop, close: mocks.close })
})
afterEach(() => {
  cleanup(); ports.splice(0).forEach(port => port.close())
  if (credentialless) Object.defineProperty(HTMLIFrameElement.prototype, "credentialless", credentialless)
  else delete (HTMLIFrameElement.prototype as { credentialless?: unknown }).credentialless
  if (contentWindow) Object.defineProperty(HTMLIFrameElement.prototype, "contentWindow", contentWindow)
})
async function open(name = "Original title") {
  const view = render(<WordCraftEditorHost workspaceId="workspace" pageId="page" name={name} />)
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
it("bounds display metadata separately from unchanged host checkpoint and origin", async () => {
  const { port } = await open("Name\n" + "😀".repeat(200))
  const messages: string[] = []
  port.onmessage = event => messages.push(event.data)
  const proof = { joined_json: "joined original", checkpoint_json: "checkpoint original", assigned_origin: "17", access: "writer" }
  await act(async () => { mocks.connect.mock.calls[0][5].bootstrap(proof); await new Promise(resolve => setTimeout(resolve, 10)) })
  expect(messages).toHaveLength(1)
  expect(JSON.parse(messages[0])).toEqual({ protocol: 1, type: "wordcraft:bootstrap", ...proof, display_name: "Name " + "😀".repeat(123) })
})
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


it("ends the old human socket and producer before reusing an already warm native host for another human", async () => {
  const { frame, view } = await open()
  identity.id = "human-two"
  view.rerender(<WordCraftEditorHost workspaceId="workspace" pageId="page" name="Original title" />)
  await waitFor(() => expect(mocks.open).toHaveBeenCalledTimes(2))
  expect(mocks.close).toHaveBeenCalledOnce()
  expect(frame.isConnected).toBe(false)
  expect(document.querySelector("iframe")).not.toBe(frame)
})

it("stops and retains the original dirty frame on metadata denial without reopening it on recovery", async () => {
  const { frame, view } = await open()
  const callbacks = mocks.connect.mock.calls[0][5]
  await act(async () => { callbacks.state("draft") })
  view.rerender(<WordCraftEditorHost workspaceId="workspace" pageId="page" unavailable />)
  expect(mocks.stop).toHaveBeenCalledWith("page_unavailable")
  expect(mocks.close).not.toHaveBeenCalled()
  expect(frame.hidden).toBe(true)
  expect(frame.isConnected).toBe(true)
  expect(screen.getByRole("status")).toHaveTextContent("Not saved · local draft retained")
  await act(async () => { callbacks.hostFrame("old-authority-frame") })
  view.rerender(<WordCraftEditorHost workspaceId="workspace" pageId="page" />)
  expect(mocks.open).toHaveBeenCalledTimes(1)
  expect(document.querySelector("iframe")).toBe(frame)
  expect(frame.hidden).toBe(true)
  expect(screen.queryByText("Saved")).toBeNull()
})
it("on storage_full keeps the editor visible, saves the submitted changes as a local draft and offers it again on reopen", async () => {
  const first = await open()
  await act(async () => { mocks.connect.mock.calls[0][5].state("draft"); await new Promise(resolve => setTimeout(resolve, 10)) })
  await act(async () => { mocks.connect.mock.calls[0][5].stopped("storage_full"); await new Promise(resolve => setTimeout(resolve, 10)) })
  expect(screen.getAllByRole("alert")[0]).toHaveTextContent("Kapacita úložiště neumožňuje další uložení")
  expect(screen.getByRole("status")).toHaveTextContent("Not saved · local draft retained")
  // The stopped editor stays readable so its content can be copied.
  expect(first.frame.hidden).toBe(false)
  expect(first.frame.isConnected).toBe(true)
  const stored = [...drafts.store.entries()]
  expect(stored).toHaveLength(1)
  expect(JSON.parse(stored[0][0])[1]).toBe("wordcraft-pending")
  expect(JSON.parse(stored[0][1])).toMatchObject({ format: "crewship-wordcraft-draft-v1", workspace_id: "workspace", page_id: "page", originals: ['{"type":"submit_changes","request_id":"r1"}'] })
  expect(screen.getByRole("group", { name: "Místní koncept" })).toBeInTheDocument()
  // Reopen: a fresh mount finds the stored draft and offers it again.
  cleanup(); ports.splice(0).forEach(port => port.close()); windowMock.postMessage.mockClear()
  render(<WordCraftEditorHost workspaceId="workspace" pageId="page" name="Original title" />)
  await waitFor(() => expect(screen.getByRole("group", { name: "Místní koncept" })).toBeInTheDocument())
  expect(screen.getByText("Stáhnout koncept")).toBeInTheDocument()
})
it("other stops still hide the editor and save no draft", async () => {
  const t = await open()
  await act(async () => { mocks.connect.mock.calls[0][5].stopped("connection_unavailable"); await new Promise(resolve => setTimeout(resolve, 10)) })
  expect(t.frame.hidden).toBe(true)
  expect(drafts.store.size).toBe(0)
})
const draftText = (originals: string[]) => JSON.stringify({ format: "crewship-wordcraft-draft-v1", workspace_id: "workspace", page_id: "page", originals })
it("G4 B3: a recovered draft of one person is not offered to another person after the identity changes", async () => {
  drafts.store.set(JSON.stringify(["human-one", "wordcraft-pending", "workspace", "page"]), draftText(["secret of A"]))
  const view = render(<WordCraftEditorHost workspaceId="workspace" pageId="page" name="Original title" />)
  await waitFor(() => expect(screen.getByRole("group", { name: "Místní koncept" })).toBeInTheDocument())
  identity.id = "human-two"
  view.rerender(<WordCraftEditorHost workspaceId="workspace" pageId="page" name="Original title" />)
  await waitFor(() => expect(screen.queryByRole("group", { name: "Místní koncept" })).toBeNull())
  expect(screen.queryByText("Stáhnout koncept")).toBeNull()
})
it("G4 B3: a recovered draft is no longer offered after sign-out", async () => {
  drafts.store.set(JSON.stringify(["human-one", "wordcraft-pending", "workspace", "page"]), draftText(["secret of A"]))
  const view = render(<WordCraftEditorHost workspaceId="workspace" pageId="page" name="Original title" />)
  await waitFor(() => expect(screen.getByRole("group", { name: "Místní koncept" })).toBeInTheDocument())
  identity.status = "unauthenticated"
  view.rerender(<WordCraftEditorHost workspaceId="workspace" pageId="page" name="Original title" />)
  await waitFor(() => expect(screen.queryByRole("group", { name: "Místní koncept" })).toBeNull())
})
it("G4 B3: a recovered draft of one Page is not offered on another Page", async () => {
  drafts.store.set(JSON.stringify(["human-one", "wordcraft-pending", "workspace", "page"]), draftText(["draft of page"]))
  const view = render(<WordCraftEditorHost workspaceId="workspace" pageId="page" name="Original title" />)
  await waitFor(() => expect(screen.getByRole("group", { name: "Místní koncept" })).toBeInTheDocument())
  view.rerender(<WordCraftEditorHost workspaceId="workspace" pageId="other-page" name="Original title" />)
  await waitFor(() => expect(screen.queryByRole("group", { name: "Místní koncept" })).toBeNull())
})
