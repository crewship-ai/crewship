"use client"

// The Activity overview — two questions, and the evidence for each.
//
// A person opens this page for one of exactly two reasons: something might
// be waiting on them, or something might be broken. The first draft answered
// neither. It opened with four equal KPI tiles and an ACTIVITY MIX donut
// counting events by type — "how many messages versus runs" — which is a
// number with no question behind it, and it pushed the two things that ARE
// asked below the fold at a quarter of the weight.
//
// So the page is now shaped like the questions:
//
//   a one-line NOW strip     — what is in flight, small, because "3 agents
//                              are working" is a glance, not a quarter of
//                              the screen
//   WAITING ON YOU / FAILURES— side by side, each with its number big and
//                              its evidence underneath
//   LATEST ACTIVITY          — kept: at scope "all" this component IS the
//                              whole content area, so dropping it would
//                              leave no way to see a recent event at all
//   FAILURES · N DAYS        — kept, but re-aimed from "how much happened"
//                              at "is this breakage new", which is part of
//                              the second question rather than a third one.
//                              N is what the window can speak for, and the
//                              card hides itself below two days.
//
// The judgements — which zero is which, what still counts as waiting on a
// person, what one broken thing is — are in lib/activity-overview.ts, where
// they can be tested without a DOM.
//
// Every card is built from the same KpiCard / DashboardCard vocabulary as
// the Routines overview, so the two pages stay one object at two subjects.

import * as React from "react"
import {
  Activity as ActivityIcon,
  AlertTriangle,
  Bell,
  Bot,
  Brain,
  Coins,
  FileText,
  Inbox,
  MessageSquare,
  ShieldCheck,
  Terminal,
  TrendingDown,
  Workflow,
} from "lucide-react"
import type { LucideIcon } from "lucide-react"
import { Bar, BarChart, CartesianGrid, XAxis, YAxis } from "recharts"

import {
  ChartContainer,
  ChartTooltip,
  ChartTooltipContent,
  type ChartConfig,
} from "@/components/ui/chart"
import { Appear } from "@/components/ui/detail"
import { KpiCard } from "@/components/features/dashboard/kpi-card"
import { DashboardCard } from "@/components/features/dashboard/dashboard-card"
import { cn } from "@/lib/utils"
import {
  activitySource,
  dailyCounts,
  formatDurationMs,
  type ActivityScope,
  type ActivitySource,
  type SpineLabels,
  type SpineLink,
} from "@/lib/activity-stream"
import {
  failureClusters,
  liveSignal,
  openAsks,
  windowSpanDays,
  zeroCopy,
  zeroKind,
} from "@/lib/activity-overview"
import type { JournalEntry } from "@/lib/types/journal"

import { FeedRow } from "./feed-row"

/** One icon per source — the tile in every row. */
const SOURCE_ICON: Record<ActivitySource, LucideIcon> = {
  run: Terminal,
  // Same glyph the Routines page wears in its SubBar.
  routine: Workflow,
  issue: FileText,
  human: Bell,
  security: ShieldCheck,
  cost: Coins,
  memory: Brain,
  comms: MessageSquare,
  system: Bot,
}

export function iconFor(entry: JournalEntry): LucideIcon {
  return SOURCE_ICON[activitySource(entry.entry_type)] ?? Bot
}

function Empty({ icon: Icon, children }: { icon: LucideIcon; children: React.ReactNode }) {
  return (
    <div className="flex flex-col items-center justify-center gap-1.5 py-7 text-center">
      <Icon className="h-4 w-4 text-muted-foreground-soft" />
      <p className="max-w-[300px] text-[11px] leading-relaxed text-muted-foreground-soft">{children}</p>
    </div>
  )
}

/** Reads a token to a real colour — recharts needs a value, not a var(). */
function tokenColor(token: string): string {
  if (typeof window === "undefined") return "currentColor"
  return getComputedStyle(document.documentElement).getPropertyValue(token).trim() || "currentColor"
}

export interface ActivityOverviewProps {
  entries: JournalEntry[]
  rangeLabel: string
  labels: SpineLabels
  agentName: (id?: string) => string | undefined
  crewName: (id?: string) => string | undefined
  crewMeta: (id?: string) => { icon?: string | null; color?: string | null } | undefined
  selectedID?: string
  onSelect: (e: JournalEntry) => void
  onSpineClick: (l: SpineLink) => void
  onScope: (s: ActivityScope | "all") => void
}

