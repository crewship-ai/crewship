"use client"

import { useCallback, useEffect, useRef, useState } from "react"
import { z } from "zod"

/** WebSocket connection lifecycle status. */
export type WSStatus = "connecting" | "connected" | "disconnected" | "error"

const wsMessageSchema = z.object({
  type: z.string(),
  channel: z.string().optional(),
  payload: z.union([z.string(), z.record(z.string(), z.unknown())]).optional(),
}).passthrough()

/** A parsed WebSocket message with type, optional channel, and optional payload. */
export type WSMessage = z.infer<typeof wsMessageSchema>

interface UseWebSocketOptions {
  url: string
  /** Async callback that returns the current WS ticket. Called on
   *  each (re)connect so a stale ticket is never reused after a backend
   *  restart. Return null to signal "auth no longer valid"; the hook
   *  emits an auth:session-expired event and stops retrying. */
  getToken: () => Promise<string | null>
  onMessage?: (msg: WSMessage) => void
  onStatusChange?: (status: WSStatus) => void
}

/** Exponential backoff with jitter: min(base * 2^attempt, max) + random(0, jitter) */
function backoffDelay(attempt: number): number {
  const base = 1000
  const max = 30000
  const jitter = 1000
  return Math.min(base * Math.pow(2, attempt), max) + Math.random() * jitter
}

/** Maximum reconnect attempts before we give up and surface a terminal
 *  failure. With the backoff schedule above, 8 attempts ≈ 1+2+4+8+16+30
 *  +30+30 = ~2 minutes. After that, an "is the backend down?" status
 *  is more useful than yet another silent retry. */
const MAX_RECONNECT_ATTEMPTS = 8

/** Custom WS close code emitted by the server when it detects a revoked
 *  session mid-connection (see internal/ws/hub.go watchSessionRevocation).
 *  4401 is in the application range (4000–4999) — RFC 6455 reserves
 *  these for application protocols. */
const CLOSE_CODE_SESSION_REVOKED = 4401

/** Same event name that lib/api-fetch fires on terminal 401. The
 *  AuthProvider listens for it and hard-redirects to /login. We
 *  redispatch from here so a WS-detected revocation has the same
 *  effect as an HTTP-detected one. */
const AUTH_EVENT = "auth:session-expired"

function emitSessionExpired(reason: string) {
  if (typeof window === "undefined") return
  window.dispatchEvent(new CustomEvent(AUTH_EVENT, { detail: { reason } }))
}

/**
 * Managed WebSocket connection with token-aware automatic reconnection.
 *
 * Validates incoming messages against a Zod schema before dispatching
 * to the onMessage callback. Reconnects with exponential backoff +
 * jitter, but with two short-circuits compared to the previous version:
 *   - getToken returning null (e.g. /ws-token returned 401) → emit
 *     session-expired and stop retrying.
 *   - Close code 4401 (server-side session revoke) → emit session-
 *     expired and stop retrying.
 *   - Reaching MAX_RECONNECT_ATTEMPTS → emit session-expired (the
 *     backend is unreachable in a way that probably means the user
 *     should re-authenticate when it comes back).
 */
