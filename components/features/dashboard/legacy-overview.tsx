"use client"

/**
 * The pre-Harbor-2 overview widgets, kept for the Routines dashboard
 * (components/features/routines/routines-dashboard.tsx), which still renders
 * them. The Dashboard itself moved to the Harbor 2 layout in
 * dashboard-overview.tsx; these go when the Routines area moves too.
 */

import * as React from "react"
import Link from "next/link"
import { AnimatePresence, motion, useReducedMotion } from "motion/react"
import {
  AlertTriangle,
  ArrowRight,
  CalendarClock,
  CheckCircle2,
  ChevronRight,
  Gauge,
  HelpCircle,
  TimerReset,
  type LucideIcon,
} from "lucide-react"

import type { DashboardWindow } from "@/app/(dashboard)/dashboard-types"
import type { PipelineSchedule } from "@/hooks/use-pipeline-schedules"
import { AnimatedNumber } from "@/components/ui/animated-number"
import { DashboardCard } from "@/components/features/dashboard/dashboard-card"
import { InlineEmpty } from "@/components/ui/inline-empty"
import { entityHref } from "@/lib/entity-links"
import { cn } from "@/lib/utils"
import { formatDuration } from "@/lib/time"

export interface AttentionItem {
  id: string
  label: string
  detail: string
  href: string
  tone: "warn" | "danger" | "purple" | "blue"
  icon: LucideIcon
}

const ATTENTION_TONE = {
  warn: "border-warn/25 bg-warn/[0.07] text-warn",
  danger: "border-destructive/25 bg-destructive/[0.07] text-destructive",
  purple: "border-purple/25 bg-purple/[0.07] text-purple-hover",
  blue: "border-primary/25 bg-primary/[0.07] text-primary-hover",
} as const

function MotionLink({
  href,
  className,
  children,
}: {
  href: string
  className?: string
  children: React.ReactNode
}) {
  return (
    <div className={className}>
      <Link href={href}>{children}</Link>
    </div>
  )
}

function attentionState(args: {
  items: AttentionItem[]
  inboxLoading: boolean
  inboxError: string | null
}): { kind: "items" | "clear" | "unknown"; inboxKnown: boolean } {
  const inboxKnown = !args.inboxLoading && !args.inboxError
  if (args.items.length > 0) return { kind: "items", inboxKnown }
  if (!inboxKnown) return { kind: "unknown", inboxKnown }
  return { kind: "clear", inboxKnown }
}

/** The verb on each attention row — what clicking it lets the person do. */
const ATTENTION_ACTION: Record<string, string> = {
  approvals: "Review",
  failures: "Inspect",
  reviews: "Review",
  capacity: "Details",
  schedules: "Review",
  drafts: "Publish",
}

/** How many rows the strip renders as cards before it starts summarising. */
const ATTENTION_VISIBLE = 3

function attentionOverflow(items: AttentionItem[]): AttentionItem[] {
  return items.slice(ATTENTION_VISIBLE)
}

