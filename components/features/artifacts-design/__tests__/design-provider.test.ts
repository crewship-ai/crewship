import { afterEach, describe, expect, it, vi } from "vitest"
import * as Y from "yjs"
import * as encoding from "lib0/encoding"
import * as decoding from "lib0/decoding"
import * as sync from "y-protocols/sync"
import { DesignProvider } from "../design-provider"
import { designNodes, removeDesignNode, upsertDesignNode } from "../design-document"

class Socket {
 static OPEN = 1
 static instances: Socket[] = []
 readyState = 1
 binaryType = ""
 onopen: (() => void) | null = null
 onmessage: ((event: { data: ArrayBuffer }) => void) | null = null
 onerror: (() => void) | null = null
 onclose: ((event: { code: number }) => void) | null = null
 sent: Uint8Array[] = []
 constructor(readonly url: string) { Socket.instances.push(this) }
 send(bytes: ArrayBuffer) { this.sent.push(new Uint8Array(bytes)) }
 close(code = 1000) { this.onclose?.({ code }) }
 /** `since`: what the frame leaves out, as the server's update or step 2 diff does. */
 receive(doc: Y.Doc, step = 1, since?: Y.Doc) {
  const encoder = encoding.createEncoder()
  encoding.writeVarUint(encoder, 0)
  if (step === 1) sync.writeSyncStep1(encoder, doc)
  else if (step === 2) sync.writeSyncStep2(encoder, doc, since && Y.encodeStateVector(since))
  else sync.writeUpdate(encoder, Y.encodeStateAsUpdate(doc, since && Y.encodeStateVector(since)))
  const bytes = encoding.toUint8Array(encoder)
  this.onmessage?.({ data: bytes.buffer.slice(bytes.byteOffset, bytes.byteOffset + bytes.byteLength) as ArrayBuffer })
 }
}
const active: DesignProvider[] = []
afterEach(() => { active.splice(0).forEach(provider => provider.destroy()); vi.unstubAllGlobals(); vi.useRealTimers(); Socket.instances = [] })
function setup(editable: boolean, url = "ws://localhost/design/ws", start: { doc?: Y.Doc; baseline?: Uint8Array; unconfirmed?: boolean; prepare?: () => Promise<"ready"> } = {}) {
 vi.stubGlobal("WebSocket", Socket)
 const doc = start.doc ?? new Y.Doc(), status = vi.fn(), dirty = vi.fn(), pending = vi.fn(), conflicts = vi.fn()
 const provider = new DesignProvider(doc, url, editable, { name: "Test", color: "#2563eb" }, status, dirty, start.prepare, { baseline: start.baseline, unconfirmed: start.unconfirmed, pending, conflicts })
 active.push(provider)
 const socket = Socket.instances.at(-1)
 socket?.onopen?.()
 return { doc, provider, socket: socket!, status, dirty, pending, conflicts }
}
function documentFrames(socket: Socket) {
 return socket.sent.flatMap(bytes => { const decoder = decoding.createDecoder(bytes); return decoding.readVarUint(decoder) === 0 ? [decoding.readVarUint(decoder)] : [] })
}
describe("Design standard sync provider", () => {
 it("keeps the immutable Page target during an offline reconnect", () => {
  vi.useFakeTimers()
  const url = "ws://localhost/api/v1/pages/@old-private-page/design/ws?workspace_id=workspace"
  const { socket } = setup(true, url)
  socket.close(1006)
  vi.advanceTimersByTime(2000)
  expect(Socket.instances).toHaveLength(2)
  expect(Socket.instances[1].url).toBe(url)
 })
 it("stops reconnecting when the server rejected the canvas update", () => {
  vi.useFakeTimers()
  const { socket, status } = setup(true)
  socket.close(4400)
  vi.advanceTimersByTime(10_000)
  expect(Socket.instances).toHaveLength(1)
  expect(status).toHaveBeenLastCalledWith("rejected")
 })

 it("stops reconnecting when a frame exceeded the server read limit", () => {
  vi.useFakeTimers()
  const { socket, status } = setup(true)
  socket.close(1009)
  vi.advanceTimersByTime(10_000)
  expect(Socket.instances).toHaveLength(1)
  expect(status).toHaveBeenLastCalledWith("rejected")
 })
 it("read-only clients receive updates and awareness without sending document writes", () => {
  const { doc, socket, dirty } = setup(false)
  const server = new Y.Doc(); server.getMap("nodes").set("hello", "world")
  socket.receive(server, 1)
  socket.receive(server, 2)
  expect(doc.getMap("nodes").get("hello")).toBe("world")
  doc.getMap("nodes").set("forbidden", "local")
  expect(documentFrames(socket)).toEqual([sync.messageYjsSyncStep1])
  expect(dirty).not.toHaveBeenCalled()
 })
 it("waits for the server sync response before allowing an HTTP snapshot", async () => {
  const { provider, socket } = setup(true)
  const server = new Y.Doc()
  socket.receive(server, 2)
  let acknowledged = false
  const barrier = provider.synchronize().then(() => { acknowledged = true })
  await Promise.resolve()
  socket.receive(server, 3)
  await Promise.resolve()
  expect(acknowledged).toBe(false)
  expect(documentFrames(socket).at(-1)).toBe(sync.messageYjsSyncStep1)
  socket.receive(server, 2)
  await barrier
  expect(acknowledged).toBe(true)
 })
 it("serializes overlapping snapshot barriers", async () => {
  const { provider, socket } = setup(true)
  const server = new Y.Doc()
  socket.receive(server, 2)
  const first = provider.synchronize()
  const second = provider.synchronize()
  await new Promise(resolve => setTimeout(resolve, 0))
  expect(documentFrames(socket).filter(type => type === sync.messageYjsSyncStep1)).toHaveLength(2)
  socket.receive(server, 2)
  await first
  await new Promise(resolve => setTimeout(resolve, 0))
  expect(documentFrames(socket).filter(type => type === sync.messageYjsSyncStep1)).toHaveLength(3)
  let secondDone = false
  void second.then(() => { secondDone = true })
  await Promise.resolve()
  expect(secondDone).toBe(false)
  socket.receive(server, 2)
  await second
  expect(secondDone).toBe(true)
 })
 it("editors send standard updates and stop after permission revocation", async () => {
  const { doc, socket, provider, status } = setup(true)
  doc.getMap("nodes").set("edit", "one")
  await new Promise(resolve => setTimeout(resolve, 100))
  expect(documentFrames(socket)).toContain(sync.messageYjsUpdate)
  socket.close(1008)
  expect(status).toHaveBeenLastCalledWith("denied")
  const count = socket.sent.length
  doc.getMap("nodes").set("edit", "two")
  expect(socket.sent).toHaveLength(count)
  expect(provider.ready).toBe(false)
 })
})

