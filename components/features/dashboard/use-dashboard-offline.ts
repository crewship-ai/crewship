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
  const lost = status === "disconnected" || status === "error"
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
