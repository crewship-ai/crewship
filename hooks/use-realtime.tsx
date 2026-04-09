"use client"

import {
  createContext,
  useCallback,
  useContext,
  useEffect,
  useMemo,
  useRef,
  useState,
} from "react"
import { useWebSocket, type WSMessage, type WSStatus } from "@/hooks/use-websocket"
import { useWorkspace } from "@/hooks/use-workspace"

/** All supported real-time event types broadcast over the workspace WebSocket channel. */
export type RealtimeEventType =
  | "run.started"
  | "run.completed"
  | "run.failed"
  | "agent.status"
  | "agent.created"
  | "agent.updated"
  | "agent.deleted"
  | "assignment.updated"
  | "escalation.created"
  | "escalation.resolved"
  | "mission.updated"
  | "task.updated"
  | "peer_conversation.updated"
  | "crew.created"
  | "crew.updated"
  | "crew.deleted"
  | "agent.log"
  | "file.event"
  | "container.stats"

/** A real-time event received from the WebSocket, with typed payload and timestamp. */
export interface RealtimeEvent {
  type: RealtimeEventType
  payload: Record<string, any>
  timestamp: Date
}

type EventCallback = (event: RealtimeEvent) => void

interface RealtimeContextValue {
  status: WSStatus
  subscribe: (eventType: RealtimeEventType, callback: EventCallback) => () => void
  subscribeChannel: (channel: string) => () => void
}

const VALID_REALTIME_TYPES: Set<string> = new Set([
  "run.started", "run.completed", "run.failed",
  "agent.status", "agent.created", "agent.updated", "agent.deleted",
  "assignment.updated", "escalation.created",
  "escalation.resolved", "mission.updated", "task.updated",
  "peer_conversation.updated", "crew.created", "crew.updated", "crew.deleted",
  "agent.log", "file.event", "container.stats",
])

const RealtimeContext = createContext<RealtimeContextValue | null>(null)

function getWsUrl(): string {
  // During SSR window is undefined — return empty string so useWebSocket
  // skips connecting. The client-side re-render will compute the real URL.
  if (typeof window === "undefined") return ""
  const proto = window.location.protocol === "https:" ? "wss:" : "ws:"
  // Always use the same host:port as the page.  In dev mode the custom dev
  // server (dev-server.mjs) proxies /ws to the Go backend, so there is no
  // need to redirect to a different port.  In production the Go binary
  // serves both the static frontend and the WebSocket on the same port.
  return `${proto}//${window.location.host}/ws`
}

/**
 * Context provider that manages a single WebSocket connection for real-time events.
 * Auto-subscribes to the workspace channel and re-subscribes component channels after reconnect.
 */
export function RealtimeProvider({ children }: { children: React.ReactNode }) {
  const { workspaceId } = useWorkspace()
  const [token, setToken] = useState<string | null>(null)
  const listenersRef = useRef<Map<string, Set<EventCallback>>>(new Map())
  const activeChannelsRef = useRef<Set<string>>(new Set())
  const statusRef = useRef<string>("disconnected")

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
      if (!VALID_REALTIME_TYPES.has(msg.type)) return

      const event: RealtimeEvent = {
        type: msg.type as RealtimeEventType,
        payload: (typeof msg.payload === "object" && msg.payload !== null
          ? msg.payload as Record<string, string>
          : {}),
        timestamp: new Date(),
      }
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

  useEffect(() => { statusRef.current = status }, [status])

  // Subscribe to workspace channel when connected
  useEffect(() => {
    if (status !== "connected" || !workspaceId) return
    send({ type: "subscribe", channel: `workspace:${workspaceId}` })
    // Re-subscribe any component-registered channels after reconnect
    for (const ch of activeChannelsRef.current) {
      send({ type: "subscribe", channel: ch })
    }
    return () => {
      send({ type: "unsubscribe", channel: `workspace:${workspaceId}` })
    }
  }, [status, workspaceId, send])

  const subscribeChannel = useCallback(
    (channel: string): (() => void) => {
      activeChannelsRef.current.add(channel)
      if (status === "connected") {
        send({ type: "subscribe", channel })
      }
      return () => {
        activeChannelsRef.current.delete(channel)
        if (statusRef.current === "connected") {
          send({ type: "unsubscribe", channel })
        }
      }
    },
    [status, send],
  )

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

  const contextValue = useMemo(
    () => ({ status, subscribe, subscribeChannel }),
    [status, subscribe, subscribeChannel],
  )

  return (
    <RealtimeContext.Provider value={contextValue}>
      {children}
    </RealtimeContext.Provider>
  )
}

/** Access the real-time event system (status, subscribe, subscribeChannel). Must be used within RealtimeProvider. */
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

/** Subscribe to a WebSocket channel (e.g. "agent:{id}") for the lifetime of the calling component. */
export function useRealtimeChannel(channel: string | null): void {
  const { subscribeChannel } = useRealtime()
  useEffect(() => {
    if (!channel) return
    return subscribeChannel(channel)
  }, [channel, subscribeChannel])
}
