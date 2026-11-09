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

export interface PresencePeer {
  client: string
  userId: string
  name: string
  color: string
  /** How many of this person's tabs are on the document; the server sends one entry per person. */
  tabs: number
  anchor?: Y.RelativePosition
  head?: Y.RelativePosition
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
    const anchor = decodePosition(peer.anchor), head = decodePosition(peer.head)
    peers.push({ client: peer.client, userId: peer.user_id, name: peer.name.trim().slice(0, 80), color: peerColor(Number(peer.color)), tabs: Number.isSafeInteger(peer.tabs) && Number(peer.tabs) > 0 ? Number(peer.tabs) : 1, ...(anchor && head ? { anchor, head } : {}) })
  }
  const interval = Number(reply.interval_ms)
  return { peers, color: typeof reply.color === "number" ? peerColor(reply.color) : undefined, intervalMs: Number.isFinite(interval) ? Math.min(Math.max(interval, 500), 5000) : 1000 }
}

/** One line per person (a person with two tabs is one entry), in the server's order. */
export function presentPeople(peers: PresencePeer[]): PresencePeer[] {
  const seen = new Set<string>()
  return peers.filter(peer => !seen.has(peer.userId) && Boolean(seen.add(peer.userId)))
}