export function useWebSocket({
  url,
  getToken,
  onMessage,
  onStatusChange,
}: UseWebSocketOptions) {
  const [status, setStatus] = useState<WSStatus>("disconnected")
  const wsRef = useRef<WebSocket | null>(null)
  const reconnectAttemptsRef = useRef(0)
  const reconnectTimerRef = useRef<ReturnType<typeof setTimeout> | undefined>(undefined)
  const disconnectingRef = useRef(false)
  const terminatedRef = useRef(false)

  // Use refs for callbacks to prevent reconnection loops when consumers
  // pass non-memoized functions.
  const onMessageRef = useRef(onMessage)
  const onStatusChangeRef = useRef(onStatusChange)
  const getTokenRef = useRef(getToken)
  useEffect(() => { onMessageRef.current = onMessage }, [onMessage])
  useEffect(() => { onStatusChangeRef.current = onStatusChange }, [onStatusChange])
  useEffect(() => { getTokenRef.current = getToken }, [getToken])

  const updateStatus = useCallback((s: WSStatus) => {
    setStatus(s)
    onStatusChangeRef.current?.(s)
  }, [])

  const terminate = useCallback((reason: string) => {
    terminatedRef.current = true
    if (reconnectTimerRef.current) {
      clearTimeout(reconnectTimerRef.current)
      reconnectTimerRef.current = undefined
    }
    updateStatus("error")
    emitSessionExpired(reason)
  }, [updateStatus])

  const connect = useCallback(async () => {
    if (terminatedRef.current) return

    // Refresh-fetch the ticket on every (re)connect. A stale token
    // from before a backend restart would 401 the upgrade and trip
    // an infinite loop; re-fetching forces apiFetch to either rotate
    // the access cookie or surface session_expired.
    const token = await getTokenRef.current()
    if (terminatedRef.current) return
    if (!token) {
      // /ws-token returned null → apiFetch already emitted the auth
      // event; we just need to stop trying.
      terminate("session_expired")
      return
    }

    const effectiveUrl = url || resolveWsUrl()
    if (!effectiveUrl) return
    disconnectingRef.current = false
    if (reconnectTimerRef.current) {
      clearTimeout(reconnectTimerRef.current)
      reconnectTimerRef.current = undefined
    }

    // Note: token is passed as query parameter because browser WebSocket API
    // does not support custom headers. The token is a short-lived JWE and the
    // connection uses WSS in production, mitigating URL-based leakage risks.
    const wsUrlObj = new URL(effectiveUrl, window.location.origin)
    wsUrlObj.searchParams.set("token", token)
    const ws = new WebSocket(wsUrlObj.toString())
    wsRef.current = ws

    updateStatus("connecting")

    ws.onopen = () => {
      reconnectAttemptsRef.current = 0
      updateStatus("connected")
    }

    ws.onmessage = (event) => {
      try {
        const parsed = JSON.parse(event.data)
        const result = wsMessageSchema.safeParse(parsed)
        if (!result.success) return
        // Server-side revoke watcher sends this frame just before
        // closing. Treat it the same as close code 4401: hard-redirect.
        if (result.data.type === "session_revoked") {
          terminate("session_revoked")
          ws.close()
          return
        }
        onMessageRef.current?.(result.data)
      } catch {
        // non-JSON message, ignore
      }
    }

    ws.onerror = () => {
      updateStatus("error")
    }

    ws.onclose = (event) => {
      wsRef.current = null
      updateStatus("disconnected")

      if (event.code === CLOSE_CODE_SESSION_REVOKED) {
        terminate("session_revoked")
        return
      }
      if (disconnectingRef.current || terminatedRef.current) return

      const attempts = reconnectAttemptsRef.current
      if (attempts >= MAX_RECONNECT_ATTEMPTS) {
        terminate("session_expired")
        return
      }
      const delay = backoffDelay(attempts)
      reconnectAttemptsRef.current = attempts + 1
      reconnectTimerRef.current = setTimeout(() => { void connect() }, delay)
    }
  }, [url, updateStatus, terminate])

  const disconnect = useCallback(() => {
    disconnectingRef.current = true
    if (reconnectTimerRef.current) {
      clearTimeout(reconnectTimerRef.current)
    }
    wsRef.current?.close()
    wsRef.current = null
  }, [])

  const send = useCallback(
    (msg: WSMessage) => {
      if (wsRef.current?.readyState === WebSocket.OPEN) {
        wsRef.current.send(JSON.stringify(msg))
      }
    },
    [],
  )

  useEffect(() => {
    void connect()
    return () => disconnect()
  }, [connect, disconnect])

  return { status, send, disconnect, reconnect: () => { void connect() } }
}

/** Compute WS URL from window.location at runtime. Always correct regardless
 *  of SSR, env var caching, or deployment topology.  Uses the same host:port
 *  as the page — in dev mode, dev-server.mjs proxies /ws to the Go backend. */
function resolveWsUrl(): string {
  if (typeof window === "undefined") return ""
  const proto = window.location.protocol === "https:" ? "wss:" : "ws:"
  return `${proto}//${window.location.host}/ws`
}
