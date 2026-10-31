import * as Y from "yjs"
import * as sync from "y-protocols/sync"
import { Awareness, applyAwarenessUpdate, encodeAwarenessUpdate, removeAwarenessStates } from "y-protocols/awareness"
import * as encoding from "lib0/encoding"
import * as decoding from "lib0/decoding"

export type DesignConnection = "connecting" | "connected" | "offline" | "denied" | "rejected" | "quota"
export type DesignAdmission = "ready" | "denied" | "retry"
export interface DesignPeer { clientId: number; name: string; color: string; cursor?: { x: number; y: number } }
const REMOTE = "design-server"

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
 private waiters = new Set<{ resolve: () => void; reject: (error: Error) => void; timer: ReturnType<typeof setTimeout> }>()
 private changeListener: (update: Uint8Array, origin: unknown) => void
 private awarenessListener: (changes: { added: number[]; updated: number[]; removed: number[] }, origin: unknown) => void

 constructor(readonly doc: Y.Doc, private url: string, editable: boolean, user: { name: string; color: string }, private status: (status: DesignConnection) => void, private dirty: () => void, private prepare?: () => Promise<DesignAdmission>) {
  this.canEdit = editable
  this.admittedEdit = editable
  this.awareness = new Awareness(doc)
  this.awareness.setLocalStateField("user", user)
  this.changeListener = (update, origin) => {
   if (origin === REMOTE || !this.canEdit) return
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
  this.send(encoding.toUint8Array(encoder))
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
   const waiter = { resolve, reject, timer: setTimeout(() => { this.waiters.delete(waiter); reject(new Error("The canvas is still reconnecting. Your changes remain in this browser.")) }, 5000) }
   this.waiters.add(waiter)
   const encoder = encoding.createEncoder()
   encoding.writeVarUint(encoder, 0)
   sync.writeSyncStep1(encoder, this.doc)
   this.send(encoding.toUint8Array(encoder))
  })
 }
 setEditable(editable: boolean) { this.canEdit = editable }
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
   const encoder = encoding.createEncoder()
   encoding.writeVarUint(encoder, 0)
   sync.writeSyncStep1(encoder, this.doc)
   this.send(encoding.toUint8Array(encoder))
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
      }
     } else if (subtype === sync.messageYjsSyncStep2 || subtype === sync.messageYjsUpdate) {
      sync.readSyncStep2(decoder, this.doc, REMOTE)
      this.acknowledged = true
      if (subtype === sync.messageYjsSyncStep2) {
       for (const waiter of this.waiters) { clearTimeout(waiter.timer); waiter.resolve() }
       this.waiters.clear()
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
  this.awareness.setLocalState(null)
  this.socket?.close()
  this.doc.off("update", this.changeListener)
  this.awareness.off("update", this.awarenessListener)
  this.awareness.destroy()
 }
}
