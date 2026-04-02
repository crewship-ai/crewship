"use client"

import { useEffect, useRef, useState } from "react"

export interface KeeperLiveEvent {
  request_id: string
  request_type: string
  agent_name: string
  credential_name: string
  intent: string
  command?: string
  decision: string
  reason: string
  risk_score: number
  exit_code?: number
  decided_at: string
}

export type KeeperWsStatus = "disconnected" | "connecting" | "connected"

interface UseAdminWebSocketOptions {
  enabled: boolean
  workspaceId: string | null
}

interface UseAdminWebSocketReturn {
  keeperLiveEvents: KeeperLiveEvent[]
  keeperWsStatus: KeeperWsStatus
}

export function useAdminWebSocket({ enabled, workspaceId }: UseAdminWebSocketOptions): UseAdminWebSocketReturn {
  const [keeperLiveEvents, setKeeperLiveEvents] = useState<KeeperLiveEvent[]>([])
  const keeperWsRef = useRef<WebSocket | null>(null)
  const [keeperWsStatus, setKeeperWsStatus] = useState<KeeperWsStatus>("disconnected")

  useEffect(() => {
    if (!enabled || !workspaceId) return

    let ws: WebSocket | null = null
    let cancelled = false

    const connect = async () => {
      try {
        setKeeperWsStatus("connecting")
        const tokenRes = await fetch("/api/v1/ws-token", { credentials: "include" })
        if (!tokenRes.ok || cancelled) return
        const { token } = await tokenRes.json()
        if (!token || cancelled) return

        const proto = window.location.protocol === "https:" ? "wss:" : "ws:"
        const host = window.location.port === "3011"
          ? window.location.hostname + ":8081"
          : window.location.port === "3001"
            ? window.location.hostname + ":8080"
            : window.location.host
        const wsUrl = `${proto}//${host}/ws?token=${encodeURIComponent(token)}`
        ws = new WebSocket(wsUrl)
        keeperWsRef.current = ws

        ws.onopen = () => {
          if (cancelled) { ws?.close(); return }
          setKeeperWsStatus("connected")
          ws?.send(JSON.stringify({ type: "subscribe", channel: `keeper:${workspaceId}` }))
        }
        ws.onmessage = (event) => {
          try {
            const msg = JSON.parse(event.data)
            if (msg.type === "keeper_event" && msg.payload) {
              setKeeperLiveEvents((prev) => [msg.payload as KeeperLiveEvent, ...prev].slice(0, 100))
            }
          } catch { /* ignore non-JSON */ }
        }
        ws.onclose = () => {
          if (!cancelled) setKeeperWsStatus("disconnected")
        }
      } catch {
        setKeeperWsStatus("disconnected")
      }
    }

    connect()
    return () => {
      cancelled = true
      ws?.close()
      keeperWsRef.current = null
      setKeeperWsStatus("disconnected")
    }
  }, [enabled, workspaceId])

  return { keeperLiveEvents, keeperWsStatus }
}
