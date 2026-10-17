"use client"

import * as React from "react"
import Link from "next/link"
import { ChevronRight } from "lucide-react"

import type {
  AgentSummary,
  CrewServiceSummary,
  CrewSummary,
  DashboardWindow,
  RunInsightsResponse,
  RuntimeCapacityResponse,
} from "@/app/(dashboard)/dashboard-types"
import type { InboxItem } from "@/hooks/use-inbox"
import { isBlockingInboxItem } from "@/components/features/inbox-v2/inbox-v2-derive"
import type { PipelineSchedule } from "@/hooks/use-pipeline-schedules"
import { ConceptIcon } from "@/components/ui/concept-icon"
import { StatusPill } from "@/components/ui/status-pill"
import { Button } from "@/components/ui/button"
import { sparklinePoints } from "@/components/ui/sparkline"
import { DashCard, DashLine, DashNote, TEXT_LINK } from "@/components/features/dashboard/dash-card"
import { entityHref } from "@/lib/entity-links"
import { cn } from "@/lib/utils"
import { formatDuration } from "@/lib/time"
import { honestPct } from "@/lib/honest-pct"

// The Routines dashboard still renders the pre-Harbor-2 widgets under their
// old names; they live in legacy-overview.tsx until that area moves.
export { AttentionStrip, OutcomeKpis, UpNext, type AttentionItem, type OutcomeKpiData } from "./legacy-overview"

// ─── Attention ──────────────────────────────────────────────────────────────

export interface AttentionTile {
  id: string
  /** The whole sentence ("2 decisions waiting") — overflow line, page meta. */
  label: string
  /** The tile's number, as shown ("12+" past the fetch window). */
  count: string
  /** The tile's title without the number ("Decisions waiting"). */
  title: string
  detail: string
  href: string
  tone: "warn" | "bad" | "vio"
}

/**
 * What the attention tiles are allowed to claim.
 *
 * "Nothing needs you" is a positive assertion about the workspace, and the
 * tiles can only make it when the inbox actually answered. `useInbox` throws
 * on a non-ok response with `retry: false`, so a 403 on an RBAC-gated
 * workspace is a permanent empty list, not a transient one — and rendering
 * green over it tells an operator with pending approvals that nothing needs
 * them.
 *
 * Three states, because there are three: something is wrong, nothing is
 * wrong, and we do not know. `inboxKnown` says whether the items that ARE
 * shown are the whole answer: capacity holds come from their own endpoint, so
 * the list can be non-empty while the inbox was never read.
 */
export function attentionState(args: {
  items: AttentionTile[]
  inboxLoading: boolean
  inboxError: string | null
}): { kind: "items" | "clear" | "unknown"; inboxKnown: boolean } {
  const inboxKnown = !args.inboxLoading && !args.inboxError
  if (args.items.length > 0) return { kind: "items", inboxKnown }
  if (!inboxKnown) return { kind: "unknown", inboxKnown }
  return { kind: "clear", inboxKnown }
}

/**
 * The capacity holds that belong to THIS workspace, one per crew.
 *
 * The endpoint (`GET /api/v1/runtime/capacity`) is deliberately
 * instance-scoped, so on an instance with more than one tenant the raw list
 * carries other workspaces' crews; this surface renders a hold's `detail`
 * verbatim, so filtering is what keeps one tenant's crew names off another's
 * dashboard. And `admission.Hold` is appended per held START, not per crew,
 * so five queued starts on one crew read as "5 crews waiting" without the
 * dedupe.
 */
export function heldForWorkspace(
  held: RuntimeCapacityResponse["held"] | null | undefined,
  crews: CrewSummary[],
): NonNullable<RuntimeCapacityResponse["held"]> {
  if (!held || held.length === 0) return []
  const mine = new Set(crews.map((c) => c.id))
  const seen = new Set<string>()
  const out: NonNullable<RuntimeCapacityResponse["held"]> = []
  for (const h of held) {
    if (!h.crew_id || !mine.has(h.crew_id) || seen.has(h.crew_id)) continue
    seen.add(h.crew_id)
    out.push(h)
  }
  return out
}

/** How many tiles the row shows before the rest move to one overflow line. */
export const ATTENTION_VISIBLE = 4

/**
 * The items past the fourth tile. Returned rather than dropped so the row can
 * name them and keep their links — the order is fixed (decisions, failures,
 * reviews, capacity, schedules), so the later operational alerts would
 * otherwise vanish exactly when the first tiles are full (#2187).
 */
