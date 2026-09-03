"use client"

import * as React from "react"
import Link from "next/link"
import { AnimatePresence, motion, useReducedMotion } from "motion/react"
import {
  AlertTriangle,
  ArrowRight,
  Bot,
  Brain,
  CalendarClock,
  CheckCircle2,
  ChevronRight,
  Clock3,
  Gauge,
  HelpCircle,
  KeyRound,
  Play,
  ServerCog,
  ShieldAlert,
  TimerReset,
  Wrench,
  XCircle,
  type LucideIcon,
} from "lucide-react"

import type {
  AgentSummary,
  CrewServiceSummary,
  CrewSummary,
  DashboardWindow,
  MemoryHealthResponse,
  RunInsightsResponse,
  RuntimeCapacityResponse,
} from "@/app/(dashboard)/dashboard-types"
import type { InboxItem } from "@/hooks/use-inbox"
import type { PipelineRun } from "@/hooks/use-pipeline-runs"
import type { PipelineSchedule } from "@/hooks/use-pipeline-schedules"
import type { Mission } from "@/lib/types/mission"
import { AnimatedNumber } from "@/components/ui/animated-number"
import { DashboardCard } from "@/components/features/dashboard/dashboard-card"
import { Sparkline } from "@/components/ui/sparkline"
import { InlineEmpty } from "@/components/ui/inline-empty"
import { cn } from "@/lib/utils"
import { formatDuration, formatRelativeTime } from "@/lib/time"

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
  const reduce = useReducedMotion()
  return (
    <motion.div
      whileHover={reduce ? undefined : { x: 3 }}
      whileTap={reduce ? undefined : { scale: 0.995 }}
      transition={{ type: "spring", stiffness: 520, damping: 38 }}
      className={className}
    >
      <Link href={href}>{children}</Link>
    </motion.div>
  )
}

function LiveDot({ tone = "success" }: { tone?: "success" | "warn" | "danger" | "blue" }) {
  const reduce = useReducedMotion()
  const color = {
    success: "bg-success",
    warn: "bg-warn",
    danger: "bg-destructive",
    blue: "bg-primary",
  }[tone]
  return (
    <span className="relative flex h-2.5 w-2.5 shrink-0" aria-hidden>
      {!reduce && (
        <motion.span
          className={cn("absolute inset-0 rounded-full", color)}
          animate={{ opacity: [0.5, 0], scale: [1, 2.2] }}
          transition={{ duration: 1.65, repeat: Infinity, ease: "easeOut" }}
        />
      )}
      <span className={cn("relative m-auto h-2 w-2 rounded-full", color)} />
    </span>
  )
}

/**
 * What the attention strip is allowed to claim.
 *
 * "All clear" is a positive assertion about the workspace, and the strip can
 * only make it when the inbox actually answered. `useInbox` throws on a non-ok
 * response with `retry: false`, so a 403 on an RBAC-gated workspace is a
 * permanent empty list, not a transient one — and rendering green over it
 * tells an operator with pending approvals that nothing needs them. The same
 * held for the whole first-paint round trip, which the page's `loading` gate
 * does not cover.
 *
 * Three states, because there are three: something is wrong, nothing is
 * wrong, and we do not know.
 *
 * `inboxKnown` is the fourth thing, and it is not a fourth state. `capacity`
 * and `credentials` come from their own endpoints, so the list can be
 * non-empty while the inbox — which carries approvals and failures — was
 * never read. Rendering a confident count over that is the same defect this
 * function exists to prevent, one layer up: a partial answer presented as a
 * complete one. The caller shows the items AND says the rest is unknown.
 */
