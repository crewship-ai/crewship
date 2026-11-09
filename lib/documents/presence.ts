import * as Y from "yjs"

/**
 * Docs presence (#5166) on the wire. The server names people and assigns
 * their colour index per document; the client only sends which tab it is and
 * its caret and range as two Yjs relative positions, which follow the text
 * the caret is in rather than an offset that every edit would move.
 */

/** Peer colours, indexed by the server's per-document assignment. Labels on them use white text through `readablePeerFill`. */
export const PEER_COLORS = ["#2563eb", "#db2777", "#059669", "#d97706", "#7c3aed", "#0891b2", "#dc2626", "#4d7c0f"] as const

export function peerColor(index: number): string {
  return PEER_COLORS[Number.isSafeInteger(index) && index >= 0 ? index % PEER_COLORS.length : 0]
}

/**
 * Where a person is on the Canvas view (#5149 C06): their pointer in canvas
 * (world) coordinates, absent once it leaves the canvas, and the cards they
 * selected. Sent with the same heartbeat as the caret and as ephemeral.
 */
export interface CanvasPresence {
  pointer?: { x: number; y: number }
  selection: string[]
}

export interface PresencePeer {
  client: string
  userId: string
  name: string
  color: string
  anchor?: Y.RelativePosition
  head?: Y.RelativePosition
  canvas?: CanvasPresence
}

/** The server's bounds: the surface's coordinate limit and at most 8 selected cards with surface IDs. */
export const CANVAS_PRESENCE_LIMITS = Object.freeze({ coordinate: 1_000_000, selection: 8 })
const CARD_ID = /^[a-zA-Z0-9_-]{1,80}$/
const coordinate = (value: unknown) => typeof value === "number" && Number.isFinite(value) && Math.abs(value) <= CANVAS_PRESENCE_LIMITS.coordinate

/** A peer's canvas presence from the wire, or nothing when it is absent or out of bounds. */
export function parseCanvasPresence(value: unknown): CanvasPresence | undefined {
  if (!value || typeof value !== "object" || Array.isArray(value)) return undefined
  const raw = value as Record<string, unknown>
  const point = raw.pointer as Record<string, unknown> | undefined
  const pointer = point && typeof point === "object" && coordinate(point.x) && coordinate(point.y) ? { x: point.x as number, y: point.y as number } : undefined
  const ids = Array.isArray(raw.selection) ? raw.selection : []
  const selection = ids.length <= CANVAS_PRESENCE_LIMITS.selection && ids.every(id => typeof id === "string" && CARD_ID.test(id)) ? [...new Set(ids as string[])] : []
  return pointer || selection.length ? { ...(pointer ? { pointer } : {}), selection } : undefined
}

/** This tab's canvas presence for a heartbeat body: rounded to whole canvas units, bounded, or nothing to send. */
export function canvasPresenceBody(canvas: CanvasPresence | null | undefined): { canvas?: { pointer?: { x: number; y: number }; selection?: string[] } } {
  if (!canvas) return {}
  const limit = CANVAS_PRESENCE_LIMITS.coordinate
  const clamp = (value: number) => Math.max(-limit, Math.min(limit, Math.round(value)))
  const pointer = canvas.pointer && Number.isFinite(canvas.pointer.x) && Number.isFinite(canvas.pointer.y) ? { x: clamp(canvas.pointer.x), y: clamp(canvas.pointer.y) } : undefined
  const selection = [...new Set(canvas.selection.filter(id => CARD_ID.test(id)))].slice(0, CANVAS_PRESENCE_LIMITS.selection)
  if (!pointer && !selection.length) return {}
  return { canvas: { ...(pointer ? { pointer } : {}), ...(selection.length ? { selection } : {}) } }
}

export interface PresenceReply {
  peers: PresencePeer[]
  color?: string
  intervalMs: number
}

const toBase64 = (bytes: Uint8Array) => { let text = ""; for (const byte of bytes) text += String.fromCharCode(byte); return btoa(text) }
const fromBase64 = (value: string) => Uint8Array.from(atob(value), char => char.charCodeAt(0))

export function encodePosition(position: Y.RelativePosition): string {
  return toBase64(Y.encodeRelativePosition(position))
}

export function decodePosition(value: unknown): Y.RelativePosition | undefined {
  if (typeof value !== "string" || !value || value.length > 512) return undefined
  try { return Y.decodeRelativePosition(fromBase64(value)) } catch { return undefined }
}

const MAX_PEERS = 64

/** Reads a heartbeat reply; anything malformed is dropped, never shown. */
export function parsePresenceReply(value: unknown): PresenceReply {
  const reply = (value && typeof value === "object" ? value : {}) as Record<string, unknown>
  const peers: PresencePeer[] = []
  for (const raw of Array.isArray(reply.peers) ? reply.peers.slice(0, MAX_PEERS) : []) {
    if (!raw || typeof raw !== "object") continue
    const peer = raw as Record<string, unknown>
    if (typeof peer.client !== "string" || typeof peer.user_id !== "string" || typeof peer.name !== "string" || !peer.name.trim()) continue
    const anchor = decodePosition(peer.anchor), head = decodePosition(peer.head), canvas = parseCanvasPresence(peer.canvas)
    peers.push({ client: peer.client, userId: peer.user_id, name: peer.name.trim().slice(0, 80), color: peerColor(Number(peer.color)), ...(anchor && head ? { anchor, head } : {}), ...(canvas ? { canvas } : {}) })
  }
  const interval = Number(reply.interval_ms)
  return { peers, color: typeof reply.color === "number" ? peerColor(reply.color) : undefined, intervalMs: Number.isFinite(interval) ? Math.min(Math.max(interval, 500), 5000) : 1000 }
}

/** One line per person (a person with two tabs is one entry), in the server's order. */
export function presentPeople(peers: PresencePeer[]): PresencePeer[] {
  const seen = new Set<string>()
  return peers.filter(peer => !seen.has(peer.userId) && Boolean(seen.add(peer.userId)))
}
