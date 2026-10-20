"use client"

import { resolveWsBase } from "@/lib/server-base"
import { useCallback, useEffect, useRef, useState } from "react"
import { z } from "zod"
import { broadcastSessionExpired } from "@/lib/api-fetch"

/** WebSocket connection lifecycle status.
 *
 * `"connecting"` covers the first connect AND the fast backoff after a drop:
 * a retry is already scheduled, so a restart reads as "reconnecting", not as a
 * failure. `reason` (below) tells the two apart.
 *
 * `"offline"` is the not-yet-connected state past the fast backoff budget, or
 * while the browser says it has no network. It is not terminal: the hook keeps
 * retrying every 30 s and at once on `online`, a visible tab, window focus or
 * `retry()`.
 *
 * `"error"` is terminal and means the session is dead (null ticket, close code
 * 4401); the app redirects to /login. A transport failure never ends here.
 *
 * `"disconnected"` is a socket nobody asked for: before the first connect, or
 * after the consumer disabled it.
 *
 * `"unavailable"` is terminal and is not an outage: realtime does not exist for
 * this session (a restricted account, whose allowlist refuses `/ws-token` with
 * 404). Nothing retries and no "reconnecting" banner is shown for it. */
export type WSStatus = "connecting" | "connected" | "disconnected" | "offline" | "error" | "unavailable"

/** Why a socket that was wanted is not up, or null when nothing is wrong
 *  (connected, first connect, disabled, or terminated).
 *
 *   retrying         a drop or a failed attempt; fast backoff ("connecting")
 *   restart          the server closed with 1012 (service restart) or 1001
 *                    (going away); fast backoff ("connecting")
 *   unreachable      the fast budget is spent; one attempt every 30 s ("offline")
 *   browser-offline  navigator.onLine is false; waits for `online` ("offline") */
export type WSReason = "retrying" | "restart" | "unreachable" | "browser-offline"

/** Whether a wanted socket is down right now: retrying after a drop, offline,
 *  or dead. Not the first connect, and not "unavailable" (no realtime by
 *  design). For surfaces that dim or flag data that may be stale. */
export function isRealtimeDown(status: WSStatus | null | undefined, reason: WSReason | null | undefined): boolean {
  if (status === "offline" || status === "error" || status === "disconnected") return true
  return status === "connecting" && reason != null
}

/**
 * Thrown by a `getToken` callback when the ticket endpoint says realtime is not
 * available to this session at all (404 from `/api/v1/ws-token`). Unlike a
 * transient error it stops the reconnect loop for good, and unlike a null
 * ticket it is not an auth failure, so it does not log the user out.
 */
export class WsUnavailableError extends Error {
  constructor(message = "realtime is not available to this session") {
    super(message)
    this.name = "WsUnavailableError"
  }
}

const wsMessageSchema = z.object({
  type: z.string(),
  channel: z.string().optional(),
  payload: z.union([z.string(), z.record(z.string(), z.unknown())]).optional(),
}).passthrough()

/** A parsed WebSocket message with type, optional channel, and optional payload. */
export type WSMessage = z.infer<typeof wsMessageSchema>

interface UseWebSocketOptions {
  enabled?: boolean
  url: string
  /** Async callback that returns the current WS ticket. Called on
   *  each (re)connect so a stale ticket is never reused after a backend
   *  restart. Return null to signal "auth no longer valid"; the hook
   *  emits an auth:session-expired event and stops retrying. */
  getToken: () => Promise<string | null>
  onMessage?: (msg: WSMessage) => void
  onStatusChange?: (status: WSStatus) => void
  /** Fired every time the socket (re)connects (ws.onopen). Consumers use this
   *  to (re)subscribe to channels and resume any in-flight stream after a
   *  reconnect — a fresh socket has no server-side subscriptions. */
  onConnect?: () => void
}

/** Exponential backoff with jitter: min(base * 2^attempt, max) + random(0, jitter) */
function backoffDelay(attempt: number): number {
  const base = 1000
  const max = 30000
  const jitter = 1000
  return Math.min(base * Math.pow(2, attempt), max) + Math.random() * jitter
}

/** The fast backoff budget. With the schedule above, 8 attempts ≈ 1+2+4+8
 *  +16+30+30+30 = ~2 minutes. Past it the hook does not give up: it reports
 *  "offline" (an "is the backend down?" status is more useful than another
 *  silent "Reconnecting…") and tries every SLOW_RETRY_MS. */
