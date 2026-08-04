"use client"

import * as React from "react"

import { WifiIcon as AnimatedWifi, type WifiIconHandle } from "@/components/ui/wifi"
import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip"
import type { CrewsStatus } from "@/hooks/use-crews-status"
import { cn } from "@/lib/utils"

// =============================================================================
// The toolbar's status pill.
//
// This used to be TWO pills — connection ("Online") and fleet ("Crews idle") —
// sitting side by side, and the fleet one was losing an argument it should
// never have been in. Its live half said "2 active", which the Activity panel
// twenty pixels away already says with a routine name, an elapsed time, a cost
// and a Cancel button. Its quiet half said "Crews idle", a claim about right
// now derived from `agents.status` — a column that flips for the six seconds
// an agent takes to answer a question, so the word was wrong about as often as
// it was right.
//
// So the fleet half stops competing on "what is happening" (Activity owns
// that, with better data) and says the thing only it knows:
//
//     Online · 7 agents      the census — true whenever anyone looks
//     Online · 3 queued      the admission queue, surfaced nowhere else
//     Online · 2 errors      a broken agent, surfaced nowhere else
//
// Connection and fleet keep SEPARATE tones, so "the link is fine, the fleet is
// broken" reads in one glance rather than one colour having to mean both.
//
// And when the connection is down the fleet half disappears rather than going
// stale: those counts are last-known, and printing "7 agents" next to
// "Offline" states as fact something nobody can currently know. That is the
// whole reason the two pills became one — two of them could make exactly that
// contradiction, side by side.
//
// Routines and issues are deliberately absent. A routine's live state IS the
// Activity panel's LIVE section, so putting it here would recreate the overlap
// this removes; issues are a queue of work, not a health signal.
// =============================================================================

export type StatusTone = "success" | "warn" | "destructive" | "muted"

export interface SystemStatusDescription {
  connection: { label: string; tone: StatusTone }
  /** Null when the connection is not up, or the counts have not arrived. */
  fleet: { label: string; tone: StatusTone } | null
  ariaLabel: string
}

const cap99 = (n: number) => (n > 99 ? "99+" : String(n))
const plural = (n: number, word: string) => `${cap99(n)} ${word}${n === 1 ? "" : "s"}`

/**
 * Pure description of the pill. Split out from the render so the precedence
 * ladder — offline, then errors, then queue, then the census — can be read and
 * tested without a DOM.
 */
export function describeSystemStatus(
  engineStatus: string,
  wsStatus: string,
  crews: CrewsStatus | null,
): SystemStatusDescription {
  const online = engineStatus === "connected" && wsStatus === "connected"

  // "degraded" is a single failed poll or a 429 throttle — the engine is not
  // gone, so it must not read like it. "Reconnecting" vs "Connecting" tells a
  // mid-deploy restart apart from a session that never came up.
  let connection: { label: string; tone: StatusTone }
  if (online) {
    connection = { label: "Online", tone: "success" }
  } else if (engineStatus === "degraded") {
    connection = { label: "Reconnecting", tone: "warn" }
  } else if (engineStatus === "checking" || wsStatus === "connecting") {
    connection = { label: "Connecting", tone: "warn" }
  } else {
    connection = { label: "Offline", tone: "destructive" }
  }

  if (!online || !crews) {
    return {
      connection,
      fleet: null,
      ariaLabel: `System ${connection.label.toLowerCase()}`,
    }
  }

  let fleet: { label: string; tone: StatusTone }
  if (crews.error > 0) {
    // A broken agent outranks a deep queue: a queue is the system working as
    // designed, an error is not.
    fleet = { label: plural(crews.error, "error"), tone: "destructive" }
  } else if (crews.queued > 0) {
    fleet = { label: `${cap99(crews.queued)} queued`, tone: "warn" }
  } else if (crews.total === 0) {
    fleet = { label: "No agents", tone: "muted" }
  } else {
    fleet = { label: plural(crews.total, "agent"), tone: "muted" }
  }

  return {
    connection,
    fleet,
    ariaLabel: `System ${connection.label.toLowerCase()}, ${fleet.label}`,
  }
}

const TONE_TEXT: Record<StatusTone, string> = {
  success: "text-success",
  warn: "text-warn",
  destructive: "text-destructive",
  muted: "text-muted-foreground",
}

const TONE_FILL: Record<StatusTone, string> = {
  success: "bg-success/10 border-success/25",
  warn: "bg-warn/10 border-warn/25",
  destructive: "bg-destructive/10 border-destructive/25",
  muted: "bg-muted/50 border-border",
}

export interface SystemStatusPillProps {
  engineStatus: string
  wsStatus: string
  crews: CrewsStatus | null
}

export function SystemStatusPill({ engineStatus, wsStatus, crews }: SystemStatusPillProps) {
  const wifiRef = React.useRef<WifiIconHandle>(null)
  const { connection, fleet, ariaLabel } = describeSystemStatus(engineStatus, wsStatus, crews)

  React.useEffect(() => {
    if (wsStatus !== "connected") return
    const handle = wifiRef.current
    handle?.startAnimation()
    const t = setTimeout(() => handle?.stopAnimation(), 1000)
    return () => {
      clearTimeout(t)
      handle?.stopAnimation()
    }
  }, [wsStatus])

  return (
    <Tooltip>
      <TooltipTrigger asChild>
        <div
          tabIndex={0}
          role="status"
          aria-label={ariaLabel}
          data-testid="system-status-pill"
          className={cn(
            "flex items-center gap-1.5 rounded-full border px-2.5 py-1",
            // The pill's own fill follows the CONNECTION: when the link is
            // down that is the fact that governs everything else on screen.
            TONE_FILL[connection.tone],
          )}
        >
          <AnimatedWifi ref={wifiRef} size={12} className={TONE_TEXT[connection.tone]} />
          <span className={cn("text-micro font-medium", TONE_TEXT[connection.tone])}>
            {connection.label}
          </span>
          {fleet && (
            <>
              <span className="text-micro text-muted-foreground-soft" aria-hidden="true">·</span>
              <span className={cn("text-micro font-medium", TONE_TEXT[fleet.tone])}>{fleet.label}</span>
            </>
          )}
        </div>
      </TooltipTrigger>
      <TooltipContent>
        <span className="block">
          Engine:{" "}
          {engineStatus === "connected"
            ? "Online"
            : engineStatus === "checking"
              ? "Connecting..."
              : engineStatus === "degraded"
                ? "Reconnecting..."
                : "Offline"}{" "}
          / Real-time: {wsStatus === "connected" ? "Connected" : wsStatus === "connecting" ? "Connecting..." : "Disconnected"}
        </span>
        {fleet && crews && (
          <span className="block">
            {/* Gated on `fleet`, not on `crews`: while the link is down these
                counts are last-known, and the pill drops them for exactly that
                reason. A tooltip that still recited them would undo it. */}
            {crews.total} agents: {crews.running} running
            {crews.queued > 0 ? `, ${crews.queued} queued` : ""}, {crews.idle} idle
            {crews.error > 0 ? `, ${crews.error} error${crews.error > 1 ? "s" : ""}` : ""}
          </span>
        )}
      </TooltipContent>
    </Tooltip>
  )
}
