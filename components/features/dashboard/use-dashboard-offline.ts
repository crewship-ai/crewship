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
  const [state, setState] = useState<{ offline: boolean; since: string | undefined }>({ offline: false, since: undefined })

  useEffect(() => {
    if (!lost) {
      setState((s) => (s.offline ? { offline: false, since: undefined } : s))
      return
    }
    const since = clock.format(new Date())
    const timer = setTimeout(() => setState({ offline: true, since }), OFFLINE_AFTER_MS)
    return () => clearTimeout(timer)
  }, [lost])

  return lost ? state : { offline: false, since: undefined }
}
