import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"
import * as Y from "yjs"
import * as encoding from "lib0/encoding"
import * as decoding from "lib0/decoding"
import * as sync from "y-protocols/sync"
import { DesignProvider, type DesignTracking } from "../design-provider"
import { designNodes, restoreDesignConflict, upsertDesignNode, type DesignConflict } from "../design-document"

/** The Design room as internal/api/pages_design_ws.go runs it: a Hello on admission, one socket's frames in order, updates broadcast to everyone but the sender, one sync reply per request, and the rate limit that closes the socket. */
class Room {
 doc = new Y.Doc()
 clients = new Set<Wire>()
 enqueue(frame: Uint8Array, to: Wire) { setTimeout(() => to.deliver(frame), 5) }
 hello(client: Wire) {
  const encoder = encoding.createEncoder()
  encoding.writeVarUint(encoder, 0)
  sync.writeSyncStep1(encoder, this.doc)
  this.enqueue(encoding.toUint8Array(encoder), client)
 }
 frame(client: Wire, frame: Uint8Array) {
  const decoder = decoding.createDecoder(frame)
  if (decoding.readVarUint(decoder) !== 0) return
  const sub = decoding.readVarUint(decoder)
  if (sub === sync.messageYjsSyncStep1) {
   client.tokens = Math.min(5, client.tokens + (performance.now() - client.checked) / 1000)
   client.checked = performance.now()
   if (client.tokens < 1) { client.close(1008); return }
   client.tokens--
   const encoder = encoding.createEncoder()
   encoding.writeVarUint(encoder, 0)
   sync.readSyncStep1(decoder, encoder, this.doc)
   this.enqueue(encoding.toUint8Array(encoder), client)
   return
  }
  const update = decoding.readVarUint8Array(decoder)
  const before = Y.encodeStateVector(this.doc)
  Y.applyUpdate(this.doc, update)
  const encoder = encoding.createEncoder()
  encoding.writeVarUint(encoder, 0)
  sync.writeUpdate(encoder, Y.encodeStateAsUpdate(this.doc, before))
  for (const peer of this.clients) if (peer !== client) this.enqueue(encoding.toUint8Array(encoder), peer)
 }
}
const room = { current: new Room() }
class Wire {
 static OPEN = 1
 static cut = false
 readyState = 0
 binaryType = ""
 tokens = 5
 checked = performance.now()
 onopen: (() => void) | null = null
 onmessage: ((event: { data: ArrayBuffer }) => void) | null = null
 onerror: (() => void) | null = null
 onclose: ((event: { code: number }) => void) | null = null
 constructor(readonly url: string) {
  setTimeout(() => {
   if (this.readyState !== 0) return
   if (url.includes("cut") && Wire.cut) { this.close(1006); return }
   this.readyState = 1
   room.current.clients.add(this)
   this.onopen?.()
   room.current.hello(this)
  }, 5)
 }
 send(bytes: ArrayBuffer) { const copy = new Uint8Array(bytes.slice(0)); setTimeout(() => { if (this.readyState === 1) room.current.frame(this, copy) }, 5) }
 deliver(frame: Uint8Array) { if (this.readyState === 1) this.onmessage?.({ data: frame.buffer.slice(frame.byteOffset, frame.byteOffset + frame.byteLength) as ArrayBuffer }) }
 close(code = 1000) {
  if (this.readyState === 3) return
  this.readyState = 3
  room.current.clients.delete(this)
  this.onclose?.({ code })
 }
}
const label = { id: "label", type: "text" as const, name: "Title", text: "Base", x: 1, y: 2, width: 200, height: 40, fill: "#111111" }
const providers: DesignProvider[] = []
function person(url: string, start: Pick<DesignTracking, "baseline" | "unconfirmed"> & { doc?: Y.Doc } = {}) {
 const doc = start.doc ?? new Y.Doc(), marks: (Uint8Array | null)[] = [], conflicts: DesignConflict[] = []
 const provider = new DesignProvider(doc, url, true, { name: url, color: "#2563eb" }, () => {}, () => {}, undefined, { ...start, pending: mark => marks.push(mark), conflicts: found => conflicts.push(...found) })
 providers.push(provider)
 return { doc, provider, marks, conflicts, confirmed: () => marks.at(-1) === null }
}
const text = (doc: Y.Doc) => designNodes(doc).find(node => node.id === "label")?.text
beforeEach(() => {
 vi.useFakeTimers()
 vi.stubGlobal("WebSocket", Wire)
 Wire.cut = false
 room.current = new Room()
 upsertDesignNode(room.current.doc, label)
})
afterEach(() => { providers.splice(0).forEach(provider => provider.destroy()); vi.unstubAllGlobals(); vi.useRealTimers() })
const settle = (ms = 100) => vi.advanceTimersByTime(ms)