describe("Design authenticated transport renewal", () => {
 async function prepared(outcomes: ("ready" | "denied" | "retry")[] = ["ready"]) {
  vi.stubGlobal("WebSocket", Socket)
  const doc = new Y.Doc(), status = vi.fn(), prepare = vi.fn(async () => outcomes.shift() ?? "ready" as const)
  const provider = new DesignProvider(doc, "ws://localhost/design/ws?authority_revision=1", true, { name: "Owner", color: "#2563eb" }, status, vi.fn(), prepare)
  active.push(provider)
  await Promise.resolve(); await Promise.resolve()
  Socket.instances.at(-1)?.onopen?.()
  return { doc, provider, status, prepare, socket: Socket.instances.at(-1)! }
 }
 it("refreshes before first admission and rotates only the socket while keeping unsaved Yjs state", async () => {
  vi.useFakeTimers()
  const { doc, provider, prepare, socket, status } = await prepared()
  expect(prepare).toHaveBeenCalledTimes(1)
  doc.getMap("nodes").set("pending", "Never discard this draft")
  await vi.advanceTimersByTimeAsync(5 * 60 * 1000)
  expect(prepare).toHaveBeenCalledTimes(2)
  expect(Socket.instances).toHaveLength(2)
  const replacement = Socket.instances[1]; replacement.onopen?.()
  expect(provider.doc).toBe(doc)
  expect(doc.getMap("nodes").get("pending")).toBe("Never discard this draft")
  socket.close(1008)
  expect(status).toHaveBeenLastCalledWith("connected")
 })
 it("recovers an expired socket only after fresh admission succeeds", async () => {
  const { socket, prepare, doc, status } = await prepared()
  doc.getMap("nodes").set("pending", "Same authority draft")
  socket.close(1008)
  await Promise.resolve(); await Promise.resolve()
  expect(prepare).toHaveBeenCalledTimes(2)
  expect(Socket.instances).toHaveLength(2)
  Socket.instances[1].onopen?.()
  expect(status).toHaveBeenLastCalledWith("connected")
  expect(doc.getMap("nodes").get("pending")).toBe("Same authority draft")
 })
 it("does not reconnect after fresh authority denies or changes", async () => {
  const { socket, prepare, provider, status } = await prepared(["ready", "denied"])
  socket.close(1008)
  await Promise.resolve(); await Promise.resolve()
  expect(prepare).toHaveBeenCalledTimes(2)
  expect(Socket.instances).toHaveLength(1)
  expect(status).toHaveBeenLastCalledWith("denied")
  expect(provider.ready).toBe(false)
 })
 it("rejects an interrupted snapshot barrier instead of acknowledging or replaying it", async () => {
  const { socket, provider, doc } = await prepared()
  socket.receive(new Y.Doc(), 2)
  const saving = provider.synchronize()
  await Promise.resolve()
  socket.close(1008)
  await expect(saving).rejects.toThrow("Save again after reconnecting")
  expect(provider.doc).toBe(doc)
 })
 it("does not open a socket after unmount during credential refresh", async () => {
  vi.stubGlobal("WebSocket", Socket)
  let finish!: (value: "ready") => void
  const prepare = () => new Promise<"ready">(resolve => { finish = resolve })
  const provider = new DesignProvider(new Y.Doc(), "ws://localhost/design/ws", true, {name:"Owner",color:"#2563eb"}, vi.fn(), vi.fn(), prepare)
  active.push(provider);provider.destroy();finish("ready")
  await Promise.resolve();await Promise.resolve()
  expect(Socket.instances).toHaveLength(0)
 })
})