export function AttentionStrip({
  items,
  inboxLoading = false,
  inboxError = null,
}: {
  items: AttentionItem[]
  inboxLoading?: boolean
  inboxError?: string | null
}) {
  const reduce = useReducedMotion()
  const visible = items.slice(0, ATTENTION_VISIBLE)
  const overflow = attentionOverflow(items)
  const { kind: state, inboxKnown } = attentionState({ items, inboxLoading, inboxError })

  return (
    <div
      className={cn(
        "overflow-hidden rounded-xl border bg-card",
        state === "items" ? "border-warn/20" : state === "clear" ? "border-success/20" : "border-border/60",
      )}
    >
      <div className="flex items-center justify-between border-b border-border/60 px-4 py-3">
        <div className="flex items-center gap-2">
          {state === "items" ? (
            <AlertTriangle className="h-4 w-4 text-warn" />
          ) : state === "clear" ? (
            <CheckCircle2 className="h-4 w-4 text-success" />
          ) : (
            <HelpCircle className="h-4 w-4 text-muted-foreground" />
          )}
          <h2 className="text-body font-semibold">
            {state === "items" ? "Needs your attention" : state === "clear" ? "All clear" : "Attention status unavailable"}
          </h2>
          {visible.length > 0 && (
            <span className="hidden shrink-0 whitespace-nowrap rounded-full bg-warn/12 px-2 py-0.5 text-micro font-semibold text-warn sm:inline-flex">
              {items.length} categories
            </span>
          )}
        </div>
        <Link href={entityHref({ kind: "inbox" })} className="inline-flex shrink-0 items-center gap-1 whitespace-nowrap text-label text-primary-hover hover:underline">
          Open Inbox <ArrowRight className="h-3.5 w-3.5" />
        </Link>
      </div>

      {state === "unknown" ? (
        <div className="flex items-center gap-2 px-4 py-4 text-body text-muted-foreground">
          {inboxError
            ? "The inbox could not be read, so this cannot say whether anything is blocking your crews."
            : "Checking what needs you…"}
        </div>
      ) : visible.length === 0 ? (
        <div className="flex items-center gap-2 px-4 py-4 text-body text-muted-foreground">
          There is nothing blocking your crews right now.
        </div>
      ) : (
        <div className="grid grid-cols-1 divide-y divide-border/60 lg:grid-cols-3 lg:divide-x lg:divide-y-0">
          <AnimatePresence initial={false}>
            {visible.map((item, index) => {
              const Icon = item.icon
              return (
                <motion.div
                  key={item.id}
                  initial={reduce ? false : { opacity: 0, y: 8, backgroundColor: "rgba(30,123,254,0.16)" }}
                  animate={{ opacity: 1, y: 0, backgroundColor: "rgba(30,123,254,0)" }}
                  exit={reduce ? undefined : { opacity: 0, y: -8 }}
                  transition={{ duration: 0.28, delay: reduce ? 0 : index * 0.06, backgroundColor: { duration: 2.2, ease: "easeOut" } }}
                >
                  <MotionLink href={item.href} className="h-full">
                    <div className="group flex h-full items-center gap-3 px-4 py-3.5 transition-colors hover:bg-foreground/[0.025]">
                      <span className={cn("flex h-9 w-9 shrink-0 items-center justify-center rounded-lg border", ATTENTION_TONE[item.tone])}>
                        <Icon className="h-4 w-4" />
                      </span>
                      <span className="min-w-0 flex-1">
                        <span className="block truncate text-body font-semibold text-foreground/90">{item.label}</span>
                        <span className="mt-0.5 block truncate text-label text-muted-foreground">{item.detail}</span>
                      </span>
                      {/* A verb, not a chevron: the row exists so the person
                          can act, and the label says what acting means. */}
                      <span className="inline-flex shrink-0 items-center gap-1 rounded-md border border-border/70 px-2.5 py-1 text-label font-medium text-foreground/90 transition-colors group-hover:border-primary/50 group-hover:text-primary-hover">
                        {ATTENTION_ACTION[item.id] ?? "Open"} <ChevronRight className="h-3.5 w-3.5" />
                      </span>
                    </div>
                  </MotionLink>
                </motion.div>
              )
            })}
          </AnimatePresence>
        </div>
      )}

      {/* Later alerts stay named and linked even when the first cards fill. */}
      {state === "items" && !inboxKnown && (
        <div className="border-t border-border/60 px-4 py-2 text-label text-muted-foreground">
          {inboxError
            ? "Approvals and failed runs are missing from this list — the inbox could not be read."
            : "Still counting approvals and failed runs…"}
        </div>
      )}

      {overflow.length > 0 && (
        <div className="flex flex-wrap items-center gap-x-3 gap-y-1 border-t border-border/60 px-4 py-2 text-label text-muted-foreground">
          <span>{overflow.length} more:</span>
          {overflow.map((item) => (
            <Link key={item.id} href={item.href} className="text-primary-hover hover:underline">
              {item.label}
            </Link>
          ))}
        </div>
      )}
    </div>
  )
}