export function attentionState(args: {
  items: AttentionItem[]
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
 * Two corrections in one place, because they share a cause. The endpoint
 * (`GET /api/v1/runtime/capacity`) is deliberately instance-scoped — the host
 * is a property of the instance, not of a workspace — so on an instance with
 * more than one tenant the raw list carries other workspaces' crews. This
 * surface is workspace-scoped and renders a hold's `detail` string verbatim,
 * so filtering is what keeps one tenant's crew names off another's dashboard.
 *
 * And `admission.Hold` is appended per held START, not per crew, so five
 * queued starts on one crew read as "5 crews waiting" without the dedupe.
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

/**
 * The Runtime capacity signal row.
 *
 * A null response means the fetch failed, and `capacity?.enabled === false` is
 * then also false — so the old expression fell through to a green
 * "Available", reporting a dead admission-control endpoint as healthy
 * capacity. That is the one row an operator reads when starts are hanging.
 * The Memory health row beside it already renders "Unavailable" for exactly
 * this case.
 */
export function capacitySignal(
  capacity: RuntimeCapacityResponse | null,
  held: NonNullable<RuntimeCapacityResponse["held"]>,
): { value: string; tone: string } {
  if (capacity == null) return { value: "Unavailable", tone: "text-muted-foreground" }
  if (held.length > 0) {
    return { value: `${held.length} held`, tone: "text-warn" }
  }
  if (capacity.enabled === false) return { value: "Disabled", tone: "text-muted-foreground" }
  return { value: "Available", tone: "text-success" }
}

/** The verb on each attention row — what clicking it lets the person do. */
const ATTENTION_ACTION: Record<string, string> = {
  approvals: "Review",
  failures: "Inspect",
  capacity: "Details",
  credentials: "Install",
  schedules: "Review",
}

/** How many rows the strip renders as cards before it starts summarising. */
const ATTENTION_VISIBLE = 3

/**
 * The items the strip cannot render as cards.
 *
 * The badge always showed `items.length`, so the count was never wrong — what
 * was wrong is that the items past the third had nowhere to go. The order is
 * fixed (approvals, failures, capacity, credentials, schedules), so on a
 * workspace with the first three a credential gap could never render, and
 * "Open Inbox" does not cover credential gaps or capacity holds.
 *
 * Returned rather than dropped so the strip can name them and keep their
 * links.
 */
export function attentionOverflow(items: AttentionItem[]): AttentionItem[] {
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
            <span className="rounded-full bg-warn/12 px-2 py-0.5 text-micro font-semibold text-warn">
              {items.length}
            </span>
          )}
        </div>
        <Link href="/inbox" className="inline-flex items-center gap-1 text-label text-primary-hover hover:underline">
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

      {/* Named, not just counted. The order is fixed, so on a busy workspace
          the items past the third are always the same ones — and neither a
          credential gap nor a capacity hold is reachable through the
          "Open Inbox" link above. */}
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

function useNow(active: boolean) {
  const [now, setNow] = React.useState(() => Date.now())
  React.useEffect(() => {
    if (!active) return
    const timer = window.setInterval(() => setNow(Date.now()), 1_000)
    return () => window.clearInterval(timer)
  }, [active])
  return now
}

function elapsed(startedAt: string, now: number): string {
  const started = new Date(startedAt).getTime()
  if (!Number.isFinite(started)) return "—"
  return formatDuration(Math.max(0, now - started))
}

export function RunningNow({
  runs,
  agents,
  crews,
  children,
}: {
  runs: PipelineRun[]
  agents: AgentSummary[]
  crews: CrewSummary[]
  /** Rendered under the run list — the activity ticker. */
  children?: React.ReactNode
}) {
  const reduce = useReducedMotion()
  const now = useNow(runs.length > 0)
  const agentById = React.useMemo(() => new Map(agents.map((agent) => [agent.id, agent])), [agents])
  const crewById = React.useMemo(() => new Map(crews.map((crew) => [crew.id, crew])), [crews])

  return (
    <DashboardCard
      title="Running now"
      icon={Play}
      hint={runs.length > 0 ? `${runs.length} active` : "fleet idle"}
      action={<Link href="/activity" className="text-primary-hover hover:underline">Activity →</Link>}
      className="h-full"
    >
      {runs.length === 0 ? (
        <InlineEmpty icon={CheckCircle2} text="Nothing is running — the fleet is ready for its next assignment." />
      ) : (
        <div className="overflow-x-auto">
          <div className="min-w-[650px]">
            <div className="grid grid-cols-[minmax(160px,1fr)_minmax(190px,1.4fr)_110px_78px] gap-3 border-b border-border/60 px-2 pb-2 text-label text-muted-foreground">
              <span>Agent / Crew</span><span>Work</span><span>Elapsed · cost</span><span className="text-right">Action</span>
            </div>
            <AnimatePresence initial={false}>
              {runs.slice(0, 5).map((run) => {
                const agent = agentById.get(run.invoking_agent_id)
                const crew = crewById.get(run.invoking_crew_id)
                const waiting = run.status === "waiting" || run.status === "paused"
                return (
                  <motion.div
                    layout="position"
                    key={run.id}
                    initial={reduce ? false : { opacity: 0, x: -10 }}
                    animate={{ opacity: 1, x: 0 }}
                    exit={reduce ? undefined : { opacity: 0, x: 12, height: 0 }}
                    transition={{ type: "spring", stiffness: 360, damping: 34 }}
                  >
                    <MotionLink href={`/activity?run=${encodeURIComponent(run.id)}`}>
                      <div className="group grid grid-cols-[minmax(160px,1fr)_minmax(190px,1.4fr)_110px_78px] items-center gap-3 rounded-md border-b border-border/50 px-2 py-2.5 last:border-0 hover:bg-foreground/[0.025]">
                        <span className="flex min-w-0 items-center gap-2.5">
                          <LiveDot tone={waiting ? "warn" : "success"} />
                          <span className="min-w-0">
                            <span className="block truncate text-body font-medium text-foreground/90">
                              {agent?.name || run.pipeline_name || run.pipeline_slug}
                            </span>
                            <span className="block truncate text-label text-muted-foreground">
                              {crew?.name || agent?.crew?.name || "Unassigned"}
                            </span>
                          </span>
                        </span>
                        <span className="min-w-0">
                          <span className="block truncate text-body text-foreground/80">
                            {run.issue_identifier ? `${run.issue_identifier} · ` : ""}{run.pipeline_name || run.pipeline_slug}
                          </span>
                          <span className={cn("block truncate text-label", waiting ? "text-warn" : "text-muted-foreground")}>
                            {waiting ? "Waiting for approval" : run.current_step_id || "Starting…"}
                          </span>
                        </span>
                        <span className="font-mono text-label tabular-nums text-muted-foreground">
                          {elapsed(run.started_at, now)}
                          {run.cost_usd > 0 && <span className="text-muted-foreground-soft"> · ${run.cost_usd.toFixed(2)}</span>}
                        </span>
                        <span className="inline-flex items-center justify-end gap-1 text-label font-medium text-primary-hover">
                          View <ChevronRight className="h-3.5 w-3.5" />
                        </span>
                      </div>
                    </MotionLink>
                  </motion.div>
                )
              })}
            </AnimatePresence>
          </div>
        </div>
      )}
      {children}
    </DashboardCard>
  )
}

export function UpNext({ schedules }: { schedules: PipelineSchedule[] }) {
  const upcoming = React.useMemo(() => {
    const now = Date.now()
    return schedules
      .filter((schedule) => schedule.enabled && schedule.next_run_at && new Date(schedule.next_run_at).getTime() > now)
      .sort((a, b) => new Date(a.next_run_at!).getTime() - new Date(b.next_run_at!).getTime())
      .slice(0, 5)
  }, [schedules])

  return (
    <DashboardCard
      title="Up next"
      icon={CalendarClock}
      hint={upcoming.length > 0 ? `${upcoming.length} scheduled` : "none queued"}
      action={<Link href="/routines" className="text-primary-hover hover:underline">Routines →</Link>}
      className="h-full"
    >
      {upcoming.length === 0 ? (
        <InlineEmpty icon={CalendarClock} text="Nothing scheduled." action={<Link href="/routines" className="text-primary-hover hover:underline">Add a trigger →</Link>} />
      ) : (
        <div className="flex flex-col">
          {upcoming.map((schedule) => (
            <MotionLink key={schedule.id} href={`/routines?routine=${encodeURIComponent(schedule.target_pipeline_slug || "")}`}>
              <div className="group flex items-center gap-3 rounded-md border-b border-border/50 px-1 py-2.5 last:border-0 hover:bg-foreground/[0.025]">
                <span className="flex h-7 w-7 shrink-0 items-center justify-center rounded-md border border-purple/20 bg-purple/10 text-purple-hover">
                  <CalendarClock className="h-3.5 w-3.5" />
                </span>
                <span className="min-w-0 flex-1">
                  <span className="block truncate text-body font-medium text-foreground/85">
                    {schedule.target_pipeline_slug || schedule.name}
                  </span>
                  <span className="block truncate text-label text-muted-foreground">{schedule.name}</span>
                </span>
                <span className="shrink-0 font-mono text-label tabular-nums text-muted-foreground">
                  {formatRelativeTime(schedule.next_run_at!)}
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
  if (s === "completed" || s === "succeeded" || s === "success" || s === "ok") return "bg-success"
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
  runSeries = [],
  spendUsd = null,
  spendPerRun = null,
}: {
  data: OutcomeKpiData
  window: DashboardWindow
  /** Runs per bucket across all crews — the Completed tile's sparkline. */
  runSeries?: number[]
  /** Metered spend for the window, null when paymaster has no ledger rows. */
  spendUsd?: number | null
  spendPerRun?: number | null
}) {
  const reduce = useReducedMotion()
  const cards = [
    {
      label: "Completed",
      icon: CheckCircle2,
      tone: "text-success bg-success/10 border-success/20",
      value: <MetricNumber value={data.completed} />,
      detail: `successful runs · ${window}`,
      series: runSeries,
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
      value: spendUsd == null ? "—" : `$${spendUsd.toFixed(2)}`,
      detail: spendUsd == null ? "not metered on this billing mode" : spendPerRun != null ? `ledger · $${spendPerRun.toFixed(3)} per run` : "metered ledger",
    },
  ]

  return (
    <div className="grid grid-cols-1 gap-4 sm:grid-cols-2 xl:grid-cols-4">
      {cards.map((card, index) => {
        const Icon = card.icon
        const series = "series" in card ? card.series : undefined
        return (
          <motion.div
            key={card.label}
            whileHover={reduce ? undefined : { y: -3 }}
            transition={{ type: "spring", stiffness: 420, damping: 32 }}
            className="group rounded-xl border border-border/60 bg-card p-4 transition-colors hover:border-border"
          >
            <div className="flex items-start justify-between gap-3">
              <div className="min-w-0">
                <div className="text-label font-semibold uppercase tracking-wider text-muted-foreground">{card.label}</div>
                <div className="mt-2 flex items-end gap-3">
                  <span className="text-[28px] font-semibold leading-none tabular-nums text-foreground">{card.value}</span>
                  {series && series.length > 1 && <Sparkline values={series} color="#1E7BFE" width={84} height={26} className="mb-0.5 opacity-80" />}
                </div>
              </div>
              <motion.span
                initial={reduce ? false : { opacity: 0, rotate: -12, scale: 0.82 }}
                animate={{ opacity: 1, rotate: 0, scale: 1 }}
                transition={{ delay: reduce ? 0 : index * 0.06 + 0.18, type: "spring", stiffness: 360, damping: 25 }}
                className={cn("flex h-9 w-9 shrink-0 items-center justify-center rounded-lg border", card.tone)}
              >
                <Icon className="h-4 w-4" />
              </motion.span>
            </div>
            <div className="mt-2 text-label text-muted-foreground">{card.detail}</div>
          </motion.div>
        )
      })}
    </div>
  )
}

export interface FleetHealthRow {
  crew: CrewSummary
  status: string
  detail: string
  tone: "success" | "warn" | "danger" | "muted" | "blue"
  agents: number
  runningAgents: number
  services: CrewServiceSummary
}

const FLEET_TONE = {
  success: { icon: CheckCircle2, className: "text-success" },
  warn: { icon: Wrench, className: "text-warn" },
  danger: { icon: ShieldAlert, className: "text-destructive" },
  muted: { icon: Clock3, className: "text-muted-foreground" },
  blue: { icon: Play, className: "text-primary-hover" },
} as const

export function FleetHealth({ rows }: { rows: FleetHealthRow[] }) {
  return (
    <DashboardCard title="Fleet health" icon={Bot} hint={`${rows.length} crews`} action={<Link href="/crews" className="text-primary-hover hover:underline">Crews →</Link>} className="h-full">
      {rows.length === 0 ? (
        <EmptyState icon={Bot} title="No crews yet" detail="Create a crew to start building your fleet." />
      ) : (
        <div className="overflow-x-auto">
          <div className="min-w-[510px]">
            <div className="grid grid-cols-[minmax(120px,1fr)_minmax(150px,1.3fr)_80px_84px] gap-3 border-b border-border/60 px-2 pb-2 text-label text-muted-foreground">
              <span>Crew</span><span>Status</span><span>Agents</span><span>Services</span>
            </div>
            {rows.slice(0, 6).map((row) => {
              const meta = FLEET_TONE[row.tone]
              const Icon = meta.icon
              return (
                <MotionLink key={row.crew.id} href={`/crews?crew=${encodeURIComponent(row.crew.slug)}`}>
                  <div className="group grid grid-cols-[minmax(120px,1fr)_minmax(150px,1.3fr)_80px_84px] items-center gap-3 rounded-md border-b border-border/50 px-2 py-2.5 last:border-0 hover:bg-foreground/[0.025]">
                    <span className="truncate text-body font-medium text-primary-hover">{row.crew.name}</span>
                    <span className="flex min-w-0 items-center gap-2">
                      <Icon className={cn("h-4 w-4 shrink-0", meta.className)} />
                      <span className="min-w-0">
                        <span className={cn("block truncate text-body", meta.className)}>{row.status}</span>
                        <span className="block truncate text-micro text-muted-foreground">{row.detail}</span>
                      </span>
                    </span>
                    <span className="font-mono text-label tabular-nums text-muted-foreground">{row.runningAgents}/{row.agents}</span>
                    <span className={cn("font-mono text-label tabular-nums", row.services.degraded > 0 ? "text-warn" : "text-muted-foreground")}>
                      {row.services.checked ? `${row.services.running}/${row.services.total}` : "—"}
                    </span>
                  </div>
                </MotionLink>
              )
            })}
          </div>
        </div>
      )}
    </DashboardCard>
  )
}

const WORK_STATUS: Record<string, { label: string; className: string; Icon: LucideIcon }> = {
  COMPLETED: { label: "Completed", className: "text-success", Icon: CheckCircle2 },
  DONE: { label: "Completed", className: "text-success", Icon: CheckCircle2 },
  FAILED: { label: "Failed", className: "text-destructive", Icon: XCircle },
  REVIEW: { label: "Review", className: "text-warn", Icon: Clock3 },
  IN_PROGRESS: { label: "Running", className: "text-primary-hover", Icon: Play },
}

export function RecentWork({ missions }: { missions: Mission[] }) {
  return (
    <DashboardCard title="Recent work" icon={CheckCircle2} hint={missions.length > 0 ? `latest ${Math.min(5, missions.length)}` : "no work yet"} action={<Link href="/issues" className="text-primary-hover hover:underline">Issues →</Link>}>
      {missions.length === 0 ? (
        <EmptyState icon={CheckCircle2} title="No recent work" detail="Issues and assignments will appear here." />
      ) : (
        <div className="overflow-x-auto">
          <div className="min-w-[700px]">
            <div className="grid grid-cols-[72px_minmax(210px,1fr)_130px_100px_80px_72px] gap-3 border-b border-border/60 px-2 pb-2 text-label text-muted-foreground">
              <span>Issue</span><span>Title</span><span>Owner</span><span>Outcome</span><span>Duration</span><span className="text-right">Est.</span>
            </div>
            {missions.slice(0, 5).map((mission) => {
              const status = WORK_STATUS[mission.status] ?? { label: mission.status.toLowerCase().replace("_", " "), className: "text-muted-foreground", Icon: Clock3 }
              const Icon = status.Icon
              const duration = mission.completed_at
                ? Math.max(0, new Date(mission.completed_at).getTime() - new Date(mission.created_at).getTime())
                : 0
              return (
                <MotionLink key={mission.id} href={mission.identifier ? `/issues/${mission.identifier}` : "/issues"}>
                  <div className="group grid grid-cols-[72px_minmax(210px,1fr)_130px_100px_80px_72px] items-center gap-3 rounded-md border-b border-border/50 px-2 py-2.5 last:border-0 hover:bg-foreground/[0.025]">
                    <span className="truncate font-mono text-label text-primary-hover">{mission.identifier ?? "—"}</span>
                    <span className="truncate text-body text-foreground/85">{mission.title}</span>
                    <span className="truncate text-label text-muted-foreground">{mission.assignee_name || mission.lead_agent_name || mission.crew_name || "Unassigned"}</span>
                    <span className={cn("inline-flex items-center gap-1.5 text-label", status.className)}><Icon className="h-3.5 w-3.5" />{status.label}</span>
                    <span className="font-mono text-label tabular-nums text-muted-foreground">{duration > 0 ? formatDuration(duration) : "—"}</span>
                    <span className="text-right font-mono text-label tabular-nums text-muted-foreground">
                      {mission.total_estimated_cost == null ? "—" : `$${mission.total_estimated_cost.toFixed(2)}`}
                    </span>
                  </div>
                </MotionLink>
              )
            })}
          </div>
        </div>
      )}
    </DashboardCard>
  )
}

export function SystemSignals({
  capacity,
  memory,
  credentialGapCount,
  heldCrews,
  services,
  realtimeStatus,
}: {
  capacity: RuntimeCapacityResponse | null
  memory: MemoryHealthResponse | null
  credentialGapCount: number
  heldCrews: NonNullable<RuntimeCapacityResponse["held"]>
  services: { running: number; total: number; checked: number; unchecked: number }
  realtimeStatus?: string
}) {
  const cap = capacitySignal(capacity, heldCrews)
  const rows = [
    {
      label: "Runtime capacity",
      value: cap.value,
      href: "/settings",
      icon: ServerCog,
      tone: cap.tone,
    },
    {
      label: "Memory health",
      value: memory ? `${Math.round(memory.overall)}` : "Unavailable",
      href: "/crews",
      icon: Brain,
      tone: memory == null ? "text-muted-foreground" : memory.overall >= 80 ? "text-success" : memory.overall >= 60 ? "text-warn" : "text-destructive",
    },
    {
      label: "Credentials",
      value: credentialGapCount > 0 ? `${credentialGapCount} tool gap${credentialGapCount === 1 ? "" : "s"}` : "Ready",
      href: "/credentials",
      icon: KeyRound,
      tone: credentialGapCount > 0 ? "text-warn" : "text-success",
    },
    {
      label: "Services",
      // `total` counts only the crews whose /services call answered, so an
      // all-green "6/6 running" could be hiding two crews nobody reached.
      // FleetHealth already renders those per-row as "—"; say so here too
      // rather than paint success over an unknown.
      value:
        services.checked === 0
          ? "Unavailable"
          : services.unchecked > 0
            ? `${services.running}/${services.total} running · ${services.unchecked} unchecked`
            : `${services.running}/${services.total} running`,
      href: "/crews",
      icon: ServerCog,
      tone:
        services.checked === 0
          ? "text-muted-foreground"
          : services.unchecked > 0
            ? "text-warn"
            : services.running === services.total
              ? "text-success"
              : "text-warn",
    },
  ]

  if (realtimeStatus) {
    rows.push({
      label: "Realtime",
      value: realtimeStatus === "connected" ? "connected" : realtimeStatus,
      href: "/activity",
      icon: Gauge,
      tone: realtimeStatus === "connected" ? "text-success" : realtimeStatus === "connecting" ? "text-warn" : "text-destructive",
    })
  }

  return (
    <DashboardCard title="System" icon={Gauge} hint="live checks" className="h-full">
      <div className="grid grid-cols-1 gap-2 sm:grid-cols-2">
        {rows.map((row) => {
          const Icon = row.icon
          return (
            <MotionLink key={row.label} href={row.href}>
              <div className="group flex items-center gap-2.5 rounded-lg border border-border/60 px-3 py-2 transition-colors hover:border-border hover:bg-foreground/[0.025]">
                <Icon className="h-3.5 w-3.5 shrink-0 text-muted-foreground" />
                <span className="min-w-0 flex-1 truncate text-label text-foreground/85">{row.label}</span>
                <span className={cn("shrink-0 truncate text-label font-medium", row.tone)}>{row.value}</span>
              </div>
            </MotionLink>
          )
        })}
      </div>
    </DashboardCard>
  )
}



function EmptyState({ icon: Icon, title, detail }: { icon: LucideIcon; title: string; detail: string }) {
  return (
    <div className="flex min-h-[150px] flex-col items-center justify-center gap-2 px-4 text-center">
      <span className="flex h-9 w-9 items-center justify-center rounded-lg border border-border/60 bg-foreground/[0.025] text-muted-foreground">
        <Icon className="h-4 w-4" />
      </span>
      <span className="text-body font-medium text-foreground/80">{title}</span>
      <span className="max-w-[280px] text-label text-muted-foreground-soft">{detail}</span>
    </div>
  )
}

export function buildAttentionItems({
  inbox,
  heldCrews,
  credentialGapCount,
}: {
  inbox: InboxItem[]
  heldCrews: NonNullable<RuntimeCapacityResponse["held"]>
  credentialGapCount: number
}): AttentionItem[] {
  const items: AttentionItem[] = []
  const approvals = inbox.filter((item) => item.kind === "waitpoint" || item.kind === "escalation")
  const failures = inbox.filter((item) => item.kind === "failed_run" || item.kind === "schedule_circuit_breaker_tripped")
  const scheduleProblems = inbox.filter((item) => item.kind === "schedule_missed")
  const held = heldCrews.length

  if (approvals.length > 0) items.push({ id: "approvals", label: `${approvals.length} approval${approvals.length === 1 ? "" : "s"} waiting`, detail: "Review pending decisions", href: "/inbox", tone: "warn", icon: Clock3 })
  if (failures.length > 0) items.push({ id: "failures", label: `${failures.length} failed run${failures.length === 1 ? "" : "s"}`, detail: "Investigate and retry", href: "/inbox?kind=failed_run", tone: "danger", icon: XCircle })
  if (held > 0) items.push({ id: "capacity", label: `${held} crew${held === 1 ? "" : "s"} waiting for capacity`, detail: heldCrews[0]?.detail || "View host admission details", href: "/settings", tone: "purple", icon: Gauge })
  if (credentialGapCount > 0) items.push({ id: "credentials", label: `${credentialGapCount} credential tool gap${credentialGapCount === 1 ? "" : "s"}`, detail: "Install missing crew tools", href: "/credentials", tone: "blue", icon: KeyRound })
  if (scheduleProblems.length > 0) items.push({ id: "schedules", label: `${scheduleProblems.length} schedule alert${scheduleProblems.length === 1 ? "" : "s"}`, detail: "Review missed or disabled routines", href: "/inbox", tone: "warn", icon: CalendarClock })
  return items
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
): OutcomeKpiData {
  const ok = insights?.totals.succeeded ?? 0
  const failed = insights?.totals.failed ?? 0
  const finished = ok + failed
  return {
    completed: ok,
    successPct: finished > 0 ? Math.round((ok / finished) * 100) : null,
    successOk: ok,
    successTotal: finished,
    p95Ms: insights?.duration.p95_ms ?? 0,
  }
}