describe("Design provider work the server has not confirmed", () => {
 const label = { id: "label", type: "text" as const, name: "Title", text: "Base", x: 0, y: 0, width: 200, height: 40, fill: "#111111" }
 /** Connected and fully synced with the server's doc: Hello answered, first reply in. */
 function live() {
  const ctx = setup(true)
  const server = new Y.Doc()
  upsertDesignNode(server, label)
  ctx.socket.receive(server, 1)
  ctx.socket.receive(server, 2)
  vi.useFakeTimers()
  return { ...ctx, server }
 }
 function offline() {
  const ctx = live()
  ctx.socket.close(1006)
  return ctx
 }
 const reconnect = () => {
  vi.advanceTimersByTime(2000)
  const socket = Socket.instances.at(-1)!
  socket.onopen?.()
  return socket
 }
 const found = (conflicts: ReturnType<typeof vi.fn>) => conflicts.mock.calls.flatMap(call => call[0] as { id: string; mine: string; theirs: string; kept: string; left?: string }[])
 it("reports the local text the merge dropped, with the other text beside it", () => {
  const { doc, server, conflicts } = offline()
  upsertDesignNode(doc, { id: "label", text: "Mine" })
  upsertDesignNode(server, { id: "label", text: "Theirs" })
  const socket = reconnect()
  socket.receive(server, 1)
  socket.receive(server, 2)
  const merged = designNodes(doc)[0].text
  expect(found(conflicts)).toMatchObject([{ id: "label", mine: "Mine", theirs: "Theirs", kept: merged === "Mine" ? "mine" : "theirs", left: merged }])
 })
 it("reports no conflict when only this person changed the layer", () => {
  const { doc, server, conflicts } = offline()
  upsertDesignNode(doc, { id: "label", text: "Mine" })
  const socket = reconnect()
  socket.receive(server, 1)
  socket.receive(server, 2)
  expect(conflicts).not.toHaveBeenCalled()
  expect(designNodes(doc)[0].text).toBe("Mine")
 })
 it("compares an edit still queued when the socket dropped against the confirmed canvas", () => {
  const { doc, socket, server, conflicts } = live()
  upsertDesignNode(doc, { id: "label", text: "Queued" })
  socket.close(1006)
  upsertDesignNode(server, { id: "label", text: "Theirs" })
  const next = reconnect()
  next.receive(server, 1)
  next.receive(server, 2)
  expect(found(conflicts)).toMatchObject([{ mine: "Queued", theirs: "Theirs" }])
 })
 it("compares an edit sent but not confirmed when the socket dropped", () => {
  const { doc, socket, server, conflicts } = live()
  upsertDesignNode(doc, { id: "label", text: "Sent" })
  vi.advanceTimersByTime(100)
  socket.close(1006)
  upsertDesignNode(server, { id: "label", text: "Theirs" })
  const next = reconnect()
  next.receive(server, 1)
  next.receive(server, 2)
  expect(found(conflicts)).toMatchObject([{ mine: "Sent", theirs: "Theirs" }])
 })
 it("reports a text that lost to a frame arriving while connected", () => {
  const { doc, socket, server, conflicts } = live()
  upsertDesignNode(doc, { id: "label", text: "Mine" })
  vi.advanceTimersByTime(100)
  upsertDesignNode(server, { id: "label", text: "Theirs" })
  socket.receive(server, 3)
  expect(found(conflicts)).toMatchObject([{ mine: "Mine", theirs: "Theirs" }])
 })
 it("reports an unsent text against a deletion-only frame, which leaves the state vector alone", () => {
  const { doc, socket, server, conflicts, pending } = live()
  upsertDesignNode(doc, { id: "label", text: "Unsent" })
  removeDesignNode(server, "label")
  const before = pending.mock.calls.length
  socket.receive(server, 3, doc)
  expect(designNodes(doc)).toEqual([])
  expect(found(conflicts)).toMatchObject([{ mine: "Unsent", left: undefined }])
  expect(pending.mock.calls.length).toBe(before + 1)
 })
 it("reports a remote text against this person's unsent deletion", () => {
  const { doc, socket, server, conflicts } = live()
  removeDesignNode(doc, "label")
  upsertDesignNode(server, { id: "label", text: "Theirs" })
  socket.receive(server, 3, doc)
  expect(found(conflicts)).toMatchObject([{ theirs: "Theirs", left: undefined }])
 })
 it("refreshes the mark as work grows, offline too, paced rather than per edit", () => {
  const { doc, pending } = offline()
  for (let step = 0; step < 30; step++) { upsertDesignNode(doc, { id: "label", text: `Typing ${step}` }); vi.advanceTimersByTime(100) }
  const calls = pending.mock.calls.length
  expect(calls).toBeGreaterThan(1)
  expect(calls).toBeLessThanOrEqual(4)
  vi.advanceTimersByTime(2000)
  expect(pending.mock.calls.length).toBeGreaterThan(calls - 1)
  expect(pending).not.toHaveBeenCalledWith(null)
 })
 it("does not report a later change over a text the server already confirmed", () => {
  const { doc, socket, server, conflicts } = live()
  upsertDesignNode(doc, { id: "label", text: "Mine" })
  vi.advanceTimersByTime(2000)
  Y.applyUpdate(server, Y.encodeStateAsUpdate(doc))
  socket.receive(server, 2)
  socket.close(1006)
  upsertDesignNode(server, { id: "label", text: "After" })
  const next = reconnect()
  next.receive(server, 1)
  next.receive(server, 2)
  expect(conflicts).not.toHaveBeenCalled()
 })
 it("does not report a change made on top of this person's unconfirmed text", () => {
  const { doc, socket, server, conflicts } = live()
  upsertDesignNode(doc, { id: "label", text: "Mine" })
  vi.advanceTimersByTime(100)
  Y.applyUpdate(server, Y.encodeStateAsUpdate(doc))
  upsertDesignNode(server, { id: "label", text: "On top" })
  socket.receive(server, 3, doc)
  expect(conflicts).not.toHaveBeenCalled()
  expect(designNodes(doc)[0].text).toBe("On top")
 })
 it("reports pending at the first edit and clears it only on the reply to a barrier sent after it", () => {
  const { doc, socket, server, pending } = live()
  upsertDesignNode(doc, { id: "label", text: "Mine" })
  expect(pending).toHaveBeenCalledTimes(1)
  expect(pending.mock.calls[0][0]).toBeInstanceOf(Uint8Array)
  socket.receive(server, 3)
  vi.advanceTimersByTime(100)
  socket.receive(server, 3)
  expect(pending).not.toHaveBeenCalledWith(null)
  vi.advanceTimersByTime(2000)
  expect(documentFrames(socket).at(-1)).toBe(sync.messageYjsSyncStep1)
  socket.receive(server, 2)
  expect(pending).toHaveBeenLastCalledWith(null)
 })
 it("does not count the reply to a step 1 sent before the edit", () => {
  const ctx = setup(true)
  const server = new Y.Doc()
  ctx.socket.receive(server, 1)
  vi.useFakeTimers()
  upsertDesignNode(ctx.doc, label)
  vi.advanceTimersByTime(100)
  ctx.socket.receive(server, 2)
  expect(ctx.pending).not.toHaveBeenCalledWith(null)
  vi.advanceTimersByTime(2000)
  ctx.socket.receive(server, 2)
  expect(ctx.pending).toHaveBeenLastCalledWith(null)
 })
 it("keeps pending through a reconnect until the sync step 2 sent to the server is confirmed", () => {
  const { doc, server, pending } = offline()
  upsertDesignNode(doc, { id: "label", text: "Mine" })
  const socket = reconnect()
  socket.receive(server, 2)
  expect(pending).not.toHaveBeenCalledWith(null)
  socket.receive(server, 1)
  expect(documentFrames(socket).slice(-2)).toEqual([sync.messageYjsSyncStep2, sync.messageYjsSyncStep1])
  expect(pending).not.toHaveBeenCalledWith(null)
  socket.receive(server, 2)
  expect(pending).toHaveBeenLastCalledWith(null)
 })
 it("keeps an edit queued at revocation pending, and sends it when editing returns", () => {
  const { doc, provider, socket, server, pending } = live()
  upsertDesignNode(doc, { id: "label", text: "Queued" })
  provider.setEditable(false)
  vi.advanceTimersByTime(3000)
  expect(documentFrames(socket)).not.toContain(sync.messageYjsUpdate)
  expect(pending).not.toHaveBeenCalledWith(null)
  upsertDesignNode(doc, { id: "label", text: "Queued, then more" })
  provider.setEditable(true)
  vi.advanceTimersByTime(100)
  expect(documentFrames(socket)).toContain(sync.messageYjsUpdate)
  vi.advanceTimersByTime(2000)
  socket.receive(server, 2)
  expect(pending).toHaveBeenLastCalledWith(null)
 })
 it("keeps pending when a permission close stops the socket", () => {
  const { doc, socket, pending } = live()
  upsertDesignNode(doc, { id: "label", text: "Mine" })
  vi.advanceTimersByTime(100)
  socket.close(4403)
  expect(pending).not.toHaveBeenCalledWith(null)
 })
 it("keeps comparing across a credential rotation, which closes the socket without a drop", async () => {
  vi.useFakeTimers()
  const prepare = vi.fn(async () => "ready" as const)
  const server = new Y.Doc(); upsertDesignNode(server, label)
  const { doc, pending, conflicts } = setup(true, undefined, { prepare })
  await vi.advanceTimersByTimeAsync(0)
  const first = Socket.instances.at(-1)!
  first.onopen?.()
  first.receive(server, 1)
  first.receive(server, 2)
  upsertDesignNode(doc, { id: "label", text: "Mine" })
  vi.advanceTimersByTime(100)
  first.sent = []
  await vi.advanceTimersByTimeAsync(5 * 60 * 1000)
  const second = Socket.instances.at(-1)!
  expect(second).not.toBe(first)
  second.onopen?.()
  upsertDesignNode(server, { id: "label", text: "Theirs" })
  second.receive(server, 1)
  second.receive(server, 2)
  expect(pending).not.toHaveBeenCalledWith(null)
  expect(found(conflicts)).toMatchObject([{ mine: "Mine", theirs: "Theirs" }])
 })
 it("takes over a stored baseline and draft from a replaced provider", () => {
  const server = new Y.Doc(); upsertDesignNode(server, label)
  const baseline = Y.encodeStateAsUpdate(server)
  const doc = new Y.Doc(); Y.applyUpdate(doc, baseline)
  upsertDesignNode(doc, { id: "label", text: "Mine" })
  const { socket, pending, conflicts } = setup(true, undefined, { doc, baseline, unconfirmed: true })
  vi.useFakeTimers()
  upsertDesignNode(server, { id: "label", text: "Theirs" })
  socket.receive(server, 1)
  socket.receive(server, 2)
  expect(found(conflicts)).toMatchObject([{ mine: "Mine", theirs: "Theirs" }])
  expect(pending).not.toHaveBeenCalledWith(null)
  socket.receive(server, 2)
  expect(pending).toHaveBeenLastCalledWith(null)
 })
 it("treats a stored draft without a baseline as unconfirmed, without inventing conflicts", () => {
  const server = new Y.Doc(); upsertDesignNode(server, label)
  const doc = new Y.Doc(); Y.applyUpdate(doc, Y.encodeStateAsUpdate(server))
  const { socket, pending, conflicts } = setup(true, undefined, { doc, unconfirmed: true })
  socket.receive(server, 1)
  socket.receive(server, 2)
  socket.receive(server, 2)
  expect(pending).toHaveBeenLastCalledWith(null)
  expect(conflicts).not.toHaveBeenCalled()
 })
 it("stays under the server's sync request budget while a person types", () => {
  const { doc, socket } = live()
  const before = documentFrames(socket).filter(type => type === sync.messageYjsSyncStep1).length
  for (let step = 0; step < 100; step++) { upsertDesignNode(doc, { id: "label", text: `Typing ${step}` }); vi.advanceTimersByTime(100) }
  expect(documentFrames(socket).filter(type => type === sync.messageYjsSyncStep1).length - before).toBeLessThanOrEqual(8)
 })
 it("releases a snapshot barrier only on the reply to its own request", async () => {
  const ctx = setup(true)
  const server = new Y.Doc()
  ctx.socket.receive(server, 1)
  ctx.socket.receive(server, 3)
  let released = false
  const barrier = ctx.provider.synchronize().then(() => { released = true })
  await new Promise(resolve => setTimeout(resolve, 0))
  ctx.socket.receive(server, 2)
  await new Promise(resolve => setTimeout(resolve, 0))
  expect(released).toBe(false)
  ctx.socket.receive(server, 2)
  await barrier
  expect(released).toBe(true)
 })
})