const FAST_RECONNECT_ATTEMPTS = 8

/** The slow retry once the fast budget is spent ("retrying every 30 s"). */
const SLOW_RETRY_MS = 30_000

/** Close codes a server sends when it is restarting on purpose: 1012 (service
 *  restart) and 1001 (going away). The backoff is the same; only the words
 *  change ("Crewship is restarting"). crewshipd's hub sends neither today —
 *  x/net/websocket closes with 1000 and a deploy drops the TCP connection
 *  (1006) — but a proxy in front of it may. */
const PLANNED_RESTART_CODES = new Set([1012, 1001])

/** Window event every useWebSocket instance listens to: one "Retry now" wakes
 *  every socket in the tab, not only the one whose banner was clicked. */
const RETRY_EVENT = "crewship:realtime-retry"

function browserOffline(): boolean {
  return typeof navigator !== "undefined" && navigator.onLine === false
}

/** Custom WS close code emitted by the server when it detects a revoked
 *  session mid-connection (see internal/ws/hub.go watchSessionRevocation).
 *  4401 is in the application range (4000–4999) — RFC 6455 reserves
 *  these for application protocols. */
const CLOSE_CODE_SESSION_REVOKED = 4401

/** Maximum size, in UTF-8 encoded bytes, of an outbound WebSocket frame
 *  this client will send. Set below the server's inbound frame cap
 *  (wsMaxInboundFrameBytes = 64 KiB, internal/ws/hub.go:108) to leave
 *  headroom for the JSON envelope. Exceeding the server cap is not a
 *  graceful rejection: the x/net/websocket Conn returns ErrFrameTooLarge
 *  from Receive, readPump treats that as a plain read error, and the
 *  WHOLE connection is torn down — not just the oversize message.
 *  Pre-flighting client-side (see callers of encodedByteLength below)
 *  turns that silent disconnect into a visible, recoverable error.
 *  Keep this in sync with hub.go's constant if it ever changes. */
export const WS_MAX_OUTBOUND_FRAME_BYTES = 60 * 1024 // 60 KiB — ~4 KiB headroom under the 64 KiB server cap

/** Byte length of `value` once UTF-8 encoded. Deliberately NOT
 *  `value.length`: JS string length counts UTF-16 code units, which
 *  undercounts every multi-byte character (emoji, CJK, combining marks,
 *  …) relative to what actually goes over the wire — e.g. a 4-byte emoji
 *  is length 2 in JS (a surrogate pair) but 4 bytes on the wire. */
export function encodedByteLength(value: string): number {
  return new TextEncoder().encode(value).length
}

// emitSessionExpired delegates to lib/api-fetch's shared emitter so a
// WS-detected revocation reaches not just this tab's AuthProvider but
// every other tab via the BroadcastChannel. Without this, only the
// tab that received the 4401 frame redirected; sibling tabs sat idle
// on stale UI until they themselves tried an HTTP request.
const emitSessionExpired = broadcastSessionExpired

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
 * A transport failure never stops it: past FAST_RECONNECT_ATTEMPTS it
 * reports "offline" and retries every 30 s, and it retries at once on
 * `online`, a tab becoming visible, window focus, or `retry()`. With the
 * browser offline it waits for `online` instead of spending attempts.
 */
