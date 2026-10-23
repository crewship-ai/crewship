/** Synthetic host/channel tests. Real editor acceptance runs against a deployment. */
import React from "react"
import { act, cleanup, render, screen, waitFor } from "@testing-library/react"
import { afterEach, beforeEach, expect, it, vi } from "vitest"
import { MooringEditorHost } from "../mooring-editor-host"
const requests = vi.hoisted(() => vi.fn())
const realtime = vi.hoisted(() => ({ handlers: new Map<string, (event: { payload: unknown }) => void>() }))
vi.mock("@/lib/api-fetch", () => ({ apiFetch: requests, tryRefresh: vi.fn(), broadcastSessionExpired: vi.fn() }))
vi.mock("@/hooks/use-realtime", async () => {
  const React = await import("react")
  return { useRealtimeChannelSafe: vi.fn(), useRealtimeEventSafe: (type: string, fn: (event: { payload: unknown }) => void) => { const ref = React.useRef(fn); ref.current = fn; React.useEffect(() => { realtime.handlers.set(type, event => ref.current(event)); return () => { realtime.handlers.delete(type) } }, [type]) }, useRealtimeStatusSafe: () => "connected" }
})
const digest = "a".repeat(64)
let rev = 1, snapshot: unknown = { any: "initial" }, answer: () => Promise<Response>
const windowMock = { postMessage: vi.fn() }
const credentialless = Object.getOwnPropertyDescriptor(HTMLIFrameElement.prototype, "credentialless")
const contentWindow = Object.getOwnPropertyDescriptor(HTMLIFrameElement.prototype, "contentWindow")
const ports: MessagePort[] = []
beforeEach(() => {
  rev = 1; snapshot = { any: "initial" }; windowMock.postMessage.mockClear()
  Object.defineProperty(HTMLIFrameElement.prototype, "credentialless", { configurable: true, get() { return (this as HTMLIFrameElement).hasAttribute("credentialless") } })
  Object.defineProperty(HTMLIFrameElement.prototype, "contentWindow", { configurable: true, get: () => windowMock })
  answer = async () => Response.json({ rev: ++rev, outcome: "committed", issued_by: "crewship", durability: "node" })
  requests.mockImplementation(async (_url: string, init?: RequestInit) => init?.method === "POST" ? answer() : Response.json({ app_id: "arbitrary-third-party-app", state: { rev, generation: 1, snapshot }, editor: { url: `https://editor.example.test/b/${digest}/index.html`, bundle_digest: digest } }))
})
afterEach(() => {
  cleanup(); ports.splice(0).forEach(port => port.close()); requests.mockReset()
  if (credentialless) Object.defineProperty(HTMLIFrameElement.prototype, "credentialless", credentialless)
  else delete (HTMLIFrameElement.prototype as { credentialless?: unknown }).credentialless
  if (contentWindow) Object.defineProperty(HTMLIFrameElement.prototype, "contentWindow", contentWindow)
})
async function open() {
  const dirty = vi.fn()
  render(<MooringEditorHost workspaceId="ws" pageId="page" name="Third-party application" onDirtyChange={dirty} />)
  await waitFor(() => expect(document.querySelector("iframe")).not.toBeNull())
  const frame = document.querySelector("iframe")!
  await act(async () => { frame.dispatchEvent(new Event("load")); window.dispatchEvent(new MessageEvent("message", { source: windowMock as unknown as Window, origin: "null", data: { type: "mooring:hello", protocol: 1, bundle: digest } })) })
  expect(windowMock.postMessage).toHaveBeenCalledOnce()
  const port = windowMock.postMessage.mock.calls[0][2][0] as MessagePort
  ports.push(port)
  const messages: Record<string, unknown>[] = []
  port.onmessage = event => messages.push(JSON.parse(event.data))
  await waitFor(() => expect(messages.some(message => message.type === "snapshot")).toBe(true))
  const send = async (message: object) => act(async () => { port.postMessage(JSON.stringify({ protocol: 1, ...message })) })
  return { frame, port, messages, send, dirty }
}
it("loads a declaratively registered arbitrary app and pushes confirmed revisions through the generic channel", async () => {
  const t = await open()
  expect(t.frame.getAttribute("sandbox")).toBe("allow-scripts")
  expect(t.frame.hasAttribute("credentialless")).toBe(true)
  snapshot = { any: "from agent" }; rev = 2
  await act(async () => realtime.handlers.get("mooring.committed")?.({ payload: { artifact_id: "page", rev: 2, generation: 1 } }))
  await waitFor(() => expect(t.messages.at(-2)).toMatchObject({ type: "snapshot", rev: 2, snapshot }))
  expect(document.querySelector("iframe")).toBe(t.frame)
})
it("keeps an unsubmitted draft unsaved during pushes and only acknowledges a durable receipt", async () => {
  const t = await open()
  await t.send({ type: "draft", dirty: true })
  await waitFor(() => expect(screen.getByRole("status")).toHaveTextContent("Not saved"))
  let release!: (value: Response) => void
  answer = () => new Promise(resolve => { release = resolve })
  await t.send({ type: "command", seq: 1, base_rev: 1, generation: 1, command: "custom", arguments: { any: "mine" } })
  await waitFor(() => expect(release).toBeDefined())
  expect(t.messages.some(message => message.type === "ack")).toBe(false)
  rev = 2
  await act(async () => release(Response.json({ rev, outcome: "committed", issued_by: "crewship", durability: "node" })))
  await waitFor(() => expect(t.messages.some(message => message.type === "ack" && message.rev === 2)).toBe(true))
  // The receipt cannot clear a draft that was never submitted.
  expect(screen.getByRole("status")).toHaveTextContent("Not saved")
})
it("does not treat a non-durable response as saved and retains exact retry bytes", async () => {
  const t = await open(); answer = async () => Response.json({ rev: 2, outcome: "committed", issued_by: "crewship", durability: "memory" })
  await t.send({ type: "command", seq: 1, base_rev: 1, generation: 1, command: "custom", arguments: { any: "mine" } })
  await waitFor(() => expect(screen.getByRole("alert")).toHaveTextContent("durable receipt"))
  expect(t.messages.some(message => message.type === "ack")).toBe(false)
  await act(async () => { window.dispatchEvent(new Event("online")) })
  await waitFor(() => expect(requests.mock.calls.filter(([, init]) => init?.method === "POST")).toHaveLength(2))
  const writes = requests.mock.calls.filter(([, init]) => init?.method === "POST")
  expect(writes[0][1].body).toBe(writes[1][1].body)
})
it("removes the frame when authenticated state is revoked", async () => {
  await open()
  requests.mockImplementation(async () => Response.json({ error: "denied" }, { status: 403 }))
  await act(async () => realtime.handlers.get("mooring.committed")?.({ payload: { artifact_id: "page", rev: 2, generation: 1 } }))
  await waitFor(() => expect(document.querySelector("iframe")).toBeNull())
  expect(screen.getByRole("alert")).toHaveTextContent("Access")
})

