"use client"

import { useEffect, useState } from "react"

import type { WSReason, WSStatus } from "@/hooks/use-websocket"

/**
 * One vocabulary for a lost realtime connection (#3783), shared by the app's
 * banner (realtime-status-banner.tsx) and the chat's (reconnect-banner.tsx):
 * quiet first, louder only for a real outage.
 *
 *   reconnecting     neutral  "Reconnecting…"
 *   restarting       neutral  "Crewship is restarting · reconnecting…"
 *   unreachable      warn     "Can't reach Crewship · retrying every 30 s" + Retry now
 *   browser-offline  neutral  "You're offline · Crewship will reconnect when you're back"
 *
 * Nothing shows for the first RECONNECT_GRACE_MS of an outage, and nothing
 * replaces it when the socket returns: the bar just goes.
 */
export type RealtimeOutage = "reconnecting" | "restarting" | "unreachable" | "browser-offline"

/** How long the socket may be down before anything says so. A deploy's
 *  restart usually fits inside it. */
export const RECONNECT_GRACE_MS = 4000

/** The outage a socket's status and reason describe, or null for none. The
 *  first connect, a socket nobody asked for ("disconnected"), a dead session
 *  ("error": the app is on its way to /login) and "unavailable" are not. */
export function realtimeOutage(status: WSStatus | string, reason: WSReason | null | undefined): RealtimeOutage | null {
  if (status === "offline") return reason === "browser-offline" ? "browser-offline" : "unreachable"
  if (status !== "connecting" || !reason) return null
  return reason === "restart" ? "restarting" : "reconnecting"
}

/**
 * The outage once it has lasted RECONNECT_GRACE_MS, else null. The clock
 * starts at the drop and is not restarted when the kind changes (reconnecting
 * → unreachable), so a long outage is not hidden again for another 4 s.
 */
export function useAnnouncedOutage(outage: RealtimeOutage | null): RealtimeOutage | null {
  const down = outage !== null
  const [due, setDue] = useState(false)
  useEffect(() => {
    if (!down) {
      setDue(false)
      return
    }
    const timer = setTimeout(() => setDue(true), RECONNECT_GRACE_MS)
    return () => clearTimeout(timer)
  }, [down])
  return down && due ? outage : null
}

export interface OutageCopy {
  tone: "neutral" | "warn"
  text: string
  /** Offer "Retry now": only for a real outage, where a person may know better. */
  retry: boolean
  /** A spinner while an attempt is coming soon; an icon otherwise. */
  spinner: boolean
}

export function outageCopy(outage: RealtimeOutage, queuedCount = 0): OutageCopy {
  const queued = queuedCount > 0 ? ` · ${queuedCount} message${queuedCount === 1 ? "" : "s"} queued` : ""
  switch (outage) {
    case "reconnecting":
      return { tone: "neutral", text: `Reconnecting…${queued}`, retry: false, spinner: true }
    case "restarting":
      return { tone: "neutral", text: `Crewship is restarting · reconnecting…${queued}`, retry: false, spinner: true }
    case "unreachable":
      return { tone: "warn", text: `Can't reach Crewship · retrying every 30 s${queued}`, retry: true, spinner: false }
    case "browser-offline":
      return { tone: "neutral", text: `You're offline · Crewship will reconnect when you're back${queued}`, retry: false, spinner: false }
  }
}
