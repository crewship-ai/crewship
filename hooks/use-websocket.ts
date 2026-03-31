"use client"

import { useCallback, useEffect, useRef, useState } from "react"
import { z } from "zod"

export type WSStatus = "connecting" | "connected" | "disconnected" | "error"

const wsMessageSchema = z.object({
  type: z.string(),
  channel: z.string().optional(),
  payload: z.union([z.string(), z.record(z.string(), z.unknown())]).optional(),
}).passthrough()

export type WSMessage = z.infer<typeof wsMessageSchema>

interface UseWebSocketOptions {
  url: string
  token: string | null
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

export function useWebSocket({
  url,
  token,
  onMessage,
  onStatusChange,
}: UseWebSocketOptions) {
  const [status, setStatus] = useState<WSStatus>("disconnected")
  const wsRef = useRef<WebSocket | null>(null)
  const reconnectAttemptsRef = useRef(0)
  const reconnectTimerRef = useRef<ReturnType<typeof setTimeout> | undefined>(undefined)
  const disconnectingRef = useRef(false)

  // Use refs for callbacks to prevent reconnection loops when consumers
  // pass non-memoized functions.
  const onMessageRef = useRef(onMessage)
  const onStatusChangeRef = useRef(onStatusChange)
  useEffect(() => { onMessageRef.current = onMessage }, [onMessage])
  useEffect(() => { onStatusChangeRef.current = onStatusChange }, [onStatusChange])

  const updateStatus = useCallback((s: WSStatus) => {
    setStatus(s)
    onStatusChangeRef.current?.(s)
  }, [])

  const connect = useCallback(() => {
    if (!token) return
    // Compute URL at connect-time from window.location to avoid SSR/cache issues.
    // The `url` prop is used as override; if empty, auto-detect from the browser.
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
    const wsUrl = wsUrlObj.toString()
    const ws = new WebSocket(wsUrl)
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
        onMessageRef.current?.(result.data)
      } catch {
        // non-JSON message, ignore
      }
    }

    ws.onerror = () => {
      updateStatus("error")
    }

    ws.onclose = () => {
      wsRef.current = null
      updateStatus("disconnected")

      // Reconnect with exponential backoff unless intentionally disconnected
      if (!disconnectingRef.current) {
        const delay = backoffDelay(reconnectAttemptsRef.current)
        reconnectAttemptsRef.current++
        reconnectTimerRef.current = setTimeout(connect, delay)
      }
    }
  }, [url, token, updateStatus])

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
    connect()
    return () => disconnect()
  }, [connect, disconnect])

  return { status, send, disconnect, reconnect: connect }
}

/** Compute WS URL from window.location at runtime. Always correct regardless
 *  of SSR, env var caching, or deployment topology.  Uses the same host:port
 *  as the page — in dev mode, dev-server.mjs proxies /ws to the Go backend. */
function resolveWsUrl(): string {
  if (typeof window === "undefined") return ""
  const proto = window.location.protocol === "https:" ? "wss:" : "ws:"
  return `${proto}//${window.location.host}/ws`
}
