"use client"

import { useEffect, useState } from "react"

import { useRealtime } from "@/hooks/use-realtime"

/** How long live updates may be gone before the page calls itself offline.
 *  The shell's RealtimeStatusBanner waits the same 3 s, so a blip never dims. */
export const OFFLINE_AFTER_MS = 3_000

const clock = new Intl.DateTimeFormat(undefined, { hour: "2-digit", minute: "2-digit", hourCycle: "h23" })

/**
 * Whether the dashboard's numbers may be out of date (DSH-01, v67 Offline
 * state): realtime dropped ("disconnected") or failed ("error") for 3 s.
 * "connecting" is the first connect, not a loss; "unavailable" is a session
 * that has no realtime by design (restricted) — neither is offline.
 *
 * `since` is when live updates stopped, i.e. the time the data on screen is
 * from ("You see data from 14:02").
 */
export function useDashboardOffline(): { offline: boolean; since: string | undefined } {
  const { status } = useRealtime()
  // An outage runs from the first loss until "connected" (or "unavailable"),
  // as the shell's RealtimeStatusBanner counts it: the "connecting" of a retry
  // inside the outage neither ends it nor restarts the 3 s.
  const [lostAt, setLostAt] = useState<Date | null>(null)
  const [offline, setOffline] = useState(false)

  useEffect(() => {
    if (status === "disconnected" || status === "error") setLostAt((prev) => prev ?? new Date())
    else if (status === "connected" || status === "unavailable") setLostAt(null)
  }, [status])

  useEffect(() => {
    if (!lostAt) {
      setOffline(false)
      return
    }
    const timer = setTimeout(() => setOffline(true), Math.max(0, lostAt.getTime() + OFFLINE_AFTER_MS - Date.now()))
    return () => clearTimeout(timer)
  }, [lostAt])

  const over = status === "connected" || status === "unavailable"
  return offline && lostAt && !over ? { offline: true, since: clock.format(lostAt) } : { offline: false, since: undefined }
}