describe("Design providers against the room's frame order", () => {
 it("confirms an online edit, and only after the room holds it", () => {
  const anna = person("anna")
  settle()
  upsertDesignNode(anna.doc, { id: "label", text: "Anna" })
  expect(anna.confirmed()).toBe(false)
  settle(80)
  expect(text(room.current.doc)).toBe("Anna")
  expect(anna.confirmed()).toBe(false)
  settle(2000)
  expect(anna.confirmed()).toBe(true)
 })
 it("names the loser of a queued edit against a change made while the socket was down, and restores it", () => {
  const cut = person("cut")
  const peer = person("peer")
  settle()
  upsertDesignNode(cut.doc, { id: "label", text: "Queued" })
  Wire.cut = true
  ;[...room.current.clients].find(client => client.url === "cut")!.close(1006)
  upsertDesignNode(peer.doc, { id: "label", text: "Peer" })
  upsertDesignNode(peer.doc, { id: "label", x: 50 })
  settle(2000)
  Wire.cut = false
  expect(cut.confirmed()).toBe(false)
  settle(4000)
  expect(cut.confirmed()).toBe(true)
  const [conflict] = cut.conflicts
  expect(conflict).toMatchObject({ mine: "Queued", theirs: "Peer", left: text(cut.doc) })
  expect(restoreDesignConflict(cut.doc, conflict)).toBe("text")
  settle(4000)
  const lost = conflict.kept === "mine" ? "Peer" : "Queued"
  for (const doc of [cut.doc, peer.doc, room.current.doc]) expect(designNodes(doc).find(node => node.id === "label")).toMatchObject({ text: lost, x: 50 })
  expect(cut.confirmed()).toBe(true)
 })
 it("confirms work and finds the conflict in a provider that replaced the one that made it", () => {
  const first = person("first")
  settle()
  upsertDesignNode(first.doc, { id: "label", text: "Stored" })
  const baseline = first.marks[0]!, draft = Y.encodeStateAsUpdate(first.doc)
  first.provider.destroy()
  upsertDesignNode(room.current.doc, { id: "label", text: "Peer" })
  const doc = new Y.Doc(); Y.applyUpdate(doc, draft)
  const second = person("second", { doc, baseline, unconfirmed: true })
  settle(300)
  expect(second.conflicts).toMatchObject([{ mine: "Stored", theirs: "Peer" }])
  expect(second.confirmed()).toBe(true)
  expect(text(room.current.doc)).toBe(text(doc))
 })
 it("does not report an unconfirmed text the room already holds when a peer changes something else", () => {
  upsertDesignNode(room.current.doc, { ...label, id: "other" })
  const anna = person("anna")
  const peer = person("peer")
  settle()
  upsertDesignNode(anna.doc, { id: "label", text: "Anna" })
  settle(90)
  upsertDesignNode(peer.doc, { id: "other", text: "Peer elsewhere" })
  settle(200)
  expect(text(room.current.doc)).toBe("Anna")
  expect(anna.conflicts).toEqual([])
  expect(peer.conflicts).toEqual([])
 })
 it("tells the person whose text lost a race on one layer, with that text", () => {
  const anna = person("anna")
  const peer = person("peer")
  settle()
  upsertDesignNode(anna.doc, { id: "label", text: "Anna" })
  upsertDesignNode(peer.doc, { id: "label", text: "Peer" })
  settle(3000)
  const left = text(room.current.doc)
  expect(text(anna.doc)).toBe(left)
  expect(text(peer.doc)).toBe(left)
  const loser = left === "Anna" ? peer : anna
  expect(loser.conflicts).toMatchObject([{ mine: left === "Anna" ? "Peer" : "Anna", left }])
  expect(loser.conflicts[0].kept).toBe("theirs")
 })
 it("keeps the socket through rapid saves, each still covering its own edit", async () => {
  const anna = person("anna")
  settle()
  const admitted = [...room.current.clients][0]
  for (let save = 0; save < 12; save++) {
   upsertDesignNode(anna.doc, { id: "label", text: `Save ${save}` })
   const saved = anna.provider.synchronize()
   await vi.advanceTimersByTimeAsync(40)
   let done = false
   void saved.then(() => { done = true }, () => { done = true })
   for (let wait = 0; wait < 40 && !done; wait++) await vi.advanceTimersByTimeAsync(100)
   await saved
   expect(text(room.current.doc)).toBe(`Save ${save}`)
  }
  expect([...room.current.clients]).toEqual([admitted])
 })
 it("keeps the socket when saves overlap with background barriers", async () => {
  const anna = person("anna")
  settle()
  const saves: Promise<void>[] = []
  for (let save = 0; save < 12; save++) { upsertDesignNode(anna.doc, { id: "label", text: `Overlap ${save}` }); saves.push(anna.provider.synchronize()) }
  // Saves queued past their own deadline refuse; none costs the socket.
  const settled = Promise.allSettled(saves)
  await vi.advanceTimersByTimeAsync(20_000)
  expect((await settled)[0].status).toBe("fulfilled")
  expect(room.current.clients.size).toBe(1)
 })
 it("does not exhaust the room's sync budget during long typing", () => {
  const anna = person("anna")
  settle()
  for (let key = 0; key < 600; key++) { upsertDesignNode(anna.doc, { id: "label", text: `Key ${key}` }); settle(50) }
  settle(3000)
  expect(room.current.clients.size).toBe(1)
  expect(anna.confirmed()).toBe(true)
  expect(text(room.current.doc)).toBe("Key 599")
 })
})

