"use client"

import { useEffect, useState } from "react"
import { Wifi, WifiOff } from "lucide-react"
import { Spinner } from "@/components/ui/spinner"
import { useRealtime } from "@/hooks/use-realtime"

/**
 * Shows a banner when WebSocket is disconnected for more than 3 seconds.
 * Auto-hides with a brief "Reconnected" flash when connection is restored.
 */
export function RealtimeStatusBanner() {
  const { status } = useRealtime()
  const [visible, setVisible] = useState(false)
  const [showReconnected, setShowReconnected] = useState(false)
  const [wasDisconnected, setWasDisconnected] = useState(false)

  useEffect(() => {
    if (status === "disconnected" || status === "error") {
      setShowReconnected(false)
      setVisible(false)
      // Show banner after 3 seconds of being disconnected
      const timer = setTimeout(() => {
        setVisible(true)
        setWasDisconnected(true)
      }, 3000)
      return () => clearTimeout(timer)
    }

    if (status === "connected" && wasDisconnected) {
      // Show brief "Reconnected" flash
      setVisible(true)
      setShowReconnected(true)
      const timer = setTimeout(() => {
        setVisible(false)
        setShowReconnected(false)
        setWasDisconnected(false)
      }, 2000)
      return () => clearTimeout(timer)
    }

    setVisible(false)
  }, [status, wasDisconnected])

  if (!visible) return null

  if (showReconnected) {
    return (
      <div role="status" aria-live="polite" aria-atomic="true" className="bg-emerald-500/90 text-white text-center py-1.5 px-4 text-xs font-medium flex items-center justify-center gap-2">
        <Wifi className="h-3.5 w-3.5" />
        Reconnected
      </div>
    )
  }

  return (
    <div role="status" aria-live="polite" aria-atomic="true" className="bg-amber-500/90 text-white text-center py-1.5 px-4 text-xs font-medium flex items-center justify-center gap-2 animate-pulse">
      {status === "error" ? (
        <>
          <WifiOff className="h-3.5 w-3.5" />
          Connection lost. Retrying...
        </>
      ) : (
        <>
          <Spinner className="h-3.5 w-3.5" />
          Reconnecting...
        </>
      )}
    </div>
  )
}
