import * as Y from "yjs"
import * as sync from "y-protocols/sync"
import { Awareness, applyAwarenessUpdate, encodeAwarenessUpdate, removeAwarenessStates } from "y-protocols/awareness"
import * as encoding from "lib0/encoding"
import * as decoding from "lib0/decoding"
import { designConflicts, designNodes, type DesignConflict } from "./design-document"

export type DesignConnection = "connecting" | "connected" | "offline" | "denied" | "rejected" | "quota"
export type DesignAdmission = "ready" | "denied" | "retry"
/** Another person on the canvas: name and colour from the server, pointer and selected layer from their browser. */
export interface DesignPeer { clientId: number; name: string; color: string; cursor?: { x: number; y: number }; selection?: string[] }
const REMOTE = "design-server"
// A paced turn refreshes the stored mark and sends a barrier. Every sync request
// spends from the server's bucket: a burst of 5, then 1 a second, and it closes
// the socket with 4429 when empty. The client keeps one token in reserve.
const BARRIER_MS = 1500
const REQUEST_BURST = 4
const REQUEST_REFILL_MS = 1000
// A save waits for the budget and for the reply within this time.
const SAVE_DEADLINE_MS = 5000

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
type Waiter = { resolve: () => void; reject: (error: Error) => void }

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
 // The newest update the stored mark knows about.
 private reported = 0
 private tokens = REQUEST_BURST
 private refilled = 0
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
   else this.pace()
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
   this.pace()
  }
 }
 private pace(delay = BARRIER_MS) {
  this.barrierTimer ??= setTimeout(() => {
   this.barrierTimer = undefined
   if (this.pending.length && this.seq > this.reported) this.reportPending()
   this.barrier()
  }, delay)
 }
 /** Milliseconds until the server's bucket can take another sync request. */
 private requestDelay() {
  // Monotonic like the server's bucket: a wall clock jump must not move the budget.
  const now = performance.now()
  this.tokens = Math.min(REQUEST_BURST, this.tokens + (now - this.refilled) / REQUEST_REFILL_MS)
  this.refilled = now
  return this.tokens >= 1 ? 0 : Math.ceil((1 - this.tokens) * REQUEST_REFILL_MS)
 }
 private request(waiter?: Waiter) {
  this.requestDelay()
  this.tokens -= 1
  this.asked = this.sent
  this.requests.push({ seq: this.sent, waiter })
  const encoder = encoding.createEncoder()
  encoding.writeVarUint(encoder, 0)
  sync.writeSyncStep1(encoder, this.doc)
  this.send(encoding.toUint8Array(encoder))
 }
 private barrier() {
  if (!this.answered || this.socket?.readyState !== WebSocket.OPEN || !this.pending.some(item => item.seq > this.asked && item.seq <= this.sent)) return
  const wait = this.requestDelay()
  if (wait) this.pace(wait)
  else this.request()
 }
 private reportPending() {
  this.reported = this.seq
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
  const base = designNodes(confirmed), mine = designNodes(this.doc)
  sync.readSyncStep2(decoding.clone(decoder), confirmed, REMOTE)
  sync.readSyncStep2(decoder, this.doc, REMOTE)
  // By content: a deletion-only frame leaves the state vector alone.
  const theirs = designNodes(confirmed)
  if (JSON.stringify(theirs) === JSON.stringify(base)) return
  const found = designConflicts(base, mine, theirs, designNodes(this.doc))
  if (found.length) this.tracking.conflicts?.(found)
  this.reportPending()
 }
 /** Confirms everything local. Ends at the deadline, counted from this call, or at the socket's close, `destroy` or `cancel`, with the work kept. */
 synchronize(cancel?: AbortSignal): Promise<void> {
  const deadline = new AbortController()
  const timer = setTimeout(() => deadline.abort(new Error("The canvas is still reconnecting. Your changes remain in this browser.")), SAVE_DEADLINE_MS)
  const stop = () => deadline.abort(cancel?.reason)
  if (cancel?.aborted) stop()
  else cancel?.addEventListener("abort", stop, { once: true })
  const turn = this.syncQueue.then(() => this.synchronizeNow(deadline.signal))
  this.syncQueue = turn.catch(() => {})
  // A save behind others ends without waiting for its turn.
  const ended = new Promise<never>((_, reject) => {
   if (deadline.signal.aborted) reject(deadline.signal.reason)
   else deadline.signal.addEventListener("abort", () => reject(deadline.signal.reason), { once: true })
  })
  return Promise.race([turn, ended]).finally(() => { clearTimeout(timer); cancel?.removeEventListener("abort", stop) })
 }
 private synchronizeNow(deadline: AbortSignal): Promise<void> {
  if (deadline.aborted) return Promise.reject(deadline.reason)
  if (!this.ready) return Promise.reject(new Error("Reconnect before saving a version."))
  return new Promise<void>((resolve, reject) => {
   const abort = () => { this.waiters.delete(waiter); waiter.reject(deadline.reason) }
   const settled = () => { clearTimeout(turn); deadline.removeEventListener("abort", abort) }
   let turn: ReturnType<typeof setTimeout> | undefined
   const waiter: Waiter = { resolve: () => { settled(); resolve() }, reject: error => { settled(); reject(error) } }
   deadline.addEventListener("abort", abort, { once: true })
   // In the set from the start: a closing socket or `destroy` ends the wait too.
   this.waiters.add(waiter)
   // A save shares the bucket with background barriers and waits its turn.
   const take = () => {
    const wait = this.requestDelay()
    if (wait) { turn = setTimeout(take, wait); return }
    this.flushUpdates()
    this.request(waiter)
   }
   take()
  })
 }
 setEditable(editable: boolean) {
  this.canEdit = editable
  // Work dropped from the queue while editing was off: the socket never had it.
  if (!editable || !this.answered || !this.pending.some(item => item.seq > this.sent)) return
  this.queuedUpdates = this.pending.map(item => item.update)
  this.updateTimer ??= setTimeout(() => this.flushUpdates(), 75)
 }
 /** The layer this person has selected, for the others to see. Never a document change. */
 /** One layer goes out as its ID, several (#5217) as a list of IDs, none as null. */
 setSelection(ids: string | readonly string[] | null) {
  const valid = [...new Set(typeof ids === "string" ? [ids] : ids ?? [])].filter(id => typeof id === "string" && id && id.length <= 128).slice(0, 200)
  const next = valid.length === 0 ? null : valid.length === 1 ? valid[0] : valid
  if (JSON.stringify(this.awareness.getLocalState()?.selection ?? null) !== JSON.stringify(next)) this.awareness.setLocalStateField("selection", next)
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
   this.tokens = REQUEST_BURST
   this.refilled = performance.now()
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
       if (reply?.waiter) { this.waiters.delete(reply.waiter); reply.waiter.resolve() }
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
  for (const waiter of this.waiters) waiter.reject(Object.assign(new Error(message),{status}))
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
  for (const waiter of this.waiters) waiter.reject(new Error("Canvas closed before the version was saved."))
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
