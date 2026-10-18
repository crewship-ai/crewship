"use client"

import * as React from "react"

import Link from "next/link"
import { Bot, Clock, TriangleAlert, WifiOff } from "lucide-react"

import { WifiIcon as AnimatedWifi, type WifiIconHandle } from "@/components/ui/wifi"
import { Popover, PopoverContent, PopoverTrigger } from "@/components/ui/popover"
import type { CrewsStatus } from "@/hooks/use-crews-status"
import { apiFetch } from "@/lib/api-fetch"
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
// that, with better data) and speaks only when it has something to act on:
//
//     (wifi icon only)       healthy — the bar says nothing it does not have to
//     3 queued               the admission queue, surfaced nowhere else
//     2 agent errors         a broken agent, surfaced nowhere else
//     No agents              the one count worth a word: nothing can run
//
// A census ("7 agents") was the first attempt and went the way of "Crews idle"
// before it — true, unchanging, and never asking for anything, so it spent a
// permanent slot saying nothing. The breakdown lives in the popover, which is
// where a number you look UP belongs rather than one you are shown.
//
// The pill shows ONE message in ONE tone, the worst state winning. It used to
// print both halves — "Online · 1 error", green and red inside one green
// capsule — and the error read as a footnote to good news. A fleet message
// only ever appears while the link is up, so it implies "Online" without
// saying it; the popover states both halves separately.
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
  /**
   * The fleet segment, or null — which means EITHER "nothing to report" or
   * "not knowable right now". Use `fleetKnown` to tell those apart.
   */
  fleet: { label: string; tone: StatusTone } | null
  /**
   * Whether the counts are current: the link is up and they have arrived.
   *
   * Separate from `fleet` because the pill and the tooltip answer different
   * questions. The pill shows only what wants acting on, so it is silent on a
   * healthy workspace; the tooltip is where you LOOK UP the numbers, so it
   * still recites them. Sharing one condition hid the breakdown exactly when
   * the tooltip had become its only home.
   */
  fleetKnown: boolean
  /**
   * What the pill itself shows: ONE message in ONE tone, the worst state
   * winning — Offline › Reconnecting/Connecting › errors › queue › no agents.
   * `label: null` is the healthy case, where the pill shrinks to its icon.
   *
   * The pill used to print both halves side by side — a green "Online" and a
   * red "1 error" inside one green capsule — so the error read as a footnote
   * to good news. A broken agent implies the link is up (the fleet half is
   * dropped whenever it is not), so the connection does not need saying next
   * to it; the popover still states both.
   */
  display: { label: string | null; tone: StatusTone; icon: StatusIcon }
  ariaLabel: string
}

export type StatusIcon = "wifi" | "wifi-off" | "alert" | "queue" | "empty"

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
      fleetKnown: false,
      display: online
        ? { label: null, tone: "success", icon: "wifi" }
        : { label: connection.label, tone: connection.tone, icon: connection.label === "Offline" ? "wifi-off" : "wifi" },
      ariaLabel: `System ${connection.label.toLowerCase()}`,
    }
  }

  // Exceptions only. The census went the way of "Crews idle" before it: on a
  // healthy workspace "7 agents" is true, unchanging and never asks for
  // anything, so it spends a permanent slot in the bar saying nothing. It is
  // in the tooltip, where a number you look up belongs.
  let fleet: { label: string; tone: StatusTone } | null = null
  let display: SystemStatusDescription["display"] = { label: null, tone: "success", icon: "wifi" }
  if (crews.error > 0) {
    // A broken agent outranks a deep queue: a queue is the system working as
    // designed, an error is not.
    fleet = { label: plural(crews.error, "error"), tone: "destructive" }
    // Standing alone the pill has to say WHAT errored — "1 error" next to
    // "Online" borrowed its subject from the word beside it.
    display = { label: plural(crews.error, "agent error"), tone: "destructive", icon: "alert" }
  } else if (crews.queued > 0) {
    fleet = { label: `${cap99(crews.queued)} queued`, tone: "warn" }
    display = { label: fleet.label, tone: "warn", icon: "queue" }
  } else if (crews.total === 0) {
    // Not a census — the one state where the count IS the thing to act on.
    // A workspace with no agents cannot do anything, and that is worth a word
    // in the bar rather than a hover.
    fleet = { label: "No agents", tone: "muted" }
    display = { label: fleet.label, tone: "muted", icon: "empty" }
  }

  return {
    connection,
    fleet,
    fleetKnown: true,
    display,
    ariaLabel: fleet
      ? `System ${connection.label.toLowerCase()}, ${fleet.label}`
      : `System ${connection.label.toLowerCase()}`,
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
  /** Workspace whose agents the popover lists when some are in error. */
  workspaceId?: string | null
}