describe("A save waiting on the sync budget (#5017)", () => {
 const drained = () => {
  const anna = person("anna")
  settle()
  ;(anna.provider as unknown as { tokens: number }).tokens = -100
  return anna
 }
 const outcome = (saving: Promise<void>) => {
  const state = { error: "", done: false }
  void saving.then(() => { state.done = true }, (error: Error) => { state.done = true; state.error = error.message })
  return state
 }
 it("is not held for an hour by the wall clock jumping back", async () => {
  const anna = person("anna")
  settle()
  vi.setSystemTime(Date.now() - 60 * 60 * 1000)
  upsertDesignNode(anna.doc, { id: "label", text: "After the jump" })
  const state = outcome(anna.provider.synchronize())
  await vi.advanceTimersByTimeAsync(6000)
  expect(state).toEqual({ error: "", done: true })
  expect(text(room.current.doc)).toBe("After the jump")
 })
 it("stays inside the room's budget when the wall clock jumps forward", async () => {
  const anna = person("anna")
  settle()
  const admitted = [...room.current.clients][0]
  for (let save = 0; save < 12; save++) {
   vi.setSystemTime(Date.now() + 60 * 60 * 1000)
   upsertDesignNode(anna.doc, { id: "label", text: `Save ${save}` })
   void anna.provider.synchronize().catch(() => {})
   await vi.advanceTimersByTimeAsync(100)
  }
  await vi.advanceTimersByTimeAsync(20_000)
  expect([...room.current.clients]).toEqual([admitted])
  expect(text(room.current.doc)).toBe("Save 11")
 })
 it("gives up at a deadline and leaves the work unconfirmed", async () => {
  const anna = drained()
  upsertDesignNode(anna.doc, { id: "label", text: "Waiting" })
  const state = outcome(anna.provider.synchronize())
  await vi.advanceTimersByTimeAsync(6000)
  expect(state).toEqual({ error: "The canvas is still reconnecting. Your changes remain in this browser.", done: true })
  expect(anna.confirmed()).toBe(false)
 })
 it("stops at once when its caller aborts, before asking the room", async () => {
  const anna = drained()
  const frames = vi.spyOn([...room.current.clients][0], "send")
  const stop = new AbortController()
  const state = outcome(anna.provider.synchronize(stop.signal))
  await vi.advanceTimersByTimeAsync(10)
  stop.abort(new Error("Save cancelled."))
  await vi.advanceTimersByTimeAsync(0)
  expect(state).toEqual({ error: "Save cancelled.", done: true })
  await vi.advanceTimersByTimeAsync(200_000)
  expect(frames).not.toHaveBeenCalled()
 })
 it("stops a queued save at once when its caller aborts, behind one that gets no reply", async () => {
  const anna = person("anna")
  settle()
  ;[...room.current.clients][0].send = () => {}
  const first = outcome(anna.provider.synchronize())
  const stop = new AbortController()
  const queued = outcome(anna.provider.synchronize(stop.signal))
  await vi.advanceTimersByTimeAsync(10)
  stop.abort(new Error("Save cancelled."))
  await vi.advanceTimersByTimeAsync(0)
  expect(queued).toEqual({ error: "Save cancelled.", done: true })
  expect(first.done).toBe(false)
 })
 it("counts the deadline from the call, not from the save's turn in the queue", async () => {
  const anna = person("anna")
  settle()
  ;[...room.current.clients][0].send = () => {}
  const first = outcome(anna.provider.synchronize())
  const queued = outcome(anna.provider.synchronize())
  await vi.advanceTimersByTimeAsync(5100)
  expect(first.done).toBe(true)
  expect(queued).toEqual({ error: "The canvas is still reconnecting. Your changes remain in this browser.", done: true })
 })
 it("sends nothing for a queued save aborted before its turn", async () => {
  const anna = person("anna")
  settle()
  const wire = [...room.current.clients][0]
  const real = wire.send.bind(wire)
  let frames = 0
  wire.send = () => {}
  void anna.provider.synchronize().catch(() => {})
  const stop = new AbortController()
  void anna.provider.synchronize(stop.signal).catch(() => {})
  await vi.advanceTimersByTimeAsync(10)
  stop.abort(new Error("Save cancelled."))
  wire.send = bytes => { frames++; real(bytes) }
  await vi.advanceTimersByTimeAsync(6000)
  expect(frames).toBe(0)
 })
 it("does not start when its caller already aborted", async () => {
  const anna = person("anna")
  settle()
  const stop = new AbortController()
  stop.abort(new Error("Save cancelled."))
  await expect(anna.provider.synchronize(stop.signal)).rejects.toThrow("Save cancelled.")
 })
 it("stops at once when the socket closes or the provider goes", async () => {
  const anna = drained()
  const closed = outcome(anna.provider.synchronize())
  await vi.advanceTimersByTimeAsync(10)
  ;[...room.current.clients][0].close(1006)
  await vi.advanceTimersByTimeAsync(0)
  expect(closed).toEqual({ error: "Canvas reconnected before the version was saved. Save again after reconnecting.", done: true })
  const bert = drained()
  const gone = outcome(bert.provider.synchronize())
  await vi.advanceTimersByTimeAsync(10)
  bert.provider.destroy()
  await vi.advanceTimersByTimeAsync(0)
  expect(gone).toEqual({ error: "Canvas closed before the version was saved.", done: true })
 })
})
