"use client"

import {
  createContext,
  useCallback,
  useContext,
  useEffect,
  useRef,
  useState,
} from "react"
import { useWebSocket, type WSMessage, type WSStatus } from "@/hooks/use-websocket"
import { useWorkspace } from "@/hooks/use-workspace"

export type RealtimeEventType =
  | "run.started"
  | "run.completed"
  | "run.failed"
  | "agent.status"
  | "assignment.updated"
  | "escalation.created"
  | "escalation.resolved"
  | "mission.updated"
  | "task.updated"
  | "peer_conversation.updated"
  | "agent.log"

export interface RealtimeEvent {
  type: RealtimeEventType
  payload: Record<string, any>
  timestamp: Date
}

type EventCallback = (event: RealtimeEvent) => void

interface RealtimeContextValue {
  status: WSStatus
  subscribe: (eventType: RealtimeEventType, callback: EventCallback) => () => void
  lastEvent: RealtimeEvent | null
}

const RealtimeContext = createContext<RealtimeContextValue | null>(null)

function getWsUrl(): string {
  if (process.env.NEXT_PUBLIC_WS_URL) return process.env.NEXT_PUBLIC_WS_URL
  if (typeof window === "undefined") return "ws://localhost:8080/ws"
  const proto = window.location.protocol === "https:" ? "wss:" : "ws:"
  const host = window.location.port === "3001"
    ? window.location.hostname + ":8080"
    : window.location.host
  return `${proto}//${host}/ws`
}

export function RealtimeProvider({ children }: { children: React.ReactNode }) {
  const { workspaceId } = useWorkspace()
  const [token, setToken] = useState<string | null>(null)
  const [lastEvent, setLastEvent] = useState<RealtimeEvent | null>(null)
  const listenersRef = useRef<Map<string, Set<EventCallback>>>(new Map())

  useEffect(() => {
    let cancelled = false
    fetch("/api/v1/ws-token", { credentials: "include" })
      .then((res) => (res.ok ? res.json() : null))
      .then((data) => {
        if (!cancelled && data?.token) setToken(data.token)
      })
      .catch(() => {})
    return () => { cancelled = true }
  }, [])

  const handleMessage = useCallback(
    (msg: WSMessage) => {
      const validTypes: Set<string> = new Set([
        "run.started", "run.completed", "run.failed",
        "agent.status", "assignment.updated", "escalation.created",
        "escalation.resolved", "mission.updated", "task.updated",
        "peer_conversation.updated", "agent.log",
      ])
      if (!validTypes.has(msg.type)) return

      const event: RealtimeEvent = {
        type: msg.type as RealtimeEventType,
        payload: (typeof msg.payload === "object" && msg.payload !== null
          ? msg.payload as Record<string, string>
          : {}),
        timestamp: new Date(),
      }
      setLastEvent(event)

      const callbacks = listenersRef.current.get(msg.type)
      if (callbacks) {
        for (const cb of callbacks) {
          try { cb(event) } catch { /* prevent subscriber errors from breaking others */ }
        }
      }
    },
    [],
  )

  const { status, send } = useWebSocket({
    url: getWsUrl(),
    token,
    onMessage: handleMessage,
  })

  // Subscribe to workspace channel when connected
  useEffect(() => {
    if (status !== "connected" || !workspaceId) return
    send({ type: "subscribe", channel: `workspace:${workspaceId}` })
    return () => {
      send({ type: "unsubscribe", channel: `workspace:${workspaceId}` })
    }
  }, [status, workspaceId, send])

  const subscribe = useCallback(
    (eventType: RealtimeEventType, callback: EventCallback): (() => void) => {
      if (!listenersRef.current.has(eventType)) {
        listenersRef.current.set(eventType, new Set())
      }
      listenersRef.current.get(eventType)!.add(callback)
      return () => {
        listenersRef.current.get(eventType)?.delete(callback)
      }
    },
    [],
  )

  return (
    <RealtimeContext.Provider value={{ status, subscribe, lastEvent }}>
      {children}
    </RealtimeContext.Provider>
  )
}

export function useRealtime(): RealtimeContextValue {
  const ctx = useContext(RealtimeContext)
  if (!ctx) {
    throw new Error("useRealtime must be used within a RealtimeProvider")
  }
  return ctx
}

/**
 * Subscribe to a specific realtime event type.
 * The callback is called whenever the event fires.
 * Returns the latest event of this type (or null).
 */
export function useRealtimeEvent(
  eventType: RealtimeEventType,
  callback: EventCallback,
): void {
  const { subscribe } = useRealtime()
  const callbackRef = useRef(callback)
  useEffect(() => { callbackRef.current = callback }, [callback])

  useEffect(() => {
    return subscribe(eventType, (event) => callbackRef.current(event))
  }, [eventType, subscribe])
}