export function UpNext({ schedules }: { schedules: PipelineSchedule[] }) {
  const upcoming = React.useMemo(() => {
    const now = Date.now()
    return schedules
      .filter((schedule) => schedule.enabled && schedule.next_run_at && new Date(schedule.next_run_at).getTime() > now)
      .sort((a, b) => new Date(a.next_run_at!).getTime() - new Date(b.next_run_at!).getTime())
      .slice(0, 4)
  }, [schedules])
  const scheduleTime = (iso: string) => {
    const date = new Date(iso)
    const today = new Date()
    const tomorrow = new Date(today)
    tomorrow.setDate(today.getDate() + 1)
    const day = date.toDateString() === today.toDateString() ? "Today"
      : date.toDateString() === tomorrow.toDateString() ? "Tomorrow"
        : date.toLocaleDateString(undefined, { month: "short", day: "numeric" })
    return `${day} · ${date.toLocaleTimeString(undefined, { hour: "2-digit", minute: "2-digit" })}`
  }

  return (
    <DashboardCard
      title="Up next"
      icon={CalendarClock}
      hint={upcoming.length > 0 ? `${upcoming.length} upcoming` : "none queued"}
      action={<Link href="/routines?tab=calendar" className="text-primary-hover hover:underline">Calendar →</Link>}
      className="h-full"
    >
      {upcoming.length === 0 ? (
        <InlineEmpty icon={CalendarClock} text="Nothing scheduled." action={<Link href="/routines" className="text-primary-hover hover:underline">Add a trigger →</Link>} />
      ) : (
        <div className="flex flex-col">
          {upcoming.map((schedule) => (
            <MotionLink key={schedule.id} href={`/routines?routine=${encodeURIComponent(schedule.target_pipeline_slug || "")}`}>
              <div className="group flex items-center gap-3 rounded-md border-b border-border/50 px-1 py-2 last:border-0 hover:bg-foreground/[0.025]">
                <span className="flex h-7 w-7 shrink-0 items-center justify-center rounded-md border border-primary/25 bg-primary/[0.07] text-primary-hover">
                  <CalendarClock className="h-3.5 w-3.5" />
                </span>
                <span className="min-w-0 flex-1">
                  <span className="block truncate text-body font-medium text-foreground/85">
                    {schedule.target_pipeline_slug || schedule.name}
                  </span>
                  <span className="block truncate text-label text-muted-foreground">{schedule.name}</span>
                </span>
                <span className="shrink-0 font-mono text-label tabular-nums text-muted-foreground">
                  {scheduleTime(schedule.next_run_at!)}
                </span>
                <span
                  title={schedule.last_status ? `last run ${schedule.last_status}` : "never ran"}
                  className={cn("h-2 w-2 shrink-0 rounded-full", scheduleDotClass(schedule.last_status))}
                  aria-label={schedule.last_status ? `last run ${schedule.last_status}` : "never ran"}
                />
              </div>
            </MotionLink>
          ))}
        </div>
      )}
    </DashboardCard>
  )
}

/** Pure: the colour of a schedule's "last run" dot. */
export function scheduleDotClass(lastStatus: string | undefined): string {
  if (!lastStatus) return "bg-muted-foreground/50"
  const s = lastStatus.toLowerCase()
  if (s === "completed" || s === "succeeded" || s === "success" || s === "ok") return "bg-muted-foreground/50"
  if (s === "failed" || s === "error" || s === "timeout") return "bg-destructive"
  if (s === "running" || s === "queued") return "bg-primary"
  return "bg-warn"
}

function MetricNumber({ value }: { value: number }) {
  const reduce = useReducedMotion()
  return reduce ? <>{Math.round(value)}</> : <AnimatedNumber value={value} duration={0.72} />
}

export interface OutcomeKpiData {
  completed: number
  successPct: number | null
  successOk: number
  successTotal: number
  p95Ms: number
}