it.each([2, 3])("joins an in-flight push read and drains the newer revision %i before acknowledging the commit", async head => {
  const t = await open()
  let releaseCommit!: (response: Response) => void
  let releaseRead!: (response: Response) => void
  let reads = 0
  const state = (version: number) => Response.json({ app_id: "arbitrary-third-party-app", state: { rev: version, generation: 1, snapshot: { any: version === 1 ? "initial" : "confirmed" } }, editor: { url: `https://editor.example.test/b/${digest}/index.html`, bundle_digest: digest } })
  requests.mockImplementation(async (_url: string, init?: RequestInit) => {
    if (init?.method === "POST") return new Promise<Response>(resolve => { releaseCommit = resolve })
    if (++reads === 1) return new Promise<Response>(resolve => { releaseRead = resolve })
    return state(head)
  })
  await t.send({ type: "command", seq: 1, base_rev: 1, generation: 1, command: "custom", arguments: { any: "mine" } })
  await waitFor(() => expect(releaseCommit).toBeDefined())
  await act(async () => realtime.handlers.get("mooring.committed")?.({ payload: { artifact_id: "page", rev: head, generation: 1 } }))
  await waitFor(() => expect(releaseRead).toBeDefined())
  // The durable POST response overtakes a GET which captured the older state.
  // A receipt must join that read and its queued fresh read, rather than
  // returning early and abandoning the acknowledgement as "offline".
  await act(async () => releaseCommit(Response.json({ rev: 2, outcome: "committed", issued_by: "crewship", durability: "node" })))
  expect(t.messages.some(message => message.type === "ack")).toBe(false)
  await act(async () => releaseRead(state(1)))
  await waitFor(() => expect(t.messages.some(message => message.type === "ack" && message.rev === 2)).toBe(true))
  const ack = t.messages.findIndex(message => message.type === "ack")
  expect(t.messages.slice(0, ack)).toContainEqual(expect.objectContaining({ type: "snapshot", rev: head }))
  expect(t.messages.filter(message => message.type === "offline")).toHaveLength(0)
  expect(screen.getByRole("status")).toHaveTextContent("Saved")
  expect(requests.mock.calls.filter(([, init]) => init?.method === "POST")).toHaveLength(1)
})

it("retains the exact mutation when the joined snapshot read fails, then confirms on reconnect", async () => {
  const t = await open()
  const writes: string[] = []
  let failRead = true
  requests.mockImplementation(async (_url: string, init?: RequestInit) => {
    if (init?.method === "POST") {
      writes.push(String(init.body))
      return Response.json({ rev: 2, outcome: "committed", issued_by: "crewship", durability: "node" })
    }
    if (failRead) throw new Error("Snapshot network failure")
    return Response.json({ app_id: "arbitrary-third-party-app", state: { rev: 2, generation: 1, snapshot: { any: "confirmed" } }, editor: { url: `https://editor.example.test/b/${digest}/index.html`, bundle_digest: digest } })
  })
  await t.send({ type: "command", seq: 1, base_rev: 1, generation: 1, command: "custom", arguments: { any: "mine" } })
  await waitFor(() => expect(screen.getByRole("status")).toHaveTextContent("Offline"))
  expect(t.messages.some(message => message.type === "ack")).toBe(false)
  failRead = false
  await act(async () => { window.dispatchEvent(new Event("online")) })
  await waitFor(() => expect(t.messages.some(message => message.type === "ack" && message.rev === 2)).toBe(true))
  expect(writes).toHaveLength(2)
  expect(writes[1]).toBe(writes[0])
  expect(screen.getByRole("status")).toHaveTextContent("Saved")
})