export function attentionOverflow(items: AttentionTile[]): AttentionTile[] {
  return items.slice(ATTENTION_VISIBLE)
}

const plural = (n: number, one: string, many: string) => (n === 1 ? one : many)

/**
 * The attention tiles, in a fixed priority order: decisions, failures,
 * reviews, capacity, schedules. A kind with nothing waiting has no tile.
 */
export function buildAttentionItems({
  inbox,
  heldCrews,
  reviewCount = 0,
  reviewIdentifiers = [],
  activeByKind,
  decisionCount,
}: {
  inbox: InboxItem[]
  heldCrews: NonNullable<RuntimeCapacityResponse["held"]>
  reviewCount?: number
  /** Identifiers of the first issues waiting for review, newest first. */
  reviewIdentifiers?: string[]
  /** Server-computed exact counts of visible not-resolved items per kind.
   *  When present the counts come from it — inbox rows are windowed at 100
   *  and the headline silently plateaued there (#2187). */
  activeByKind?: Record<string, number>
  decisionCount?: number
}): AttentionTile[] {
  const items: AttentionTile[] = []
  const decisions = inbox.filter(isBlockingInboxItem)
  const failures = inbox.filter((item) => item.kind === "failed_run" || item.kind === "schedule_circuit_breaker_tripped")
  const scheduleProblems = inbox.filter((item) => item.kind === "schedule_missed")
  const held = heldCrews.length

  const decisionsTotal = decisionCount ?? decisions.length
  const failedRunCount = activeByKind
    ? (activeByKind.failed_run ?? 0)
    : failures.filter((item) => item.kind === "failed_run").length
  const circuitBreakerCount = activeByKind
    ? (activeByKind.schedule_circuit_breaker_tripped ?? 0)
    : failures.filter((item) => item.kind === "schedule_circuit_breaker_tripped").length
  const failuresCount = failedRunCount + circuitBreakerCount
  const scheduleCount = activeByKind ? (activeByKind.schedule_missed ?? 0) : scheduleProblems.length

  if (decisionsTotal > 0) {
    items.push({
      id: "approvals",
      label: `${decisionsTotal} ${plural(decisionsTotal, "decision", "decisions")} waiting`,
      count: String(decisionsTotal),
      title: plural(decisionsTotal, "Decision waiting", "Decisions waiting"),
      detail: "Approvals and questions",
      href: entityHref({ kind: "inbox", attention: "approvals" }),
      tone: "warn",
    })
  }
  if (failuresCount > 0) {
    const alerts = circuitBreakerCount > 0
    const first = failures[0]?.title
    items.push({
      id: "failures",
      label: alerts ? `${failuresCount} run ${plural(failuresCount, "alert", "alerts")}` : `${failuresCount} failed ${plural(failuresCount, "run", "runs")}`,
      count: String(failuresCount),
      title: alerts ? plural(failuresCount, "Run alert", "Run alerts") : plural(failuresCount, "Failed run", "Failed runs"),
      detail: first || "Investigate and retry",
      href: entityHref({ kind: "inbox", attention: "run-alerts" }),
      tone: "bad",
    })
  }
  if (reviewCount > 0) {
    const shown = reviewCount >= 12 ? "12+" : String(reviewCount)
    items.push({
      id: "reviews",
      label: `${shown} ${plural(reviewCount, "issue", "issues")} for review`,
      count: shown,
      title: plural(reviewCount, "Issue for review", "Issues for review"),
      detail: reviewIdentifiers.slice(0, 2).join(", ") || "Check agent work and decide",
      href: entityHref({ kind: "issues", status: "REVIEW" }),
      tone: "vio",
    })
  }
  if (held > 0) {
    items.push({
      id: "capacity",
      label: `${held} ${plural(held, "crew", "crews")} waiting for capacity`,
      count: String(held),
      title: "Waiting for capacity",
      detail: heldCrews[0]?.detail || "View host admission details",
      href: "/settings",
      tone: "warn",
    })
  }
  if (scheduleCount > 0) {
    items.push({
      id: "schedules",
      label: `${scheduleCount} schedule ${plural(scheduleCount, "alert", "alerts")}`,
      count: String(scheduleCount),
      title: plural(scheduleCount, "Schedule alert", "Schedule alerts"),
      detail: "Missed routines",
      href: entityHref({ kind: "inbox", attention: "schedule-alerts" }),
      tone: "warn",
    })
  }
  return items
}

