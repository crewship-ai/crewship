import { afterEach, describe, expect, it, vi } from "vitest"
import * as Y from "yjs"
import * as encoding from "lib0/encoding"
import * as decoding from "lib0/decoding"
import * as sync from "y-protocols/sync"
import { DesignProvider } from "../design-provider"

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
 receive(doc: Y.Doc, step = 1) {
  const encoder = encoding.createEncoder()
  encoding.writeVarUint(encoder, 0)
  if (step === 1) sync.writeSyncStep1(encoder, doc)
  else if (step === 2) sync.writeSyncStep2(encoder, doc)
  else sync.writeUpdate(encoder, Y.encodeStateAsUpdate(doc))
  const bytes = encoding.toUint8Array(encoder)
  this.onmessage?.({ data: bytes.buffer.slice(bytes.byteOffset, bytes.byteOffset + bytes.byteLength) as ArrayBuffer })
 }
}
const active: DesignProvider[] = []
afterEach(() => { active.splice(0).forEach(provider => provider.destroy()); vi.unstubAllGlobals(); vi.useRealTimers(); Socket.instances = [] })
function setup(editable: boolean, url = "ws://localhost/design/ws") {
 vi.stubGlobal("WebSocket", Socket)
 const doc = new Y.Doc(), status = vi.fn(), dirty = vi.fn()
 const provider = new DesignProvider(doc, url, editable, { name: "Test", color: "#2563eb" }, status, dirty)
 active.push(provider)
 const socket = Socket.instances.at(-1)!
 socket.onopen?.()
 return { doc, provider, socket, status, dirty }
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
