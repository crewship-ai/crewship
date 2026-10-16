"use client"
import { useCallback, useEffect, useMemo, useRef, useState } from "react"
import { usePresenceTransportSafe, useRealtimeChannelSafe, useRealtimeEventSafe, type RealtimeEvent, type ContentPresenceCursor } from "@/hooks/use-realtime"
export type ContentPresenceContext = { kind: "artifact"; pageId: string; revision: number } | { kind: "board"; pageId: string; boardSeq: number }
export interface PresenceMember { display_name?: string; connection_id: string; user_id: string; revision: number | null; board_seq: number | null; cursor: ContentPresenceCursor | null }
function membersFrom(payload: Record<string, unknown>): PresenceMember[] | null {
  if (payload.schema_version !== 1 || !Array.isArray(payload.members) || payload.members.length > 64) return null
  const result: PresenceMember[] = []
  for (const candidate of payload.members) {
    if (!candidate || typeof candidate !== "object") return null
    const item = candidate as Record<string, unknown>
    if (typeof item.connection_id !== "string" || !item.connection_id || typeof item.user_id !== "string" || !item.user_id) return null
    const revision = item.revision
    const seq = item.board_seq
    if (revision !== null && (!Number.isSafeInteger(revision) || (revision as number) < 1)) return null
    if (seq !== null && (!Number.isSafeInteger(seq) || (seq as number) < 0)) return null
    let cursor: ContentPresenceCursor | null = null
    if (item.cursor && typeof item.cursor === "object") {
      const point = item.cursor as Record<string, unknown>
      if (typeof point.x === "number" && Number.isFinite(point.x) && Math.abs(point.x) <= 1000000 && typeof point.y === "number" && Number.isFinite(point.y) && Math.abs(point.y) <= 1000000) {
        if (point.coordinate_space === "board") cursor = { x: point.x, y: point.y, coordinate_space: "board" }
        else if (point.coordinate_space === "viewport" && typeof point.viewport_width === "number" && point.viewport_width > 0 && point.viewport_width <= 16384 && typeof point.viewport_height === "number" && point.viewport_height > 0 && point.viewport_height <= 16384 && point.x >= 0 && point.x <= point.viewport_width && point.y >= 0 && point.y <= point.viewport_height) cursor = { x: point.x, y: point.y, coordinate_space: "viewport", viewport_width: point.viewport_width, viewport_height: point.viewport_height }
      }
    }
    const name = typeof item.display_name === "string" && item.display_name.length <= 512 && Array.from(item.display_name).length <= 128 && !/[\p{Cc}\p{Cf}]/u.test(item.display_name) ? item.display_name.trim() : undefined
    result.push({ display_name: name, cursor, connection_id: item.connection_id, user_id: item.user_id, revision: revision as number | null, board_seq: seq as number | null })
  }
  return result
}
const emptyMembers: PresenceMember[] = []
export function useContentPresence(context: ContentPresenceContext, enabled = true) {
  const valid = Boolean(context.pageId && (context.kind === "artifact" ? Number.isSafeInteger(context.revision) && context.revision > 0 : Number.isSafeInteger(context.boardSeq) && context.boardSeq >= 0))
  const channel = enabled && valid ? `${context.kind}:${context.pageId}` : null
  const { status, sendPresence, currentUserId } = usePresenceTransportSafe()
  const cursor = useRef<ContentPresenceCursor | undefined>(undefined)
  const lastSent = useRef(0)
  const [stale, setStale] = useState(false)
  const [snapshot, setSnapshot] = useState<{ channel: string; members: PresenceMember[]; received: number } | null>(null)
  useRealtimeChannelSafe(channel)
  const receive = useCallback((event: RealtimeEvent) => {
    if (!channel || event.channel !== channel || status !== "connected") return
    const members = membersFrom(event.payload)
    if (members) { setStale(false); setSnapshot({ channel, members, received: Date.now() }) }
  }, [channel, status])
  useRealtimeEventSafe("presence.snapshot", receive)
  const position = context.kind === "artifact" ? context.revision : context.boardSeq
  useEffect(() => {
    setSnapshot(null); setStale(false)
  }, [channel, status])
  useEffect(() => { cursor.current = undefined }, [channel, position])
  useEffect(() => {
    if (!channel || !sendPresence || status !== "connected") return
    const heartbeat = () => {
      if (document.visibilityState === "hidden") return
      sendPresence(channel, context.kind === "artifact" ? { schema_version: 1, revision: position, ...(cursor.current ? { cursor: cursor.current } : {}) } : { schema_version: 1, board_seq: position, ...(cursor.current ? { cursor: cursor.current } : {}) })
    }
    heartbeat()
    const timer = setInterval(heartbeat, 5000)
    document.addEventListener("visibilitychange", heartbeat)
    return () => { clearInterval(timer); document.removeEventListener("visibilitychange", heartbeat) }
  }, [channel, sendPresence, status, context.kind, position])
  useEffect(() => {
    if (!channel || status !== "connected") return
    const timer = setTimeout(() => { setSnapshot(null); setStale(true) }, 20000)
    return () => clearTimeout(timer)
  }, [channel, status, snapshot?.received])
  const visible = status === "connected" && snapshot?.channel === channel ? snapshot.members : emptyMembers
  const people = new Set(visible.map(item => item.user_id)).size
  const here = new Set(visible.filter(item => context.kind === "artifact" ? item.revision === position : item.board_seq === position).map(item => item.user_id)).size
  const setCursor = useCallback((point: ContentPresenceCursor) => {
    if (!channel || !sendPresence || status !== "connected" || document.visibilityState === "hidden" || context.kind !== "board" || point.coordinate_space !== "board" || !Number.isFinite(point.x) || !Number.isFinite(point.y) || Math.abs(point.x) > 1000000 || Math.abs(point.y) > 1000000) return
    cursor.current = { x: point.x, y: point.y, coordinate_space: "board" }
    if (Date.now() - lastSent.current < 150) return
    lastSent.current = Date.now()
    sendPresence(channel, { schema_version: 1, board_seq: position, cursor: cursor.current })
  }, [channel, sendPresence, status, context.kind, position])
  const hereMembers = useMemo(() => visible.filter(item => context.kind === "artifact" ? item.revision === position : item.board_seq === position), [visible, context.kind, position])
  return { allMembers: visible, members: hereMembers, currentUserId, setCursor, available: Boolean(channel && status === "connected" && sendPresence && !stale), confirmed: Boolean(snapshot && snapshot.channel === channel && status === "connected"), people, here }
}
