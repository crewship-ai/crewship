"use client"

import { useEffect, useRef, useState } from "react"
import { CircleCheck, WifiOff } from "lucide-react"
import { Button } from "@/components/ui/button"
import { useRealtime } from "@/hooks/use-realtime"
import { AppBanner, useAppBannerSlot } from "@/components/layout/app-banners"

/** How long the socket may be down before the banner says so. */
export const REALTIME_BANNER_DELAY_MS = 3000
/** How long "Reconnected" stays after the socket returns. */
export const RECONNECTED_FLASH_MS = 2000

function clock(d: Date): string {
  return d.toLocaleTimeString(undefined, { hour: "2-digit", minute: "2-digit" })
}

/**
 * The realtime banner (Harbor 2 Offline state): after 3 s without the socket,
 * "Reconnecting… Live updates are paused. You see data from 14:02." with
 * Retry now; when it returns, "Reconnected" for 2 s. It holds the app's one
 * banner slot at the highest priority, so a page's own offline banner steps
 * aside while it shows. No pulse: the banner does not move.
 *
 * Renders nothing for "unavailable": a restricted session has no realtime by
 * design (the allowlist refuses /ws-token), so there is nothing to reconnect
 * and a banner saying otherwise would be permanent and false.
 */
export function RealtimeStatusBanner() {
  const { status, retry } = useRealtime()
  const [visible, setVisible] = useState(false)
  const [showReconnected, setShowReconnected] = useState(false)
  // When the data on screen was last live: the moment the socket dropped.
  // Non-null for the whole outage, including the "connecting" attempts in
  // between, so the banner does not blink with every retry.
  const [since, setSince] = useState<Date | null>(null)
  const shown = useRef(false)

  useEffect(() => {
    if (status === "disconnected" || status === "error") setSince((prev) => prev ?? new Date())
    else if (status === "connected" || status === "unavailable") setSince(null)
  }, [status])

  const down = since !== null
  useEffect(() => {
    if (down) {
      setShowReconnected(false)
      const timer = setTimeout(() => {
        shown.current = true
        setVisible(true)
      }, REALTIME_BANNER_DELAY_MS)
      return () => clearTimeout(timer)
    }
    if (shown.current) {
      // Only an outage the person was told about earns a "Reconnected".
      shown.current = false
      setShowReconnected(true)
      setVisible(true)
      const timer = setTimeout(() => {
        setVisible(false)
        setShowReconnected(false)
      }, RECONNECTED_FLASH_MS)
      return () => clearTimeout(timer)
    }
    setVisible(false)
  }, [down])

  const wanted = visible && status !== "unavailable"
  const slot = useAppBannerSlot("realtime", wanted)
  if (!slot) return null

  if (showReconnected) {
    return (
      <AppBanner tone="ok" icon={CircleCheck} data-state="reconnected">
        Reconnected
      </AppBanner>
    )
  }

  return (
    <AppBanner
      tone="warn"
      icon={WifiOff}
      data-state="offline"
      action={
        retry ? (
          <Button size="sm" variant="outline" className="page-banner-action" onClick={retry}>
            Retry now
          </Button>
        ) : undefined
      }
    >
      <b>{status === "error" ? "Connection lost." : "Reconnecting…"}</b> Live updates are paused.
      {since && <> You see data from {clock(since)}.</>}
    </AppBanner>
  )
}