const TILE_TONE: Record<AttentionTile["tone"], string> = {
  warn: "bg-chip-warn-bg text-chip-warn-fg",
  bad: "bg-chip-danger-bg text-chip-danger-fg",
  vio: "bg-chip-violet-bg text-chip-violet-fg",
}

/**
 * Up to four tiles, each a number in the tone of its state, a title and one
 * fact; whatever does not fit is named on one line under them. When nothing
 * waits, one line says so — and only when the inbox answered.
 */
export function AttentionTiles({
  items,
  inboxLoading = false,
  inboxError = null,
  onRetry,
}: {
  items: AttentionTile[]
  inboxLoading?: boolean
  inboxError?: string | null
  onRetry?: () => void
}) {
  const visible = items.slice(0, ATTENTION_VISIBLE)
  const overflow = attentionOverflow(items)
  const { kind, inboxKnown } = attentionState({ items, inboxLoading, inboxError })

  if (kind !== "items") {
    return (
      <div data-testid="attention-tiles" data-state={kind} className="mb-4 overflow-hidden rounded-card border border-border bg-card [&>*]:border-t-0">
        {kind === "clear" ? (
          <DashNote tone="ok">Nothing needs you right now</DashNote>
        ) : inboxError ? (
          <DashNote tone="error" action={onRetry && <Button size="xs" variant="outline" onClick={onRetry} className="coarse:min-h-11">Retry</Button>}>
            Could not check the inbox
          </DashNote>
        ) : (
          <DashNote>Checking what needs you…</DashNote>
        )}
      </div>
    )
  }

  return (
    <div data-testid="attention-tiles" data-state="items" className="mb-4">
      <div className="grid grid-cols-2 gap-2.5 xl:grid-cols-4">
        {visible.map((item) => (
          <Link
            key={item.id}
            href={item.href}
            data-slot="attention-tile"
            className="group flex min-w-0 flex-col items-start gap-2 rounded-card border border-border bg-card px-3.5 py-3 text-muted-foreground transition-colors hover:border-line-strong focus-visible:outline-2 focus-visible:outline-primary md:flex-row md:items-center md:gap-3"
          >
            <span className={cn("grid h-9 w-9 shrink-0 place-items-center rounded-md text-heading font-semibold tabular-nums", TILE_TONE[item.tone])}>
              {item.count}
            </span>
            <span className="min-w-0 flex-1">
              <b className="block font-medium text-foreground max-md:whitespace-normal md:truncate">{item.title}</b>
              <small className="block truncate text-label">{item.detail}</small>
            </span>
            <ChevronRight className="hidden size-3.5 shrink-0 md:block" aria-hidden />
          </Link>
        ))}
      </div>
      {(overflow.length > 0 || !inboxKnown) && (
        <p className="mt-2 flex flex-wrap items-center gap-x-3 gap-y-1 px-1 text-label text-muted-foreground">
          {!inboxKnown && (
            <span>{inboxError ? "Decisions and failed runs are missing here — the inbox could not be read." : "Still counting decisions and failed runs…"}</span>
          )}
          {overflow.length > 0 && (
            <>
              <span>+{overflow.length} more:</span>
              {overflow.map((item) => (
                <Link key={item.id} href={item.href} className={TEXT_LINK}>
                  {item.label}
                </Link>
              ))}
              <Link href={entityHref({ kind: "inbox" })} className={TEXT_LINK}>Open Inbox</Link>
            </>
          )}
        </p>
      )}
    </div>
  )
}

// ─── Up next ────────────────────────────────────────────────────────────────

/** Pure: when a scheduled run fires, as the card's right column says it —
 *  "09:30" today, "Thu 22 · 08:00" within a week, "1 Nov" after that. */
export function upNextTime(iso: string, now: Date = new Date()): string {
  const at = new Date(iso)
  const time = at.toLocaleTimeString(undefined, { hour: "2-digit", minute: "2-digit" })
  if (at.toDateString() === now.toDateString()) return time
  if (at.getTime() - now.getTime() < 7 * 24 * 3_600_000) {
    return `${at.toLocaleDateString("en-GB", { weekday: "short" })} ${at.getDate()} · ${time}`
  }
  return at.toLocaleDateString("en-GB", { day: "numeric", month: "short" })
}

const FAILED_STATUS = new Set(["failed", "error", "timeout"])

