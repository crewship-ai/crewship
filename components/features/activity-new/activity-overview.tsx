"use client"

// The Activity overview — a card dashboard, not a log.
//
// This page and /journal read the SAME table, and the first draft of this
// file forgot that and rebuilt the journal: a seven-column grid of ts /
// type / severity rows. The journal already does that, better. What was
// missing is the question you ask BEFORE you go reading rows — what is
// running, what is stuck on me, what broke, and is today normal — which is
// a dashboard question and gets dashboard answers.
//
// Every card is built from the same KpiCard / DashboardCard / StatusDonut
// vocabulary as the Routines overview, so the two pages are the same object
// at different subjects rather than two houses in one street.

import * as React from "react"
import {
  Activity as ActivityIcon,
  AlertTriangle,
  Ban,
  Bell,
  Bot,
  Brain,
  CircleDot,
  Coins,
  FileText,
  MessageSquare,
  PieChart,
  Play,
  ShieldCheck,
  Terminal,
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
import { StatusDonut } from "@/components/features/dashboard/status-donut"
import {
  ACTIVITY_SCOPES,
  activitySource,
  dailyCounts,
  formatDurationMs,
  entryCostUSD,
  entryDurationMs,
  scopeOf,
  sourceMix,
  type ActivityScope,
  type ActivitySource,
  type SpineLabels,
  type SpineLink,
} from "@/lib/activity-stream"
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
      <p className="max-w-[280px] text-[11px] text-muted-foreground-soft">{children}</p>
    </div>
  )
}