export function OutcomeKpis({
  data,
  window,
  spendUsd = null,
  spendPerRun = null,
}: {
  data: OutcomeKpiData
  window: DashboardWindow
  /** Metered spend for the window; null when paymaster answered with no
   *  ledger rows; undefined while pending or after a failed fetch. */
  spendUsd?: number | null | undefined
  spendPerRun?: number | null
}) {
  const reduce = useReducedMotion()
  // Nothing ran in the window: four tiles of zeros and dashes say one thing,
  // so say it once. The tiles come back as soon as anything finishes.
  if (data.completed === 0 && data.successTotal === 0 && data.p95Ms === 0) {
    return (
      <InlineEmpty
        icon={Gauge}
        text={<>No agent runs in the last {window}. Routine runs are counted in Activity.</>}
        action={<Link href="/activity" className="shrink-0 text-label font-medium text-primary-hover hover:underline">Open Activity →</Link>}
      />
    )
  }
  const cards = [
    {
      label: "Completed",
      icon: CheckCircle2,
      tone: "text-success bg-success/10 border-success/20",
      value: <MetricNumber value={data.completed} />,
      detail: `successful runs · ${window}`,
    },
    {
      label: "Success",
      icon: Gauge,
      tone: "text-primary-hover bg-primary/10 border-primary/20",
      value: data.successPct == null ? "—" : `${data.successPct}%`,
      detail: data.successTotal > 0 ? `${data.successOk} of ${data.successTotal} finished` : "no finished runs",
    },
    // No cost tile. On a flat-rate subscription the marginal cost of a call is
    // structurally not a number — paymaster says so itself: BillingFlatRate
    // forces CostUSD to 0 and Confidence to Unknown, and $ budgets do not
    // apply. Rendering "$0.00" there reads as "this was free", which is a
    // different claim from "this is not measured here".
    //
    // The workspace this was built against makes the point: 25 routine runs
    // carrying $0.83 of adapter-reported usage produced zero ledger rows,
    // because routine runs on the subscription adapter never reach the
    // metered path at all. The tile showed the ledger.
    //
    // A cost figure comes back when it follows billing_mode and carries a
    // cost_confidence badge — which is what paymaster's own type comment
    // requires ("never display a number without a badge telling the operator
    // how trustworthy it is") and what this surface never did. See #2193.
    {
      label: "P95 duration",
      icon: TimerReset,
      tone: "text-purple-hover bg-purple/10 border-purple/20",
      value: data.p95Ms > 0 ? formatDuration(data.p95Ms) : "—",
      detail: data.p95Ms > 0 ? "95% finish within this" : "no duration samples",
    },
    // The fourth tile the grid always had a slot for. It is the metered
    // LEDGER, and it says so: on a flat-rate subscription paymaster meters
    // nothing (#2193), and that case renders "not metered", never "$0.00".
    {
      label: "Spend",
      icon: Gauge,
      tone: "text-warn bg-warn/10 border-warn/20",
      value: typeof spendUsd === "number" ? `$${spendUsd.toFixed(2)}` : "—",
      detail: spendUsd === undefined ? "ledger unavailable" : spendUsd === null ? "not metered on this billing mode" : spendPerRun != null ? `ledger · $${spendPerRun.toFixed(3)} per run` : "metered ledger",
    },
  ]

  return (
    <div className="grid grid-cols-2 gap-3 xl:grid-cols-4">
      {cards.map((card, index) => {
        const Icon = card.icon
        return (
          <motion.div
            key={card.label}
            whileHover={reduce ? undefined : { y: -2 }}
            transition={{ type: "spring", stiffness: 420, damping: 32 }}
            className="group flex min-h-20 items-center gap-3 rounded-card border border-border bg-card px-4 py-3 transition-colors hover:border-line-strong"
          >
            <motion.span
              initial={reduce ? false : { opacity: 0, rotate: -12, scale: 0.82 }}
              animate={{ opacity: 1, rotate: 0, scale: 1 }}
              transition={{ delay: reduce ? 0 : index * 0.06 + 0.18, type: "spring", stiffness: 360, damping: 25 }}
              className={cn("flex h-9 w-9 shrink-0 items-center justify-center rounded-lg border", card.tone)}
            >
              <Icon className="h-5 w-5" />
            </motion.span>
            <div className="min-w-0 flex-1">
              <div className="flex flex-wrap items-baseline gap-x-2 gap-y-0.5">
                <span className="text-xl font-semibold leading-none tabular-nums text-foreground">{card.value}</span>
                <span className="text-micro font-semibold uppercase tracking-wider text-muted-foreground">{card.label}</span>
              </div>
              <div className="mt-1 line-clamp-2 text-label leading-snug text-muted-foreground">{card.detail}</div>
            </div>
          </motion.div>
        )
      })}
    </div>
  )
}