export function DashboardUpNext({ schedules, now }: { schedules: PipelineSchedule[]; now?: Date }) {
  const upcoming = React.useMemo(() => {
    const t = (now ?? new Date()).getTime()
    return schedules
      .filter((schedule) => schedule.enabled && schedule.next_run_at && new Date(schedule.next_run_at).getTime() > t)
      .sort((a, b) => new Date(a.next_run_at!).getTime() - new Date(b.next_run_at!).getTime())
      .slice(0, 3)
  }, [schedules, now])

  return (
    // "Calendar" names where the link will go; until the Calendar page ships
    // it opens the routines calendar tab.
    <DashCard title="Up next" concept="triggers" link={{ label: "Calendar", href: "/calendar" }}>
      {upcoming.length === 0 ? (
        <DashNote action={<Link href="/routines" className={TEXT_LINK}>Add a trigger</Link>}>Nothing scheduled.</DashNote>
      ) : (
        upcoming.map((schedule) => {
          const routine = schedule.target_pipeline_slug || schedule.name
          const failed = schedule.last_status ? FAILED_STATUS.has(schedule.last_status.toLowerCase()) : false
          return (
            <DashLine
              key={schedule.id}
              lead={<ConceptIcon concept="routines" variant="chip" className="h-7 w-7 rounded-md" />}
              title={routine}
              sub={schedule.name !== routine ? schedule.name : undefined}
              href={entityHref({ kind: "routine", slug: schedule.target_pipeline_slug || "" })}
              right={
                <>
                  {failed && <StatusPill tone="danger" label="Last run failed" />}
                  <span className="whitespace-nowrap font-mono text-label tabular-nums text-muted-foreground">{upNextTime(schedule.next_run_at!, now)}</span>
                </>
              }
            />
          )
        })
      )}
    </DashCard>
  )
}

// ─── KPIs ───────────────────────────────────────────────────────────────────

export interface DashboardKpiData {
  completed: number
  successPct: number | null
  successOk: number
  successTotal: number
  p95Ms: number
  p50Ms: number
}

/** A sparkline that stretches to its tile; drawn only from a real series. */
function KpiSpark({ values, label }: { values: number[]; label: string }) {
  const pts = sparklinePoints(values, 120, 28)
  const line = pts.map(([x, y]) => `${x.toFixed(1)},${y.toFixed(1)}`).join(" ")
  return (
    <svg data-slot="kpi-spark" viewBox="0 0 120 28" preserveAspectRatio="none" className="mt-1 block h-7 w-full" role="img" aria-label={label}>
      <polyline points={`2,28 ${line} 118,28`} fill="var(--primary)" fillOpacity={0.1} stroke="none" />
      <polyline points={line} fill="none" stroke="var(--primary)" strokeWidth={1.6} vectorEffect="non-scaling-stroke" />
    </svg>
  )
}

/**
 * The run summary as one strip of four figures: label, number, one fact of
 * context. A sparkline is drawn only where a series exists — the run volume
 * under Completed. Change against the previous window and a workspace budget
 * have no source yet, so they are not shown.
 */