export function ActivityOverview({
  entries,
  rangeLabel,
  labels,
  agentName,
  crewName,
  crewMeta,
  selectedID,
  onSelect,
  onSpineClick,
  onScope,
}: ActivityOverviewProps) {
  const total = entries.length

  // Question 1. Not "rows of type approval.requested" — those stay in the
  // log after they are answered, so counting them reports a queue that was
  // already cleared. See lib/activity-overview.ts.
  const waiting = React.useMemo(() => openAsks(entries), [entries])

  // Question 2. Grouped by the thing that is broken, so one routine failing
  // nine times is one row and not the whole panel.
  const broken = React.useMemo(() => failureClusters(entries), [entries])
  const failedTotal = React.useMemo(() => broken.reduce((n, c) => n + c.count, 0), [broken])

  const live = React.useMemo(() => liveSignal(entries), [entries])
  const latest = React.useMemo(() => entries.slice(0, 8), [entries])

  // Only as many columns as the window can actually speak for. A fixed 7
  // drawn from a 24-hour window is six empty bars that read as six quiet
  // days — the "nothing broke" lie, told with an axis. Below two days there
  // is no trend at all and the card does not render.
  const spanDays = React.useMemo(() => windowSpanDays(entries), [entries])
  const showTrend = spanDays >= 2
  const days = React.useMemo(
    () => (showTrend ? dailyCounts(entries, spanDays) : []),
    [entries, spanDays, showTrend],
  )
  const trendErrors = React.useMemo(() => days.reduce((n, d) => n + d.errors, 0), [days])
  const trendTotal = React.useMemo(() => days.reduce((n, d) => n + d.total, 0), [days])

  // A zero says which zero it is. "nothing broke" is a claim about the
  // world; this window only ever knew about itself.
  const waitingZero = zeroKind(total, waiting.length)
  const brokenZero = zeroKind(total, failedTotal)
  const waitingCopy = waitingZero
    ? zeroCopy(waitingZero, total, "an approval, escalation or keeper request still open")
    : null
  const brokenCopy = brokenZero ? zeroCopy(brokenZero, total, "a failure") : null

  const chartConfig = React.useMemo<ChartConfig>(
    () => ({ errors: { label: "Failed", color: tokenColor("--destructive") } }),
    [],
  )

  const rowProps = (e: JournalEntry) => ({
    entry: e,
    icon: iconFor(e),
    labels,
    actorName: agentName(e.agent_id),
    crewName: crewName(e.crew_id),
    agentId: e.agent_id,
    crewIcon: crewMeta(e.crew_id)?.icon,
    crewColor: crewMeta(e.crew_id)?.color,
    selected: selectedID === e.id,
    onSelect: () => onSelect(e),
    onSpineClick,
  })

  return (
    <div className="mx-auto flex max-w-[1800px] flex-col gap-4 p-4 md:p-6">
      <Appear order={0}>
        <div>
          <h1 className="text-lg font-semibold tracking-tight">Overview</h1>
          <p className="text-xs text-muted-foreground">
            What needs you, and what broke · {total.toLocaleString()}{" "}
            {total === 1 ? "event" : "events"} in {rangeLabel.toLowerCase()}
          </p>
        </div>
      </Appear>

      {/* ── The four numbers ──────────────────────────────────────────
          One row of KPI tiles, the same rhythm the Routines overview
          opens with — four tiles, then two cards, then the wide/narrow
          pair. The two pages are one object at two subjects, and a
          different opening shape is the first thing a reader notices.

          This was a one-line strip, on the argument that "three agents
          are working" is a glance rather than a quarter of the screen.
          That argument was about WEIGHT and it was right; it was applied
          by shrinking the row instead of by choosing what goes in it.
          The four here are the four questions this page exists for, and
          each is a place to go rather than a number to read. */}
      <Appear order={1}>
        <div className="grid grid-cols-2 gap-4 lg:grid-cols-4">
          <KpiCard
            label="Running now"
            value={live.running}
            subtitle={
              live.running > 0
                ? `${live.agents} ${live.agents === 1 ? "agent" : "agents"} at work`
                : total === 0
                  ? "nothing recorded in this window"
                  : "nothing in flight"
            }
            valueColor={live.running > 0 ? tokenColor("--primary") : undefined}
            onClick={live.running > 0 ? () => onScope("active") : undefined}
          />
          <KpiCard
            label="Waiting on you"
            value={waiting.length}
            subtitle={
              waitingCopy ? waitingCopy.subtitle : "approvals, escalations & keeper requests still open"
            }
            valueColor={waiting.length > 0 ? tokenColor("--warn") : undefined}
            onClick={waiting.length > 0 ? () => onScope("waiting") : undefined}
          />
          <KpiCard
            label="Failures"
            value={failedTotal}
            subtitle={
              brokenCopy
                ? brokenCopy.subtitle
                : `${broken.length} ${broken.length === 1 ? "thing" : "things"} failing`
            }
            valueColor={failedTotal > 0 ? tokenColor("--destructive") : undefined}
            onClick={failedTotal > 0 ? () => onScope("failed") : undefined}
          />
          <KpiCard
            label={`Spend · ${rangeLabel.toLowerCase()}`}
            // Not a placeholder zero: an em dash says "nothing was billed",
            // where $0.00 reads as a measurement that came back empty.
            value={live.spendUSD > 0 ? `$${live.spendUSD.toFixed(2)}` : "—"}
            subtitle={
              live.slowestMs != null ? `slowest ${formatDurationMs(live.slowestMs)}` : "no priced work yet"
            }
          />
        </div>
      </Appear>

      {/* ── The two questions ─────────────────────────────────────────
          Side by side, each with its evidence. The numbers moved up into
          the KPI row; repeating them here would put the same figure on
          screen twice, which is how two copies of one number start to
          disagree. */}
      <Appear order={2}>
        <div className="grid grid-cols-1 gap-4 lg:grid-cols-2">
          {/* 1. Is anything waiting on me? */}
          <div className="flex flex-col gap-4">
            <DashboardCard
              title="Open asks"
              icon={Bell}
              hint={waiting.length > 0 ? `${waiting.length} open` : undefined}
              action={
                waiting.length > 0 ? (
                  <button
                    type="button"
                    onClick={() => onScope("waiting")}
                    className="text-primary hover:underline"
                  >
                    Show all →
                  </button>
                ) : undefined
              }
            >
              {waiting.length === 0 ? (
                <Empty icon={Inbox}>{waitingCopy?.panel}</Empty>
              ) : (
                <div className="flex flex-col">
                  {waiting.slice(0, 6).map((e) => (
                    <FeedRow key={e.id} {...rowProps(e)} />
                  ))}
                </div>
              )}
            </DashboardCard>
          </div>

          {/* 2. What is broken? */}
          <div className="flex flex-col gap-4">
            <DashboardCard
              title="What is broken"
              icon={AlertTriangle}
              hint={failedTotal > 0 ? `${failedTotal} events` : undefined}
              action={
                failedTotal > 0 ? (
                  <button
                    type="button"
                    onClick={() => onScope("failed")}
                    className="text-primary hover:underline"
                  >
                    Show all →
                  </button>
                ) : undefined
              }
            >
              {broken.length === 0 ? (
                <Empty icon={ShieldCheck}>{brokenCopy?.panel}</Empty>
              ) : (
                <div className="flex flex-col">
                  {/* One row per broken THING, with how many times it went
                      wrong — nine rows from one routine is one thing to fix
                      and used to eat the whole panel. */}
                  {broken.slice(0, 6).map((c) => (
                    <div key={c.key} className="flex items-center gap-1.5">
                      <div className="min-w-0 flex-1">
                        <FeedRow {...rowProps(c.latest)} />
                      </div>
                      {c.count > 1 && (
                        <span className="shrink-0 rounded bg-destructive/15 px-1.5 py-0.5 font-mono text-[10px] tabular-nums text-destructive">
                          ×{c.count}
                        </span>
                      )}
                    </div>
                  ))}
                </div>
              )}
            </DashboardCard>
          </div>
        </div>
      </Appear>

      {/* ── Everything else, at the weight of everything else ───────── */}
      <Appear order={3}>
        <div className={cn("grid grid-cols-1 gap-4", showTrend && "lg:grid-cols-3")}>
          <DashboardCard
            className={cn(showTrend && "lg:col-span-2")}
            title="Latest activity"
            icon={ActivityIcon}
            hint={latest.length > 0 ? `last ${latest.length}` : undefined}
            action={
              <a href="/journal" className="text-primary hover:underline">
                Full journal →
              </a>
            }
          >
            {latest.length === 0 ? (
              <Empty icon={ActivityIcon}>
                No events were recorded in this window. Widen the range or clear a filter.
              </Empty>
            ) : (
              <div className="flex flex-col">
                {latest.map((e) => (
                  <FeedRow key={e.id} {...rowProps(e)} />
                ))}
              </div>
            )}
          </DashboardCard>

          {/* Not "how much happened" — that was the mix donut's question in
              another shape. This one asks whether the breakage is new, which
              is the second half of "what is broken".

              Rendered only when the loaded window spans more than one day.
              A fixed seven columns over a 24-hour range is six bars that are
              empty because nobody asked, drawn as if they were quiet. */}
          {showTrend && (
            <DashboardCard
              title={`Failures · ${spanDays} days`}
              icon={TrendingDown}
              hint={trendTotal > 0 ? `${trendErrors} of ${trendTotal}` : undefined}
            >
              <ChartContainer config={chartConfig} className="aspect-auto h-[196px] w-full">
                <BarChart data={days} margin={{ top: 8, right: 8, left: -22, bottom: 0 }}>
                  <CartesianGrid vertical={false} strokeOpacity={0.08} />
                  <XAxis
                    dataKey="label"
                    tickLine={false}
                    axisLine={false}
                    tickMargin={6}
                    className="text-[10px]"
                  />
                  <YAxis allowDecimals={false} tickLine={false} axisLine={false} className="text-[10px]" />
                  <ChartTooltip content={<ChartTooltipContent />} />
                  {/* Failures only. This was a volume column with a red cap
                      stacked on it, and on a real instance the volume (260)
                      squashed the failures (3) into an unreadable sliver —
                      a chart titled "Failures" whose failures could not be
                      compared between days. The rate that band carried is
                      in the header instead ("11 of 300"). */}
                  <Bar dataKey="errors" fill="var(--color-errors)" radius={[2, 2, 0, 0]} />
                </BarChart>
              </ChartContainer>
            </DashboardCard>
          )}
        </div>
      </Appear>
    </div>
  )
}