const ICON_PATHS: Record<Exclude<StatusIcon, "wifi">, React.ReactNode> = {
  "wifi-off": <WifiOff className="h-3 w-3" aria-hidden />,
  alert: <TriangleAlert className="h-3 w-3" aria-hidden />,
  queue: <Clock className="h-3 w-3" aria-hidden />,
  empty: <Bot className="h-3 w-3" aria-hidden />,
}

export function SystemStatusPill({ engineStatus, wsStatus, crews, workspaceId = null }: SystemStatusPillProps) {
  const wifiRef = React.useRef<WifiIconHandle>(null)
  const [open, setOpen] = React.useState(false)
  const description = describeSystemStatus(engineStatus, wsStatus, crews)
  const { display, ariaLabel } = description

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
    <Popover open={open} onOpenChange={setOpen}>
      <PopoverTrigger asChild>
        <button
          type="button"
          aria-label={ariaLabel}
          data-testid="system-status-pill"
          data-tone={display.tone}
          className={cn(
            "flex h-7 items-center gap-1.5 rounded-full border transition-colors outline-none focus-visible:ring-2 focus-visible:ring-ring/50",
            // Healthy: just the icon in a round chip. A status strip earns its
            // place by being quiet until it has something to say.
            display.label ? "px-2.5" : "w-7 justify-center",
            TONE_FILL[display.tone],
            TONE_TEXT[display.tone],
          )}
        >
          {display.icon === "wifi" ? (
            <AnimatedWifi ref={wifiRef} size={12} className={TONE_TEXT[display.tone]} />
          ) : (
            ICON_PATHS[display.icon]
          )}
          {display.label ? (
            <span className="text-micro font-medium whitespace-nowrap">{display.label}</span>
          ) : (
            // Kept for anything reading the pill as text (screen readers use
            // the aria-label; the e2e suite reads innerText).
            <span className="sr-only">{description.connection.label}</span>
          )}
        </button>
      </PopoverTrigger>
      <PopoverContent align="end" className="w-80 p-0">
        <SystemStatusPanel
          description={description}
          engineStatus={engineStatus}
          wsStatus={wsStatus}
          crews={crews}
          workspaceId={workspaceId}
          onNavigate={() => setOpen(false)}
        />
      </PopoverContent>
    </Popover>
  )
}

function engineLabel(engineStatus: string) {
  if (engineStatus === "connected") return "Online"
  if (engineStatus === "checking") return "Connecting…"
  if (engineStatus === "degraded") return "Reconnecting…"
  return "Offline"
}

function realtimeLabel(wsStatus: string) {
  if (wsStatus === "connected") return "Connected"
  if (wsStatus === "connecting") return "Connecting…"
  return "Disconnected"
}

function StatusRow({ label, value, tone }: { label: string; value: React.ReactNode; tone?: StatusTone }) {
  return (
    <div className="flex items-center justify-between gap-3 py-1.5 text-xs">
      <span className="text-muted-foreground">{label}</span>
      <span className={cn("flex items-center gap-1.5 font-medium", tone && TONE_TEXT[tone])}>
        {tone && <span className={cn("h-1.5 w-1.5 rounded-full", TONE_DOT[tone])} aria-hidden />}
        {value}
      </span>
    </div>
  )
}

const TONE_DOT: Record<StatusTone, string> = {
  success: "bg-success",
  warn: "bg-warn",
  destructive: "bg-destructive",
  muted: "bg-muted-foreground",
}

/**
 * The popover is where the numbers live. The pill shows only what wants
 * acting on; this recites the connection and the fleet in full, and — when an
 * agent is broken — names it, so "1 agent error" is one click from the agent
 * rather than a hunt through every crew.
 */