export function DashboardKpis({
  data,
  window,
  // No default: a default of null turned "the ledger has not answered"
  // (undefined) into "not metered", a claim about the billing mode.
  spendUsd,
  spendPerRun = null,
  completedSeries,
  completedSeriesLabel,
}: {
  data: DashboardKpiData
  window: DashboardWindow
  /** Metered spend for the window; null when paymaster answered with no
   *  ledger rows; undefined while pending or after a failed fetch. */
  spendUsd: number | null | undefined
  spendPerRun?: number | null
  /** Runs per bucket across the window, oldest first. */
  completedSeries?: number[]
  completedSeriesLabel?: string
}) {
  // Nothing ran in the window: four tiles of zeros and dashes say one thing,
  // so say it once.
  if (data.completed === 0 && data.successTotal === 0 && data.p95Ms === 0) {
    return (
      <div data-testid="dashboard-kpis" className="mt-4 overflow-hidden rounded-card border border-border bg-card [&>*]:border-t-0">
        <DashNote action={<Link href="/activity" className={TEXT_LINK}>Activity</Link>}>
          No agent runs in the last {window}. Routine runs are counted in Activity.
        </DashNote>
      </div>
    )
  }
  const failed = data.successTotal - data.successOk
  const tiles: { label: string; value: string; context?: string; spark?: React.ReactNode }[] = [
    {
      label: "Completed",
      value: String(data.completed),
      spark: completedSeries && completedSeries.length > 1 && completedSeries.some((v) => v > 0)
        ? <KpiSpark values={completedSeries} label={completedSeriesLabel ?? "Runs over the window"} />
        : undefined,
    },
    {
      label: "Success",
      value: data.successPct == null ? "—" : `${data.successPct}%`,
      context: data.successTotal > 0 ? `${failed} failed` : "no finished runs",
    },
    {
      label: "P95 duration",
      value: data.p95Ms > 0 ? formatDuration(data.p95Ms) : "—",
      context: data.p50Ms > 0 ? `median ${formatDuration(data.p50Ms)}` : undefined,
    },
    // The metered LEDGER, and it says so: on a flat-rate subscription
    // paymaster meters nothing (#2193), and that case renders "not metered",
    // never "$0.00".
    {
      label: "Spend",
      value: typeof spendUsd === "number" ? `$${spendUsd.toFixed(2)}` : "—",
      context: spendUsd === undefined ? "unavailable" : spendUsd === null ? "not metered" : spendPerRun != null ? `per run $${spendPerRun.toFixed(2)}` : undefined,
    },
  ]

  return (
    <div data-testid="dashboard-kpis" className="mt-4 grid grid-cols-2 gap-px overflow-hidden rounded-card border border-border bg-border xl:grid-cols-4">
      {tiles.map((tile) => (
        <div key={tile.label} data-slot="kpi" className="min-w-0 bg-card px-4 py-3.5">
          <small className="block text-control text-muted-foreground">{tile.label}</small>
          <span className="flex flex-wrap items-baseline gap-x-1.5">
            <b className="text-title font-semibold tabular-nums tracking-tight text-foreground">{tile.value}</b>
            {tile.context && <em className="text-label not-italic text-muted-foreground">{tile.context}</em>}
          </span>
          {tile.spark}
        </div>
      ))}
    </div>
  )
}

// ─── Crews ──────────────────────────────────────────────────────────────────

export interface FleetHealthRow {
  crew: CrewSummary
  status: string
  detail: string
  tone: "success" | "warn" | "danger" | "muted" | "blue"
  agents: number
  runningAgents: number
  services: CrewServiceSummary
}

export function deriveFleetHealth({
  crews,
  agents,
  gapsByCrew,
  servicesByCrew,
}: {
  crews: CrewSummary[]
  agents: AgentSummary[]
  gapsByCrew: ReadonlyMap<string, number>
  servicesByCrew: ReadonlyMap<string, CrewServiceSummary>
}): FleetHealthRow[] {
  return crews.map((crew) => {
    const crewAgents = agents.filter((agent) => agent.crew_id === crew.id || agent.crew?.slug === crew.slug)
    const running = crewAgents.filter((agent) => agent.status === "RUNNING").length
    const errors = crewAgents.filter((agent) => agent.status === "ERROR").length
    const gaps = gapsByCrew.get(crew.id) ?? 0
    const services = servicesByCrew.get(crew.id) ?? { total: 0, running: 0, degraded: 0, checked: false }

    let status = "Ready"
    let detail = "No active run"
    let tone: FleetHealthRow["tone"] = "success"
    if (errors > 0) {
      status = "Agent error"
      detail = `${errors} agent${errors === 1 ? "" : "s"} need attention`
      tone = "danger"
    } else if (services.degraded > 0) {
      status = "Service degraded"
      detail = `${services.degraded} service${services.degraded === 1 ? "" : "s"} not running`
      tone = "warn"
    } else if (gaps > 0) {
      status = "Needs tool"
      detail = `${gaps} credential gap${gaps === 1 ? "" : "s"}`
      tone = "warn"
    } else if (running > 0) {
      status = "Running"
      detail = `${running} active agent${running === 1 ? "" : "s"}`
      tone = "blue"
    } else if (crewAgents.length === 0) {
      status = "Empty"
      detail = "No agents configured"
      tone = "muted"
    }

    return { crew, status, detail, tone, agents: crewAgents.length, runningAgents: running, services }
  })
}

export function kpisFromInsights(
  insights: RunInsightsResponse | null,
): DashboardKpiData {
  const ok = insights?.totals.succeeded ?? 0
  const failed = insights?.totals.failed ?? 0
  const finished = ok + failed
  return {
    completed: ok,
    // Never round a failure away (199 of 200 is 99%, not 100%) — same rule as
    // the routines dashboard.
    successPct: honestPct(ok, finished),
    successOk: ok,
    successTotal: finished,
    p95Ms: insights?.duration.p95_ms ?? 0,
    p50Ms: insights?.duration.p50_ms ?? 0,
  }
}
