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
  CircleDollarSign,
  Clock3,
  Gauge,
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
import { Progress } from "@/components/ui/progress"
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

export function AttentionStrip({ items }: { items: AttentionItem[] }) {
  const reduce = useReducedMotion()
  const visible = items.slice(0, 3)

  return (
    <div
      className={cn(
        "overflow-hidden rounded-xl border bg-card",
        visible.length > 0 ? "border-warn/20" : "border-success/20",
      )}
    >
      <div className="flex items-center justify-between border-b border-border/60 px-4 py-3">
        <div className="flex items-center gap-2">
          {visible.length > 0 ? (
            <AlertTriangle className="h-4 w-4 text-warn" />
          ) : (
            <CheckCircle2 className="h-4 w-4 text-success" />
          )}
          <h2 className="text-body font-semibold">
            {visible.length > 0 ? "Needs your attention" : "All clear"}
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

      {visible.length === 0 ? (
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
                  initial={reduce ? false : { opacity: 0, y: 8 }}
                  animate={{ opacity: 1, y: 0 }}
                  exit={reduce ? undefined : { opacity: 0, y: -8 }}
                  transition={{ duration: 0.28, delay: reduce ? 0 : index * 0.06 }}
                >
                  <MotionLink href={item.href} className="h-full">
                    <div className="group flex h-full items-center gap-3 px-4 py-4 transition-colors hover:bg-foreground/[0.025]">
                      <span className={cn("flex h-9 w-9 shrink-0 items-center justify-center rounded-lg border", ATTENTION_TONE[item.tone])}>
                        <Icon className="h-4 w-4" />
                      </span>
                      <span className="min-w-0 flex-1">
                        <span className="block truncate text-body font-semibold text-foreground/90">{item.label}</span>
                        <span className="mt-0.5 block truncate text-label text-muted-foreground">{item.detail}</span>
                      </span>
                      <ChevronRight className="h-4 w-4 shrink-0 text-muted-foreground-soft transition-colors group-hover:text-foreground" />
                    </div>
                  </MotionLink>
                </motion.div>
              )
            })}
          </AnimatePresence>
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
}: {
  runs: PipelineRun[]
  agents: AgentSummary[]
  crews: CrewSummary[]
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
        <EmptyState icon={CheckCircle2} title="Nothing is running" detail="The fleet is ready for its next assignment." />
      ) : (
        <div className="overflow-x-auto">
          <div className="min-w-[650px]">
            <div className="grid grid-cols-[minmax(160px,1fr)_minmax(190px,1.4fr)_90px_78px] gap-3 border-b border-border/60 px-2 pb-2 text-label text-muted-foreground">
              <span>Agent / Crew</span><span>Work</span><span>Elapsed</span><span className="text-right">Action</span>
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
                      <div className="group grid grid-cols-[minmax(160px,1fr)_minmax(190px,1.4fr)_90px_78px] items-center gap-3 rounded-md border-b border-border/50 px-2 py-2.5 last:border-0 hover:bg-foreground/[0.025]">
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
                        <span className="font-mono text-label tabular-nums text-muted-foreground">{elapsed(run.started_at, now)}</span>
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
        <EmptyState icon={CalendarClock} title="Nothing scheduled" detail="Add a trigger to a routine to see it here." />
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
                <ChevronRight className="h-3.5 w-3.5 shrink-0 text-muted-foreground-soft group-hover:text-foreground" />
              </div>
            </MotionLink>
          ))}
        </div>
      )}
    </DashboardCard>
  )
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
  cost: number
  budgetSpent: number
  budgetTotal: number
  p95Ms: number
}