/** Reads a token to a real colour — recharts and the donut need a value. */
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
  onSource: (s: ActivitySource) => void
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
  onSource,
}: ActivityOverviewProps) {
  const scoped = React.useMemo(() => {
    const counts: Record<ActivityScope, number> = { active: 0, waiting: 0, failed: 0, done: 0 }
    for (const e of entries) counts[scopeOf(e)] += 1
    return counts
  }, [entries])

  const spend = React.useMemo(
    () => entries.reduce((n, e) => n + (entryCostUSD(e) ?? 0), 0),
    [entries],
  )

  const slowest = React.useMemo(() => {
    let best: { entry: JournalEntry; ms: number } | null = null
    for (const e of entries) {
      const ms = entryDurationMs(e)
      if (ms != null && (best == null || ms > best.ms)) best = { entry: e, ms }
    }
    return best
  }, [entries])

  // Distinct agents that produced anything in the window.
  const workingAgents = React.useMemo(() => {
    const seen = new Set<string>()
    for (const e of entries) if (e.agent_id) seen.add(e.agent_id)
    return seen.size
  }, [entries])

  const mix = React.useMemo(() => sourceMix(entries), [entries])
  const days = React.useMemo(() => dailyCounts(entries, 7), [entries])

  const waiting = React.useMemo(
    () => entries.filter((e) => scopeOf(e) === "waiting").slice(0, 5),
    [entries],
  )
  const failed = React.useMemo(
    () => entries.filter((e) => scopeOf(e) === "failed").slice(0, 5),
    [entries],
  )
  const live = React.useMemo(() => entries.slice(0, 8), [entries])

  const donut = React.useMemo(
    () => mix.map((m) => ({ key: m.key, label: m.label, count: m.count, color: tokenColor(m.token) })),
    [mix],
  )

  const chartConfig = React.useMemo<ChartConfig>(
    () => ({
      total: { label: "Events", color: tokenColor("--info") },
      errors: { label: "Errors", color: tokenColor("--destructive") },
    }),
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
            {entries.length.toLocaleString()} {entries.length === 1 ? "event" : "events"} ·{" "}
            {rangeLabel.toLowerCase()} · every crew, agent, routine and issue in one place
          </p>
        </div>
      </Appear>

      {/* ── What is live, what is stuck, what it cost ─────────────── */}
      <Appear order={1}>
        <div className="grid grid-cols-2 gap-4 lg:grid-cols-4">
          <KpiCard
            label="Running now"
            value={scoped.active}
            subtitle={scoped.active === 0 ? "nothing in flight" : "agents mid-run"}
            valueColor={scoped.active > 0 ? tokenColor("--info") : undefined}
          />
          <KpiCard
            label="Waiting on you"
            value={scoped.waiting}
            subtitle={scoped.waiting === 0 ? "all clear" : "approvals & escalations"}
            valueColor={scoped.waiting > 0 ? tokenColor("--warn") : undefined}
          />
          <KpiCard
            label="Failed"
            value={scoped.failed}
            subtitle={scoped.failed === 0 ? "nothing broke" : rangeLabel.toLowerCase()}
            valueColor={scoped.failed > 0 ? tokenColor("--destructive") : undefined}
          />
          {/* Not spend. Spend is a number you review once a week; "who is
              working right now" is the one you look at when you open this
              page. The cost still shows, as this tile's second line. */}
          <KpiCard
            label="Agents at work"
            value={workingAgents}
            subtitle={
              spend > 0
                ? `$${spend.toFixed(2)} · slowest ${formatDurationMs(slowest?.ms)}`
                : rangeLabel.toLowerCase()
            }
            valueColor={workingAgents > 0 ? tokenColor("--success") : undefined}
          />
        </div>
      </Appear>

      {/* ── The shape of the activity, and what blocks a person ───── */}
      <Appear order={2}>
        <div className="grid grid-cols-1 gap-4 lg:grid-cols-2">
          <DashboardCard title="Activity mix" icon={PieChart} hint={`${entries.length} events`}>
            {donut.length === 0 ? (
              <Empty icon={PieChart}>Nothing was recorded in this window.</Empty>
            ) : (
              // The arcs sum to the number in the header, and a click on a
              // slice filters the page — a donut you cannot act on is a
              // picture of your data, not a way into it.
              <StatusDonut
                data={donut}
                centerLabel="events"
                onSelect={(key) => onSource(key as ActivitySource)}
              />
            )}
          </DashboardCard>

          <DashboardCard
            title="Waiting on you"
            icon={Bell}
            hint={waiting.length > 0 ? `${scoped.waiting} pending` : "all clear"}
            action={
              scoped.waiting > 0 ? (
                <button type="button" onClick={() => onScope("waiting")} className="text-primary hover:underline">
                  Show all →
                </button>
              ) : undefined
            }
          >
            {waiting.length === 0 ? (
              <Empty icon={ShieldCheck}>
                No approval, escalation or keeper request is blocked on a person.
              </Empty>
            ) : (
              <div className="flex flex-col">
                {waiting.map((e) => (
                  <FeedRow key={e.id} {...rowProps(e)} />
                ))}
              </div>
            )}
          </DashboardCard>
        </div>
      </Appear>

      {/* ── The feed itself, and what broke ──────────────────────── */}
      <Appear order={3}>
        <div className="grid grid-cols-1 gap-4 lg:grid-cols-2">
          <DashboardCard
            title="Latest activity"
            icon={ActivityIcon}
            hint={live.length > 0 ? `last ${live.length}` : "nothing yet"}
            action={
              <a href="/journal" className="text-primary hover:underline">
                Full journal →
              </a>
            }
          >
            {live.length === 0 ? (
              <Empty icon={Play}>Nothing has happened in this window yet.</Empty>
            ) : (
              <div className="flex flex-col">
                {live.map((e) => (
                  <FeedRow key={e.id} {...rowProps(e)} />
                ))}
              </div>
            )}
          </DashboardCard>

          <DashboardCard
            title="Recently failed"
            icon={AlertTriangle}
            hint={failed.length > 0 ? `${scoped.failed} in window` : "all clean"}
            action={
              scoped.failed > 0 ? (
                <button type="button" onClick={() => onScope("failed")} className="text-primary hover:underline">
                  Show all →
                </button>
              ) : undefined
            }
          >
            {failed.length === 0 ? (
              <Empty icon={Ban}>Nothing has failed. Nice.</Empty>
            ) : (
              <div className="flex flex-col">
                {failed.map((e) => (
                  <FeedRow key={e.id} {...rowProps(e)} />
                ))}
              </div>
            )}
          </DashboardCard>
        </div>
      </Appear>

      {/* ── Is today normal? ─────────────────────────────────────── */}
      <Appear order={4}>
        <DashboardCard
          title="Activity · 7 days"
          icon={CircleDot}
          hint={`${days.reduce((n, d) => n + d.total, 0)} events`}
        >
          <ChartContainer config={chartConfig} className="aspect-auto h-[170px] w-full">
            <BarChart data={days} margin={{ top: 8, right: 8, left: -22, bottom: 0 }}>
              <CartesianGrid vertical={false} strokeOpacity={0.08} />
              <XAxis dataKey="label" tickLine={false} axisLine={false} tickMargin={6} className="text-[10px]" />
              <YAxis allowDecimals={false} tickLine={false} axisLine={false} className="text-[10px]" />
              <ChartTooltip content={<ChartTooltipContent />} />
              {/* Errors stack on top of the rest rather than beside it, so
                  the column height stays "how much happened" and the red
                  band reads as a share of it. */}
              <Bar dataKey="total" stackId="a" fill="var(--color-total)" radius={[0, 0, 2, 2]} />
              <Bar dataKey="errors" stackId="a" fill="var(--color-errors)" radius={[2, 2, 0, 0]} />
            </BarChart>
          </ChartContainer>
        </DashboardCard>
      </Appear>

      <Appear order={5}>
        <p className="text-center font-mono text-[10px] uppercase tracking-widest text-muted-foreground-soft">
          {ACTIVITY_SCOPES.map((s) => `${s.label} ${scoped[s.key]}`).join("  ·  ")}
        </p>
      </Appear>
    </div>
  )
}
