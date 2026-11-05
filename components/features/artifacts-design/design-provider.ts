import * as Y from "yjs"
import * as sync from "y-protocols/sync"
import { Awareness, applyAwarenessUpdate, encodeAwarenessUpdate, removeAwarenessStates } from "y-protocols/awareness"
import * as encoding from "lib0/encoding"
import * as decoding from "lib0/decoding"
import { designConflicts, designNodes, type DesignConflict } from "./design-document"

export type DesignConnection = "connecting" | "connected" | "offline" | "denied" | "rejected" | "quota"
export type DesignAdmission = "ready" | "denied" | "retry"
export interface DesignPeer { clientId: number; name: string; color: string; cursor?: { x: number; y: number } }
const REMOTE = "design-server"
// The server spends one sync request per barrier: a burst of 5, then 1 a second.
const BARRIER_MS = 1500

/** How a provider reports work the server has not confirmed, and takes it over from a replaced one. */
export interface DesignTracking {
 /** The document already holds work the server may lack (a stored draft). */
 unconfirmed?: boolean
 /** The confirmed canvas that work was made from, as last given to `pending`. Without it nothing can be compared. */
 baseline?: Uint8Array
 /** The confirmed canvas while work is unconfirmed; null once the server confirmed all of it. */
 pending?: (baseline: Uint8Array | null) => void
 /** Texts a merge dropped. */
 conflicts?: (found: DesignConflict[]) => void
}
type Waiter = { resolve: () => void; reject: (error: Error) => void; timer: ReturnType<typeof setTimeout> }

/** Carries the standard y-websocket sync and awareness envelopes through Crewship auth. */
export class DesignProvider {
 readonly awareness: Awareness
 private socket: WebSocket | null = null
 private retry: ReturnType<typeof setTimeout> | undefined
 private stopped = false
 private canEdit: boolean
 private admittedEdit: boolean
 private generation = 0
 private renewal: ReturnType<typeof setTimeout> | undefined
 private full = false
 get storageFull() { return this.full }
 private connected = false
 private acknowledged = false
 private updateTimer: ReturnType<typeof setTimeout> | undefined
 private queuedUpdates: Uint8Array[] = []
 private cursorTimer: ReturnType<typeof setTimeout> | undefined
 private cursor: { x: number; y: number } | null = null
 private syncQueue: Promise<void> = Promise.resolve()
 private waiters = new Set<Waiter>()
 private changeListener: (update: Uint8Array, origin: unknown) => void
 private awarenessListener: (changes: { added: number[]; updated: number[]; removed: number[] }, origin: unknown) => void
 private beforeEdit: (transaction: Y.Transaction) => void
 // Local updates the server has not confirmed. The server handles one socket's
 // frames in order and never echoes a sender's update, so the only confirmation
 // is the reply to a sync step 1 sent after them: a barrier. A closed socket
 // proves nothing about what it delivered, so they stay until a barrier on a
 // live socket covers them, across reconnects, rotation and revocation.
 private pending: { seq: number; update: Uint8Array }[] = []
 // What the server is known to hold while `pending` is not empty: the canvas
 // before the first unconfirmed edit plus every remote frame since. Local edits
 // never enter it until confirmed, so a text both sides changed is visible.
 private confirmed: Y.Doc | null = null
 private seq = 0
 // The newest local update this socket has received, in causal order.
 private sent = 0
 // Set once this socket answered the server's step 1: everything local is sent.
 private answered = false
 // The newest update already covered by a barrier on this socket.
 private asked = 0
 private barrierTimer: ReturnType<typeof setTimeout> | undefined
 // One entry per step 1 sent on this socket: the server replies to them in order.
 private requests: { seq: number; waiter?: Waiter }[] = []
 private tracking: DesignTracking