export function OutcomeKpis({ data, window }: { data: OutcomeKpiData; window: DashboardWindow }) {
  const reduce = useReducedMotion()
  const budgetPct = data.budgetTotal > 0 ? Math.min(100, (data.budgetSpent / data.budgetTotal) * 100) : 0
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
    {
      label: "Actual cost",
      icon: CircleDollarSign,
      tone: "text-warn bg-warn/10 border-warn/20",
      value: `$${data.cost.toFixed(2)}`,
      detail: data.budgetTotal > 0 ? `$${data.budgetSpent.toFixed(2)} of $${data.budgetTotal.toFixed(2)} monthly` : `ledger spend · ${window}`,
      progress: data.budgetTotal > 0 ? budgetPct : undefined,
    },
    {
      label: "P95 duration",
      icon: TimerReset,
      tone: "text-purple-hover bg-purple/10 border-purple/20",
      value: data.p95Ms > 0 ? formatDuration(data.p95Ms) : "—",
      detail: data.p95Ms > 0 ? "95% finish within this" : "no duration samples",
    },
  ]

  return (
    <div className="grid grid-cols-1 gap-4 sm:grid-cols-2 xl:grid-cols-4">
      {cards.map((card, index) => {
        const Icon = card.icon
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
                <div className="mt-2 text-[28px] font-semibold leading-none tabular-nums text-foreground">{card.value}</div>
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
            {card.progress != null && (
              <Progress value={card.progress} className="mt-3 h-1 bg-foreground/[0.06]" indicatorClassName={card.progress >= 100 ? "bg-destructive" : "bg-warn"} />
            )}
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
  services,
}: {
  capacity: RuntimeCapacityResponse | null
  memory: MemoryHealthResponse | null
  credentialGapCount: number
  services: { running: number; total: number; checked: number }
}) {
  const held = capacity?.held?.length ?? 0
  const rows = [
    {
      label: "Runtime capacity",
      value: held > 0 ? `${held} held` : capacity?.enabled === false ? "Disabled" : "Available",
      href: "/settings",
      icon: ServerCog,
      tone: held > 0 ? "text-warn" : capacity?.enabled === false ? "text-muted-foreground" : "text-success",
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
      value: services.checked === 0 ? "Unavailable" : `${services.running}/${services.total} running`,
      href: "/crews",
      icon: ServerCog,
      tone: services.checked === 0 ? "text-muted-foreground" : services.running === services.total ? "text-success" : "text-warn",
    },
  ]

  return (
    <DashboardCard title="System signals" icon={Gauge} hint="live checks" className="h-full">
      <div className="flex flex-col">
        {rows.map((row) => {
          const Icon = row.icon
          return (
            <MotionLink key={row.label} href={row.href}>
              <div className="group flex items-center gap-3 rounded-md border-b border-border/50 px-1 py-2.5 last:border-0 hover:bg-foreground/[0.025]">
                <Icon className="h-4 w-4 shrink-0 text-muted-foreground" />
                <span className="min-w-0 flex-1 truncate text-body text-foreground/80">{row.label}</span>
                <span className={cn("shrink-0 text-label font-medium", row.tone)}>{row.value}</span>
                <ChevronRight className="h-3.5 w-3.5 shrink-0 text-muted-foreground-soft group-hover:text-foreground" />
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
  capacity,
  credentialGapCount,
}: {
  inbox: InboxItem[]
  capacity: RuntimeCapacityResponse | null
  credentialGapCount: number
}): AttentionItem[] {
  const items: AttentionItem[] = []
  const approvals = inbox.filter((item) => item.kind === "waitpoint" || item.kind === "escalation")
  const failures = inbox.filter((item) => item.kind === "failed_run" || item.kind === "schedule_circuit_breaker_tripped")
  const scheduleProblems = inbox.filter((item) => item.kind === "schedule_missed")
  const held = capacity?.held?.length ?? 0

  if (approvals.length > 0) items.push({ id: "approvals", label: `${approvals.length} approval${approvals.length === 1 ? "" : "s"} waiting`, detail: "Review pending decisions", href: "/inbox", tone: "warn", icon: Clock3 })
  if (failures.length > 0) items.push({ id: "failures", label: `${failures.length} failed run${failures.length === 1 ? "" : "s"}`, detail: "Investigate and retry", href: "/inbox?kind=failed_run", tone: "danger", icon: XCircle })
  if (held > 0) items.push({ id: "capacity", label: `${held} crew${held === 1 ? "" : "s"} waiting for capacity`, detail: capacity?.held?.[0]?.detail || "View host admission details", href: "/settings", tone: "purple", icon: Gauge })
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
  cost: number,
  budgetSpent: number,
  budgetTotal: number,
): OutcomeKpiData {
  const ok = insights?.totals.succeeded ?? 0
  const failed = insights?.totals.failed ?? 0
  const finished = ok + failed
  return {
    completed: ok,
    successPct: finished > 0 ? Math.round((ok / finished) * 100) : null,
    successOk: ok,
    successTotal: finished,
    cost,
    budgetSpent,
    budgetTotal,
    p95Ms: insights?.duration.p95_ms ?? 0,
  }
}
