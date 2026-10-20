"use client"

import { useEffect, useState } from "react"

import { useRealtime } from "@/hooks/use-realtime"
import { isRealtimeDown } from "@/hooks/use-websocket"
import { RECONNECT_GRACE_MS } from "@/components/layout/realtime-outage"

/** How long live updates may be gone before the page calls itself offline.
 *  The shell's RealtimeStatusBanner waits the same 4 s, so a blip never dims. */
export const OFFLINE_AFTER_MS = RECONNECT_GRACE_MS

const clock = new Intl.DateTimeFormat(undefined, { hour: "2-digit", minute: "2-digit", hourCycle: "h23" })

/**
 * Whether the dashboard's numbers may be out of date (DSH-01, v67 Offline
 * state): realtime has been down for 4 s (`isRealtimeDown`: retrying a drop,
 * offline, or failed). The first connect is not a loss; "unavailable" is a
 * session that has no realtime by design (restricted) — neither is offline.
 *
 * `since` is when live updates stopped, i.e. the time the data on screen is
 * from ("You see data from 14:02").
 */
export function useDashboardOffline(): { offline: boolean; since: string | undefined } {
  const { status, reason } = useRealtime()
  const lost = isRealtimeDown(status, reason)
  // An outage runs from the first loss until the connection is back, as the
  // shell's RealtimeStatusBanner counts it: the retries inside it neither end
  // it nor restart the 4 s, and `since` stays the time of the first loss.
  const [lostAt, setLostAt] = useState<Date | null>(null)
  const [offline, setOffline] = useState(false)

  useEffect(() => {
    if (lost) setLostAt((prev) => prev ?? new Date())
    else setLostAt(null)
  }, [lost])

  useEffect(() => {
    if (!lostAt) {
      setOffline(false)
      return
    }
    const timer = setTimeout(() => setOffline(true), Math.max(0, lostAt.getTime() + OFFLINE_AFTER_MS - Date.now()))
    return () => clearTimeout(timer)
  }, [lostAt])

  return offline && lostAt && lost ? { offline: true, since: clock.format(lostAt) } : { offline: false, since: undefined }
}
