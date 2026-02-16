"use client"

import { useCallback, useEffect, useRef, useState } from "react"

export type WSStatus = "connecting" | "connected" | "disconnected" | "error"

interface WSMessage {
  type: string
  channel?: string
  payload?: string
  [key: string]: unknown
}

interface UseWebSocketOptions {
  url: string
  token: string | null
  onMessage?: (msg: WSMessage) => void
  onStatusChange?: (status: WSStatus) => void
  reconnectInterval?: number
  maxReconnectAttempts?: number
}

export function useWebSocket({
  url,
  token,
  onMessage,
  onStatusChange,
  reconnectInterval = 3000,
  maxReconnectAttempts = 10,
}: UseWebSocketOptions) {
  const [status, setStatus] = useState<WSStatus>("disconnected")
  const wsRef = useRef<WebSocket | null>(null)
  const reconnectAttemptsRef = useRef(0)
  const reconnectTimerRef = useRef<ReturnType<typeof setTimeout> | undefined>(undefined)

  const updateStatus = useCallback(
    (s: WSStatus) => {
      setStatus(s)
      onStatusChange?.(s)
    },
    [onStatusChange],
  )

  const connect = useCallback(() => {
    if (!token || !url) return

    const wsUrl = `${url}?token=${encodeURIComponent(token)}`
    const ws = new WebSocket(wsUrl)
    wsRef.current = ws

    updateStatus("connecting")

    ws.onopen = () => {
      reconnectAttemptsRef.current = 0
      updateStatus("connected")
    }

    ws.onmessage = (event) => {
      try {
        const msg = JSON.parse(event.data) as WSMessage
        onMessage?.(msg)
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

      if (reconnectAttemptsRef.current < maxReconnectAttempts) {
        reconnectAttemptsRef.current++
        reconnectTimerRef.current = setTimeout(connect, reconnectInterval)
      }
    }
  }, [url, token, onMessage, updateStatus, reconnectInterval, maxReconnectAttempts])

  const disconnect = useCallback(() => {
    if (reconnectTimerRef.current) {
      clearTimeout(reconnectTimerRef.current)
    }
    reconnectAttemptsRef.current = maxReconnectAttempts
    wsRef.current?.close()
    wsRef.current = null
  }, [maxReconnectAttempts])

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