function SystemStatusPanel({
  description,
  engineStatus,
  wsStatus,
  crews,
  workspaceId,
  onNavigate,
}: {
  description: SystemStatusDescription
  engineStatus: string
  wsStatus: string
  crews: CrewsStatus | null
  workspaceId: string | null
  onNavigate: () => void
}) {
  const { display, fleetKnown } = description
  return (
    <div data-testid="system-status-panel">
      <div className="flex items-center justify-between gap-2 px-4 pt-3.5 pb-2">
        <span className="text-sm font-semibold">System status</span>
        {display.label && (
          <span className={cn("rounded-full border px-2 py-0.5 text-micro font-medium", TONE_FILL[display.tone], TONE_TEXT[display.tone])}>
            {display.label}
          </span>
        )}
      </div>
      <div className="border-b border-border/60 px-4 pb-2">
        <StatusRow
          label="Engine"
          value={engineLabel(engineStatus)}
          tone={engineStatus === "connected" ? "success" : engineStatus === "offline" ? "destructive" : "warn"}
        />
        <StatusRow
          label="Real-time"
          value={realtimeLabel(wsStatus)}
          tone={wsStatus === "connected" ? "success" : wsStatus === "connecting" ? "warn" : "destructive"}
        />
        {/* Gated on `fleetKnown`, not on the pill's label: the pill is silent
            on a healthy workspace, and that is exactly when this row is the
            only place the counts exist. Not on `crews` either — while the
            link is down they are last-known, and the pill drops them for
            that reason; reciting them here would undo it. */}
        {fleetKnown && crews && (
          <StatusRow
            label="Agents"
            value={
              <span className="text-micro font-normal tabular-nums">
                {crews.total} total · {crews.running} running · {crews.idle} idle
                {crews.queued > 0 ? ` · ${crews.queued} queued` : ""}
              </span>
            }
          />
        )}
      </div>
      {fleetKnown && crews && crews.error > 0 && workspaceId && (
        <ErroredAgents workspaceId={workspaceId} count={crews.error} onNavigate={onNavigate} />
      )}
      {!fleetKnown && (
        <p className="px-4 py-3 text-xs text-muted-foreground">
          Agent counts return once the connection is back.
        </p>
      )}
      <div className="border-t border-border/60 px-4 py-2.5">
        <Link href="/crews" onClick={onNavigate} className="text-xs text-primary hover:underline">
          Open Crews →
        </Link>
      </div>
    </div>
  )
}

interface ErroredAgent {
  id: string
  slug: string
  name: string
  status?: string
  crew?: { name?: string | null } | null
}

const MAX_ERRORED_ROWS = 5

/** The broken agents themselves, fetched only while the popover is open —
 *  crews-status returns counts, and the agents list has no status filter, so
 *  this filters the workspace roster client-side (it is capped at 500 rows
 *  server-side, the same list the dashboard reads). */
function ErroredAgents({ workspaceId, count, onNavigate }: { workspaceId: string; count: number; onNavigate: () => void }) {
  const [agents, setAgents] = React.useState<ErroredAgent[] | null>(null)

  React.useEffect(() => {
    let live = true
    apiFetch(`/api/v1/agents?workspace_id=${encodeURIComponent(workspaceId)}&limit=500`)
      .then((res) => (res.ok ? res.json() : []))
      .then((data: unknown) => {
        if (!live) return
        const list = Array.isArray(data) ? (data as ErroredAgent[]) : []
        setAgents(list.filter((a) => String(a.status ?? "").toUpperCase() === "ERROR"))
      })
      .catch(() => { if (live) setAgents([]) })
    return () => { live = false }
  }, [workspaceId, count])

  const shown = agents?.slice(0, MAX_ERRORED_ROWS) ?? []
  const hidden = Math.max(0, count - shown.length)

  return (
    <div className="px-4 py-2.5">
      <div className="eyebrow mb-1 text-destructive">Needs attention</div>
      {agents === null ? (
        <div className="py-1.5 text-xs text-muted-foreground">Loading…</div>
      ) : (
        <ul className="-mx-2">
          {shown.map((a) => (
            <li key={a.id}>
              <Link
                href={`/crews?agent=${encodeURIComponent(a.slug)}`}
                onClick={onNavigate}
                className="flex items-center gap-2.5 rounded-md px-2 py-1.5 hover:bg-destructive/10"
              >
                <TriangleAlert className="h-3.5 w-3.5 shrink-0 text-destructive" aria-hidden />
                <span className="min-w-0 flex-1">
                  <span className="block truncate text-xs font-medium">{a.name}</span>
                  {a.crew?.name && <span className="block truncate text-micro text-muted-foreground">{a.crew.name}</span>}
                </span>
                <span className="text-micro text-primary">Open</span>
              </Link>
            </li>
          ))}
          {hidden > 0 && (
            <li className="px-2 py-1 text-micro text-muted-foreground">+{hidden} more</li>
          )}
        </ul>
      )}
    </div>
  )
}
