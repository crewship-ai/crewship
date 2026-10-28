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
 constructor(_url: string) { Socket.instances.push(this) }
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
afterEach(() => { active.splice(0).forEach(provider => provider.destroy()); vi.unstubAllGlobals(); Socket.instances = [] })
function setup(editable: boolean) {
 vi.stubGlobal("WebSocket", Socket)
 const doc = new Y.Doc(), status = vi.fn(), dirty = vi.fn()
 const provider = new DesignProvider(doc, "ws://localhost/design/ws", editable, { name: "Test", color: "#2563eb" }, status, dirty)
 active.push(provider)
 const socket = Socket.instances.at(-1)!
 socket.onopen?.()
 return { doc, provider, socket, status, dirty }
}
function documentFrames(socket: Socket) {
 return socket.sent.flatMap(bytes => { const decoder = decoding.createDecoder(bytes); return decoding.readVarUint(decoder) === 0 ? [decoding.readVarUint(decoder)] : [] })
}
describe("Design standard sync provider", () => {
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