export function useWebSocket({
  enabled = true,
  url,
  getToken,
  onMessage,
  onStatusChange,
  onConnect,
}: UseWebSocketOptions) {
  const [status, setStatus] = useState<WSStatus>("disconnected")
  const [reason, setReason] = useState<WSReason | null>(null)
  const reasonRef = useRef<WSReason | null>(null)
  const wsRef = useRef<WebSocket | null>(null)
  const connectionEpochRef = useRef(0)
  const reconnectAttemptsRef = useRef(0)
  const reconnectTimerRef = useRef<ReturnType<typeof setTimeout> | undefined>(undefined)
  const disconnectingRef = useRef(false)
  const terminatedRef = useRef(false)
  const unavailableRef = useRef(false)
  // The last close was 1012/1001; cleared by the next open.
  const plannedRestartRef = useRef(false)
  // Waiting for the browser's `online` event: no timer, no socket.
  const parkedRef = useRef(false)

  // Use refs for callbacks to prevent reconnection loops when consumers
  // pass non-memoized functions.
  const onMessageRef = useRef(onMessage)
  const onStatusChangeRef = useRef(onStatusChange)
  const onConnectRef = useRef(onConnect)
  const getTokenRef = useRef(getToken)
  useEffect(() => { onMessageRef.current = onMessage }, [onMessage])
  useEffect(() => { onStatusChangeRef.current = onStatusChange }, [onStatusChange])
  useEffect(() => { onConnectRef.current = onConnect }, [onConnect])
  useEffect(() => { getTokenRef.current = getToken }, [getToken])

  const updateStatus = useCallback((s: WSStatus, r: WSReason | null = null) => {
    reasonRef.current = r
    setReason(r)
    setStatus(s)
    onStatusChangeRef.current?.(s)
  }, [])

  // Only an actual auth signal (null ticket from /ws-token, close code 4401,
  // in-stream session_revoked frame) stops retrying for good; it also fires
  // session-expired so the AuthProvider hard-redirects to /login. A backend
  // that is unreachable does not: the user's session is presumably still
  // valid, their backend just isn't, and bouncing them to /login (or leaving
  // them on a dead page) on every restart is the UX bug this hook exists
  // to avoid. See scheduleRetry below.
  const terminateAuth = useCallback((reason: string) => {
    terminatedRef.current = true
    if (reconnectTimerRef.current) {
      clearTimeout(reconnectTimerRef.current)
      reconnectTimerRef.current = undefined
    }
    updateStatus("error")
    emitSessionExpired(reason)
  }, [updateStatus])

  // Realtime does not exist for this session: stop for good, no banner, no
  // session-expired. Cleared again if the consumer re-enables the hook (the
  // session's access changed while the app was open).
  const terminateUnavailable = useCallback(() => {
    terminatedRef.current = true
    unavailableRef.current = true
    if (reconnectTimerRef.current) {
      clearTimeout(reconnectTimerRef.current)
      reconnectTimerRef.current = undefined
    }
    updateStatus("unavailable")
  }, [updateStatus])

  // The next attempt after a drop or a failed one. Fast backoff while the
  // budget lasts ("connecting"), then every 30 s ("offline"); with the browser
  // offline, no timer at all until `online` (see the listeners below). The
  // ref breaks the connect ↔ scheduleRetry cycle.
  const connectRef = useRef<(opts?: { announce?: boolean }) => Promise<void>>(async () => {})
  const scheduleRetry = useCallback(() => {
    if (reconnectTimerRef.current) {
      clearTimeout(reconnectTimerRef.current)
      reconnectTimerRef.current = undefined
    }
    if (browserOffline()) {
      parkedRef.current = true
      updateStatus("offline", "browser-offline")
      return
    }
    const attempts = reconnectAttemptsRef.current
    let delay: number
    if (attempts >= FAST_RECONNECT_ATTEMPTS) {
      updateStatus("offline", "unreachable")
      delay = SLOW_RETRY_MS + Math.random() * 1000
    } else {
      updateStatus("connecting", plannedRestartRef.current ? "restart" : "retrying")
      delay = backoffDelay(attempts)
    }
    reconnectAttemptsRef.current = attempts + 1
    reconnectTimerRef.current = setTimeout(() => { reconnectTimerRef.current = undefined; void connectRef.current() }, delay)
  }, [updateStatus])

  // `announce`: say "connecting" for this attempt even in the slow "offline"
  // mode — a person pressed Retry now or the network came back. Automatic
  // slow attempts stay "offline", so the warning does not blink every 30 s.
  const connect = useCallback(async ({ announce = false }: { announce?: boolean } = {}) => {
    if (!enabled) return
    // Only `terminatedRef` is permanent — once we've decided we'll
    // never reconnect (auth dead / past the backoff cap), bail. A set
    // `disconnectingRef` here used to bail too, but that broke the
    // common case of "URL changed, cleanup ran disconnect(), now we
    // want to reconnect": the cleanup left disconnectingRef=true, the
    // next effect's connect saw it and returned, and `new WebSocket`
    // was never called. React 19 strict-mode's mount→cleanup→remount
    // double-pass triggers the same path even on a single URL.
    // Treat entering connect() as the explicit "we want to be
    // connected" signal and clear the flag — the in-flight race
    // (disconnect() called *while* getToken is awaiting) is still
    // caught by the post-await guard below.
    if (terminatedRef.current) return
    disconnectingRef.current = false
    parkedRef.current = false
    const current = reasonRef.current
    if (announce || (current !== "unreachable" && current !== "browser-offline")) {
      updateStatus("connecting", current === "browser-offline" ? "retrying" : current)
    }
    const epoch = ++connectionEpochRef.current

    // Refresh-fetch the ticket on every (re)connect. A stale token
    // from before a backend restart would 401 the upgrade and trip
    // an infinite loop; re-fetching forces apiFetch to either rotate
    // the access cookie or surface session_expired.
    //
    // Wrap in try-catch so that a thrown token-provider (network
    // failure inside apiFetch, fetch rejection, etc.) doesn't
    // escape connect() unhandled and break the reconnect loop.
    // A throw is treated as transient: schedule the next backoff
    // attempt instead of terminating. Real auth failures come
    // through the null-return path below, which apiFetch only
    // takes after the refresh-on-401 round-trip resolved
    // auth_failed.
    let token: string | null
    try {
      token = await getTokenRef.current()
    } catch (err) {
      if (epoch !== connectionEpochRef.current || terminatedRef.current || disconnectingRef.current) return
      if (err instanceof WsUnavailableError) {
        terminateUnavailable()
        return
      }
      scheduleRetry()
      return
    }

    // Re-check after the await: getToken can take a refresh round-trip,
    // and the consumer (or React unmount) might have called disconnect
    // while we were waiting. Without this guard a late-resolving token
    // would clobber disconnectingRef back to false and open a leaked
    // connection that then wouldn't be closed by the original
    // disconnect call.
    if (epoch !== connectionEpochRef.current || terminatedRef.current || disconnectingRef.current) return

    if (!token) {
      // /ws-token returned null → apiFetch already emitted the auth
      // event; we just need to stop trying.
      terminateAuth("session_expired")
      return
    }

    const effectiveUrl = url || resolveWsUrl()
    if (!effectiveUrl) return
    disconnectingRef.current = false
    if (reconnectTimerRef.current) {
      clearTimeout(reconnectTimerRef.current)
      reconnectTimerRef.current = undefined
    }

    // Auth happens post-open, not via a `?token=` query param: the browser
    // WebSocket API can't send custom headers, and a URL-embedded token
    // leaks into proxy/access logs, browser history, and Referer headers.
    // Mirrors hooks/use-terminal.ts and the server side (internal/ws/hub.go
    // authenticateUpgradedConn).
    const wsUrlObj = new URL(effectiveUrl, window.location.origin)
    const ws = new WebSocket(wsUrlObj.toString())
    wsRef.current = ws

    ws.onopen = () => {
      if (wsRef.current !== ws) return
      ws.send(JSON.stringify({ type: "auth", token }))
      reconnectAttemptsRef.current = 0
      plannedRestartRef.current = false
      updateStatus("connected")
      // Re-establish subscriptions / resume in-flight streams on every
      // (re)connect — the new socket starts with no server-side state.
      onConnectRef.current?.()
    }

    ws.onmessage = (event) => {
      if (wsRef.current !== ws) return
      try {
        const parsed = JSON.parse(event.data)
        const result = wsMessageSchema.safeParse(parsed)
        if (!result.success) return
        // Server-side revoke watcher sends this frame just before
        // closing. Treat it the same as close code 4401: hard-redirect.
        if (result.data.type === "session_revoked") {
          terminateAuth("session_revoked")
          ws.close()
          return
        }
        onMessageRef.current?.(result.data)
      } catch {
        // non-JSON message, ignore
      }
    }

    // No status change on error: a browser always follows it with close,
    // which decides between a retry and the end. Setting "error" here made
    // every retried drop flicker through the terminal state first.
    ws.onerror = null

    ws.onclose = (event) => {
      if (wsRef.current !== ws) return
      wsRef.current = null

      if (event.code === CLOSE_CODE_SESSION_REVOKED) {
        terminateAuth("session_revoked")
        return
      }
      if (disconnectingRef.current || terminatedRef.current) return

      if (PLANNED_RESTART_CODES.has(event.code)) plannedRestartRef.current = true
      scheduleRetry()
    }
  }, [url, enabled, updateStatus, scheduleRetry, terminateAuth, terminateUnavailable])
  useEffect(() => { connectRef.current = connect }, [connect])

  const disconnect = useCallback(() => {
    connectionEpochRef.current++
    disconnectingRef.current = true
    parkedRef.current = false
    if (reconnectTimerRef.current) {
      clearTimeout(reconnectTimerRef.current)
      reconnectTimerRef.current = undefined
    }
    const ws = wsRef.current
    wsRef.current = null
    if (ws) {
      // Detach handlers before closing so a delayed onclose event
      // can't fire auto-reconnect or null out wsRef again after a
      // newer connection has taken its place.
      ws.onclose = null
      ws.onerror = null
      ws.onmessage = null
      ws.onopen = null
      try { ws.close() } catch { /* already closed */ }
    }
  }, [])

  // send() used to no-op silently when the socket wasn't OPEN, which meant
  // a message typed/sent while disconnected just vanished with no signal
  // to the caller. It now returns whether the frame was actually handed to
  // the socket, so callers (e.g. the chat composer) can surface "not
  // connected" instead of assuming success.
  const send = useCallback(
    (msg: WSMessage): boolean => {
      if (wsRef.current?.readyState !== WebSocket.OPEN) {
        return false
      }
      wsRef.current.send(JSON.stringify(msg))
      return true
    },
    [],
  )

  useEffect(() => {
    if (enabled) {
      // An "unavailable" verdict belongs to the session as it was. When the
      // consumer turns the socket back on (access changed while open), try
      // again from a clean slate. Auth and transport terminations stay final.
      if (unavailableRef.current) {
        unavailableRef.current = false
        terminatedRef.current = false
        reconnectAttemptsRef.current = 0
      }
      void connect()
    }
    return () => disconnect()
  }, [connect, disconnect, enabled])

  /**
   * Cut a wait short: try now instead of at the next scheduled attempt.
   * Only two states may be cut short — an attempt waiting on its timer, and
   * one parked until the browser is back online. Anything else is a connect
   * in flight (ticket request or socket opening) or an open socket, and a
   * second connect would leak a socket. Never after an auth or an
   * "unavailable" termination.
   *
   * `fresh` restarts the fast budget (the network came back, so the earlier
   * failures say nothing). The budget is otherwise kept, so a slow-mode
   * attempt that fails goes back to "offline" rather than to two more
   * minutes of "Reconnecting…".
   */
  const wake = useCallback(({ fresh = false, announce = false, manual = false }: { fresh?: boolean; announce?: boolean; manual?: boolean }) => {
    if (!enabled || terminatedRef.current) return
    if (reconnectTimerRef.current === undefined && !parkedRef.current) return
    // Focus and a visible tab do not override the browser's own verdict; a
    // person pressing Retry now may (navigator.onLine can be wrong).
    if (!manual && !fresh && browserOffline()) return
    if (reconnectTimerRef.current) {
      clearTimeout(reconnectTimerRef.current)
      reconnectTimerRef.current = undefined
    }
    if (fresh) {
      reconnectAttemptsRef.current = 0
      reasonRef.current = plannedRestartRef.current ? "restart" : "retrying"
    }
    void connect({ announce })
  }, [enabled, connect])

  useEffect(() => {
    if (!enabled || typeof window === "undefined") return
    const onOnline = () => wake({ fresh: true, announce: true })
    const onVisible = () => { if (document.visibilityState === "visible") wake({}) }
    const onFocus = () => wake({})
    const onRetry = () => wake({ announce: true, manual: true })
    window.addEventListener("online", onOnline)
    document.addEventListener("visibilitychange", onVisible)
    window.addEventListener("focus", onFocus)
    window.addEventListener(RETRY_EVENT, onRetry)
    return () => {
      window.removeEventListener("online", onOnline)
      document.removeEventListener("visibilitychange", onVisible)
      window.removeEventListener("focus", onFocus)
      window.removeEventListener(RETRY_EVENT, onRetry)
    }
  }, [enabled, wake])

  /** "Retry now": every socket in the tab tries at once (see RETRY_EVENT). */
  const retry = useCallback(() => {
    if (typeof window !== "undefined") window.dispatchEvent(new Event(RETRY_EVENT))
  }, [])

  return { status, reason, send, disconnect, reconnect: () => { void connect() }, retry }
}

/** Compute WS URL at runtime via the server-base seam (lib/server-base.ts).
 *  Same-origin default derives from window.location exactly as before
 *  (dev-server.mjs proxies /ws to the Go backend); a desktop shell's
 *  configured server base routes the socket to the chosen crewshipd. */
function resolveWsUrl(): string {
  const base = resolveWsBase()
  return base === "" ? "" : `${base}/ws`
}