 constructor(readonly doc: Y.Doc, private url: string, editable: boolean, user: { name: string; color: string }, private status: (status: DesignConnection) => void, private dirty: () => void, private prepare?: () => Promise<DesignAdmission>, tracking: DesignTracking = {}) {
  this.canEdit = editable
  this.admittedEdit = editable
  this.tracking = tracking
  if (editable && tracking.unconfirmed) {
   this.pending = [{ seq: ++this.seq, update: Y.encodeStateAsUpdate(doc) }]
   if (tracking.baseline) {
    this.confirmed = new Y.Doc()
    Y.applyUpdate(this.confirmed, tracking.baseline, REMOTE)
   }
  }
  this.awareness = new Awareness(doc)
  this.awareness.setLocalStateField("user", user)
  this.beforeEdit = transaction => {
   // Clean, the document is what the server holds. Copied only for the edit
   // that starts a period of unconfirmed work.
   if (transaction.origin === REMOTE || !this.admittedEdit || this.pending.length) return
   this.confirmed?.destroy()
   this.confirmed = new Y.Doc()
   Y.applyUpdate(this.confirmed, Y.encodeStateAsUpdate(doc), REMOTE)
  }
  this.changeListener = (update, origin) => {
   if (origin === REMOTE || !this.admittedEdit) return
   this.pending.push({ seq: ++this.seq, update })
   if (this.pending.length === 1) this.reportPending()
   if (!this.canEdit) return
   this.dirty()
   if (this.full) return
   this.queuedUpdates.push(update)
   if (!this.updateTimer) this.updateTimer = setTimeout(() => this.flushUpdates(), 75)
  }
  this.awarenessListener = ({ added, updated, removed }, origin) => {
   if (origin === REMOTE) return
   const encoder = encoding.createEncoder()
   encoding.writeVarUint(encoder, 1)
   encoding.writeVarUint8Array(encoder, encodeAwarenessUpdate(this.awareness, [...added, ...updated, ...removed]))
   this.send(encoding.toUint8Array(encoder))
  }
  doc.on("beforeTransaction", this.beforeEdit)
  doc.on("update", this.changeListener)
  this.awareness.on("update", this.awarenessListener)
  this.connect()
 }
 get ready() { return this.connected && this.acknowledged }
 private flushUpdates() {
  clearTimeout(this.updateTimer)
  this.updateTimer = undefined
  if (!this.canEdit || !this.queuedUpdates.length) { this.queuedUpdates = []; return }
  const encoder = encoding.createEncoder()
  encoding.writeVarUint(encoder, 0)
  sync.writeUpdate(encoder, Y.mergeUpdates(this.queuedUpdates))
  this.queuedUpdates = []
  // Before the sync step 2 the server may lack what this update builds on.
  const delivered = this.answered && this.socket?.readyState === WebSocket.OPEN
  this.send(encoding.toUint8Array(encoder))
  if (delivered) {
   this.sent = this.seq
   this.barrierTimer ??= setTimeout(() => { this.barrierTimer = undefined; this.barrier() }, BARRIER_MS)
  }
 }
 private request(waiter?: Waiter) {
  this.asked = this.sent
  this.requests.push({ seq: this.sent, waiter })
  const encoder = encoding.createEncoder()
  encoding.writeVarUint(encoder, 0)
  sync.writeSyncStep1(encoder, this.doc)
  this.send(encoding.toUint8Array(encoder))
 }
 private barrier() {
  if (this.answered && this.socket?.readyState === WebSocket.OPEN && this.pending.some(item => item.seq > this.asked && item.seq <= this.sent)) this.request()
 }
 private reportPending() {
  if (this.confirmed) this.tracking.pending?.(Y.encodeStateAsUpdate(this.confirmed))
 }
 private confirmThrough(seq: number) {
  const left = this.pending.filter(item => item.seq > seq)
  if (left.length === this.pending.length) return
  if (!left.length) {
   this.pending = []
   this.confirmed?.destroy()
   this.confirmed = null
   this.tracking.pending?.(null)
   return
  }
  if (this.confirmed) for (const item of this.pending) if (item.seq <= seq) Y.applyUpdate(this.confirmed, item.update, REMOTE)
  this.pending = left
 }
 /** A remote frame goes to the document and, while work is unconfirmed, to the confirmed canvas beside it. */
 private applyRemote(decoder: decoding.Decoder) {
  const confirmed = this.pending.length ? this.confirmed : null
  if (!confirmed) { sync.readSyncStep2(decoder, this.doc, REMOTE); return }
  const base = designNodes(confirmed), mine = designNodes(this.doc), before = Y.encodeStateVector(confirmed)
  sync.readSyncStep2(decoding.clone(decoder), confirmed, REMOTE)
  sync.readSyncStep2(decoder, this.doc, REMOTE)
  const after = Y.encodeStateVector(confirmed)
  if (before.length === after.length && before.every((byte, index) => byte === after[index])) return
  const found = designConflicts(base, mine, designNodes(confirmed), designNodes(this.doc))
  if (found.length) this.tracking.conflicts?.(found)
  this.reportPending()
 }
 synchronize(): Promise<void> {
  const next = this.syncQueue.then(() => this.synchronizeNow())
  this.syncQueue = next.catch(() => {})
  return next
 }
 private async synchronizeNow(): Promise<void> {
  if (!this.ready) throw new Error("Reconnect before saving a version.")
  this.flushUpdates()
  return new Promise<void>((resolve, reject) => {
   const waiter: Waiter = { resolve, reject, timer: setTimeout(() => { this.waiters.delete(waiter); reject(new Error("The canvas is still reconnecting. Your changes remain in this browser.")) }, 5000) }
   this.waiters.add(waiter)
   this.request(waiter)
  })
 }
 setEditable(editable: boolean) {
  this.canEdit = editable
  // Work dropped from the queue while editing was off: the socket never had it.
  if (!editable || !this.answered || !this.pending.some(item => item.seq > this.sent)) return
  this.queuedUpdates = this.pending.map(item => item.update)
  this.updateTimer ??= setTimeout(() => this.flushUpdates(), 75)
 }
 setCursor(cursor: { x: number; y: number } | null) {
  this.cursor = cursor
  if (!this.cursorTimer) this.cursorTimer = setTimeout(() => { this.cursorTimer = undefined; this.awareness.setLocalStateField("cursor", this.cursor) }, 50)
 }
 private send(message: Uint8Array) {
  if (this.socket?.readyState === WebSocket.OPEN) this.socket.send(Uint8Array.from(message).buffer)
 }
 private connect(recovering = false) {
  if (this.stopped) return
  const generation = ++this.generation
  if (!recovering) this.status("connecting")
  if (!this.prepare) { this.open(generation); return }
  void this.prepare().catch(() => "retry" as const).then(admission => {
   if (this.stopped || generation !== this.generation) return
   if (admission === "ready") { this.canEdit = this.admittedEdit; this.status("connecting"); this.open(generation) }
   else if (admission === "denied") { this.canEdit = false; this.status("denied") }
   else { this.status("offline"); this.retry = setTimeout(() => this.connect(recovering), 2000) }
  })
 }
 private open(generation: number) {
  if (this.stopped || generation !== this.generation) return
  const socket = new WebSocket(this.url)
  this.socket = socket
  socket.binaryType = "arraybuffer"
  socket.onopen = () => {
   if (this.stopped || this.socket !== socket) return
   this.connected = true
   this.status("connected")
   if (this.prepare) this.renewal = setTimeout(() => this.rotate(), 5 * 60 * 1000)
   this.sent = this.asked = 0
   this.answered = false
   this.requests = []
   this.request()
   const awareness = encoding.createEncoder()
   encoding.writeVarUint(awareness, 1)
   encoding.writeVarUint8Array(awareness, encodeAwarenessUpdate(this.awareness, [this.doc.clientID]))
   this.send(encoding.toUint8Array(awareness))
  }
  socket.onmessage = event => {
   if (this.stopped || this.socket !== socket) return
   if (!(event.data instanceof ArrayBuffer)) return
   try {
    const decoder = decoding.createDecoder(new Uint8Array(event.data))
    const type = decoding.readVarUint(decoder)
    if (type === 0) {
     const subtype = decoding.readVarUint(decoder)
     if (subtype === sync.messageYjsSyncStep1) {
      // Read-only clients never reply with a document update, including sync step 2.
      if (this.canEdit) {
       const encoder = encoding.createEncoder()
       encoding.writeVarUint(encoder, 0)
       sync.readSyncStep1(decoder, encoder, this.doc)
       this.send(encoding.toUint8Array(encoder))
       this.answered = true
       this.sent = this.seq
       this.barrier()
      }
     } else if (subtype === sync.messageYjsSyncStep2 || subtype === sync.messageYjsUpdate) {
      this.applyRemote(decoder)
      this.acknowledged = true
      if (subtype === sync.messageYjsSyncStep2) {
       const reply = this.requests.shift()
       if (reply) this.confirmThrough(reply.seq)
       if (reply?.waiter) { clearTimeout(reply.waiter.timer); this.waiters.delete(reply.waiter); reply.waiter.resolve() }
      }
     }
    } else if (type === 1) applyAwarenessUpdate(this.awareness, decoding.readVarUint8Array(decoder), REMOTE)
   } catch {
    this.connected = false
    this.status("offline")
    socket.close(1002, "Invalid collaboration frame")
   }
  }
  socket.onerror = () => { if (!this.stopped && this.socket === socket) this.status("offline") }
  socket.onclose = event => {
   if (this.stopped || this.socket !== socket) return
   this.socket = null
   clearTimeout(this.renewal)
   this.rejectWaiters(event.code===4009?"Workspace storage is full. Your private draft is retained.":"Canvas reconnected before the version was saved. Save again after reconnecting.",event.code===4009?413:undefined)
   this.connected = false
   this.acknowledged = false
   removeAwarenessStates(this.awareness, [...this.awareness.getStates().keys()].filter(id => id !== this.doc.clientID), REMOTE)
   if (this.stopped) return
   if(event.code === 4009) {
    this.full=true;this.queuedUpdates=[];this.status("quota");return
   }
   if (event.code === 1008 || event.code === 4003 || event.code === 4403) {
    this.canEdit = false
    this.status("denied")
    if (this.prepare) this.connect(true)
   } else if (event.code === 4400 || event.code === 1009) {
    // The server refused this client's update (invalid, or a frame over the
    // read limit); a reconnect would resend the same change in sync step 2
    // and be refused again, forever.
    this.status("rejected")
   } else {
    this.status("offline")
    this.retry = setTimeout(() => this.connect(), 2000)
   }
  }
 }
 private rejectWaiters(message: string, status?:number) {
  for (const waiter of this.waiters) { clearTimeout(waiter.timer); waiter.reject(Object.assign(new Error(message),{status})) }
  this.waiters.clear()
 }
 private rotate() {
  if (this.stopped || !this.socket || !this.connected) return
  // Never replay or acknowledge an in-flight HTTP snapshot during renewal.
  if (this.waiters.size || this.queuedUpdates.length) { this.renewal = setTimeout(() => this.rotate(), 1000); return }
  const previous = this.socket
  this.socket = null
  this.connected = false
  this.acknowledged = false
  previous.close()
  this.connect()
 }
 destroy() {
  this.flushUpdates()
  this.stopped = true
  ++this.generation
  clearTimeout(this.renewal)
  clearTimeout(this.cursorTimer)
  for (const waiter of this.waiters) { clearTimeout(waiter.timer); waiter.reject(new Error("Canvas closed before the version was saved.")) }
  this.waiters.clear()
  clearTimeout(this.retry)
  clearTimeout(this.barrierTimer)
  this.confirmed?.destroy()
  this.confirmed = null
  this.awareness.setLocalState(null)
  this.socket?.close()
  this.doc.off("beforeTransaction", this.beforeEdit)
  this.doc.off("update", this.changeListener)
  this.awareness.off("update", this.awarenessListener)
  this.awareness.destroy()
 }
}
