"use client"

// One lens, one column.
//
// The rail grew four lenses — Workflows, Issues, Agents, Routines — and the
// main column did not move. All four rendered the same Overview: the same four
// KPIs, the same "what is broken", the same "latest activity". A tab whose
// press leaves the screen unchanged is a tab nobody presses twice, and it is
// the same defect as the graph that stayed pointed at the previous chain while
// the heading named a new one: a control that changes what is SELECTED without
// changing what is SHOWN.
//
// So each lens owns the column. Every dashboard here answers the question its
// own rail row is a row of, and nothing else:
//
//   Issues    what was touched, what was created, and by which routine and agent
//   Agents    who worked, how much, how long, and what it cost
//   Routines  what ran, how often, and how reliably
//
// Workflows keeps ActivityOverview — it was always that lens's dashboard, and
// it is the one that was right.
//
// All three read the SAME ChainSummary[] the rail reads, so no dashboard can
// disagree with the list beside it about what happened. The shaping is in
// lib/activity-lenses; this file is layout.

import * as React from "react"
import { Bot, CircleDot, Clock, ScrollText, type LucideIcon } from "lucide-react"

import { StatusIcon } from "@/components/features/issues/status-icon"
import { PriorityIcon } from "@/components/features/issues/priority-icon"
import { ALL_PRIORITIES } from "@/components/features/issues/issue-constants"
import type { IssuePriority } from "@/lib/types/mission"

import { DashboardCard } from "@/components/features/dashboard/dashboard-card"
import { KpiCard } from "@/components/features/dashboard/kpi-card"
import { Appear } from "@/components/ui/detail"
import { AgentAvatar } from "@/components/ui/agent-avatar"
import { CrewIcon } from "@/components/ui/crew-icon"
import { resolveRoutineIcon, resolveRoutineColor } from "@/lib/routine-identity"
import { formatDurationMs } from "@/lib/activity-stream"
import {
  agentLens,
  chainStatus,
  issueLens,
  routineLens,
} from "@/lib/activity-lenses"
import { firingNext, type FiringSchedule } from "@/lib/firing-next"
import type { ChainSummary } from "@/hooks/use-chains"
import type { SidebarRoutine } from "./activity-sidebar"
import { cn } from "@/lib/utils"

/* ------------------------------------------------------------------ *
 *  Shared pieces
 * ------------------------------------------------------------------ */

function Empty({ icon: Icon, children }: { icon: LucideIcon; children: React.ReactNode }) {
  return (
    <div className="flex flex-col items-center justify-center gap-1.5 py-8 text-center">
      <Icon className="h-4 w-4 text-muted-foreground-soft" />
      <p className="max-w-[320px] text-[11px] leading-relaxed text-muted-foreground-soft">{children}</p>
    </div>
  )
}

function Head({ title, caption }: { title: string; caption: string }) {
  return (
    <Appear order={0}>
      <div>
        <h1 className="text-lg font-semibold tracking-tight">{title}</h1>
        <p className="text-xs text-muted-foreground">{caption}</p>
      </div>
    </Appear>
  )
}

function Shell({ children }: { children: React.ReactNode }) {
  return <div className="mx-auto flex max-w-[1800px] flex-col gap-4 p-4 md:p-6">{children}</div>
}

/**
 * A proportion as a bar.
 *
 * `of` may be 0 — a routine listed with no completed runs yet — and dividing
 * would give NaN, which renders as an empty bar indistinguishable from 0%.
 */
function Bar({ value, of, token }: { value: number; of: number; token: string }) {
  const pct = of > 0 ? Math.round((value / of) * 100) : 0
  return (
    <span className="h-1.5 w-14 shrink-0 overflow-hidden rounded-full bg-white/[0.08]">
      <span className="block h-full rounded-full" style={{ width: `${pct}%`, background: `var(${token})` }} />
    </span>
  )
}

/** One clickable line. Rows across the three dashboards share this grammar. */
function Line({
  onClick,
  children,
}: {
  onClick?: () => void
  children: React.ReactNode
}) {
  const Root = onClick ? "button" : "div"
  return (
    <Root
      {...(onClick ? { type: "button" as const, onClick } : {})}
      className={cn(
        "flex w-full items-center gap-2.5 rounded-md px-1.5 py-1.5 text-left text-[11.5px]",
        onClick && "transition-colors hover:bg-white/[0.03]",
      )}
    >
      {children}
    </Root>
  )
}

/** A priority the icon can draw, or undefined for anything else. */
function priorityOf(raw: string | null | undefined): IssuePriority | undefined {
  return raw != null && (ALL_PRIORITIES as string[]).includes(raw) ? (raw as IssuePriority) : undefined
}

function Dot({ token }: { token: string }) {
  return <span aria-hidden className="h-1.5 w-1.5 shrink-0 rounded-full" style={{ background: `var(${token})` }} />
}

/* ------------------------------------------------------------------ *
 *  Issues
 * ------------------------------------------------------------------ */

export interface LensOverviewProps {
  chains: ChainSummary[]
  rangeLabel: string
  onOpenEntity: (kind: string, id: string, label: string) => void
  /** Open a workflow by its origin — the "which routine did this" link. */
  onOpenWorkflow: (origin: string) => void
}

export interface IssuesOverviewProps extends LensOverviewProps {
  /**
   * The workspace's issues, by id, for status and priority.
   *
   * ChainIssueRef carries id, identifier, title and `created` and nothing else —
   * enough to name an issue, not enough to DRAW one. Without this the rows wore
   * a generic dot while the Issues page gave the same issue a status glyph and a
   * priority mark, which is the same complaint as a workflow not wearing its
   * routine's face, one object over.
   *
   * A miss is normal, not an error: the issue list is capped at 200 and a chain
   * can touch one outside it. The row then falls back to the dot rather than
   * inventing a status.
   */
  issueMeta: Map<string, { status: string; priority?: string | null }>
}

/**
 * What happened to issues in this window.
 *
 * The load-bearing line is "↳ which routine, which agent". That sentence is the
 * one a general-purpose tracker cannot write, because in a tracker the actor is
 * always a person and there is nothing to put in either slot. Here it is the
 * whole reason the issue is in this product rather than linked from it.
 */
export function IssuesOverview({
  chains,
  rangeLabel,
  issueMeta,
  onOpenEntity,
  onOpenWorkflow,
}: IssuesOverviewProps) {
  const rows = React.useMemo(() => issueLens(chains), [chains])
  const created = rows.filter((r) => r.created).length
  const moved = rows.length - created
  const agentsInvolved = React.useMemo(
    () =>
      new Set(
        chains.filter((c) => (c.issue_count ?? 0) > 0).flatMap((c) => (c.agents ?? []).map((a) => a.id)),
      ).size,
    [chains],
  )

  // Which chain touched each issue, so the row can name the routine and the
  // agent. Built once over the page rather than searched per row.
  const touchedBy = React.useMemo(() => {
    const m = new Map<string, ChainSummary[]>()
    for (const c of chains) {
      for (const i of c.issues ?? []) {
        const list = m.get(i.id)
        if (list) list.push(c)
        else m.set(i.id, [c])
      }
    }
    return m
  }, [chains])

  // Distinct chains that reached ANY issue — the denominator the "Touched" KPI
  // actually means. Counted off `touchedBy` rather than filtered again so the
  // number and the rows under it cannot drift apart.
  const touchingCount = React.useMemo(
    () => new Set([...touchedBy.values()].flat().map((c) => c.origin)).size,
    [touchedBy],
  )

  return (
    <Shell>
      <Head
        title="Issues"
        caption={`What was touched, and by what · ${rangeLabel.toLowerCase()}`}
      />
      <Appear order={1}>
        <div className="grid grid-cols-2 gap-4 lg:grid-cols-4">
          {/* The denominator is the workflows that REACHED an issue, not every
              workflow in the window. `chains.length` read "12 issues touched by
              40 workflows" on a window where five of the forty went near an
              issue — a subtitle on a KPI is read as its explanation, so a number
              that answers a different question is a wrong one. */}
          <KpiCard
            label="Touched"
            value={rows.length}
            subtitle={`by ${touchingCount} ${touchingCount === 1 ? "workflow" : "workflows"}`}
          />
          <KpiCard
            label="Created by agents"
            value={created}
            subtitle={created === 0 ? "none authored here" : "issues that exist because of this work"}
            valueColor={created > 0 ? "var(--success)" : undefined}
          />
          <KpiCard
            label="Changed"
            value={moved}
            subtitle={moved === 0 ? "none moved" : "existed before, moved here"}
          />
          {/* This slot held a hardcoded 0 labelled "Waiting on you", with a
              subtitle explaining that approvals cannot be linked to an issue.
              The explanation was true and the number was still a measurement
              nobody took — a KPI reads as measured whatever sits under it. The
              slot now carries something the data can actually answer. */}
          <KpiCard
            label="Agents involved"
            value={agentsInvolved}
            subtitle={agentsInvolved === 0 ? "no agent worked an issue here" : "worked on these issues"}
          />
        </div>
      </Appear>

      <Appear order={2}>
        <DashboardCard title="What happened to each" icon={CircleDot} hint={`${rows.length}`}>
          {rows.length === 0 ? (
            <Empty icon={CircleDot}>
              Nothing touched an issue in this window. Issues with no activity live in Issues, not here.
            </Empty>
          ) : (
            <div className="flex flex-col">
              {rows.map((r) => {
                const via = touchedBy.get(r.id) ?? []
                const agents = [...new Set(via.flatMap((c) => (c.agents ?? []).map((a) => a.name || a.slug || a.id)))]
                const meta = issueMeta.get(r.id)
                return (
                  <div key={r.id} className="flex flex-col">
                    <Line onClick={() => onOpenEntity("issue", r.id, r.identifier || r.title || r.id)}>
                      {/* The issue's own face, from the same two components the
                          Issues page draws it with. Falls back to a dot only
                          when the issue is outside the loaded list. */}
                      {meta ? (
                        <>
                          <StatusIcon status={meta.status} className="h-3.5 w-3.5 shrink-0" />
                          {/* The wire type is a bare string; PriorityIcon takes
                              the enum. Narrowed against the real list rather
                              than cast, so a value the server grows later
                              renders as absent instead of as whatever the
                              icon's default branch happens to be. */}
                          {priorityOf(meta.priority) && (
                            <PriorityIcon priority={priorityOf(meta.priority)!} className="h-3 w-3 shrink-0" />
                          )}
                        </>
                      ) : (
                        <Dot token={r.created ? "--success" : "--muted-foreground"} />
                      )}
                      {r.identifier && (
                        <span className="shrink-0 font-mono text-[10.5px] text-foreground/60">{r.identifier}</span>
                      )}
                      <span className="min-w-0 flex-1 truncate">{r.title || r.id}</span>
                      <span className="shrink-0 font-mono text-[10px] text-muted-foreground-soft">
                        {r.created ? "created" : "changed"}
                      </span>
                    </Line>
                    {/* The sentence the whole feature is for. Indented because
                        it is evidence for the row above, not a row of its own. */}
                    {via.length > 0 && (
                      <button
                        type="button"
                        onClick={() => onOpenWorkflow(via[0].origin)}
                        className="flex w-full items-center gap-1.5 rounded-md px-1.5 py-1 pl-7 text-left text-[10.5px] text-muted-foreground-soft transition-colors hover:bg-white/[0.03] hover:text-muted-foreground"
                      >
                        <span className="truncate">
                          ↳ {via.map((c) => c.routine_slug || c.started_by).join(", ")}
                          {agents.length > 0 && ` → ${agents.join(", ")}`}
                        </span>
                        {via[0].duration_ms != null && (
                          <span className="ml-auto shrink-0 font-mono">{formatDurationMs(via[0].duration_ms)}</span>
                        )}
                      </button>
                    )}
                  </div>
                )
              })}
            </div>
          )}
        </DashboardCard>
      </Appear>
    </Shell>
  )
}

/* ------------------------------------------------------------------ *
 *  Agents
 * ------------------------------------------------------------------ */

export interface AgentsOverviewProps extends LensOverviewProps {
  /** How many agents the workspace has hired, for the "1 of 12" denominator. */
  hiredCount: number
}

/**
 * What the agents did.
 *
 * The denominator is deliberate: "1" on its own reads as the whole workspace,
 * and on a fleet of 12 that is the difference between "quiet day" and "eleven
 * agents are idle". This is the lens the trace layer exists for, and it is also
 * the one most limited by where the chain index looks — see the note on the
 * empty state.
 */
export function AgentsOverview({
  chains,
  rangeLabel,
  hiredCount,
  onOpenEntity,
  onOpenWorkflow,
}: AgentsOverviewProps) {
  const rows = React.useMemo(() => agentLens(chains), [chains])
  const assignments = rows.reduce((n, a) => n + a.assignments, 0)

  // Which chains each agent worked in, so a row can say what it was doing
  // rather than only how often.
  const workIn = React.useMemo(() => {
    const m = new Map<string, ChainSummary[]>()
    for (const c of chains) {
      for (const a of c.agents ?? []) {
        const list = m.get(a.id)
        if (list) list.push(c)
        else m.set(a.id, [c])
      }
    }
    return m
  }, [chains])

  // Wall clock across the chains agents worked in. NOT a sum of assignment
  // durations, which the index does not carry — and named "in workflows" on the
  // card so it cannot be read as billed agent time.
  const spanMs = React.useMemo(
    () =>
      [...new Set([...workIn.values()].flat())].reduce((n, c) => n + (c.duration_ms ?? 0), 0),
    [workIn],
  )

  return (
    <Shell>
      <Head title="Agents" caption={`Who worked, and on what · ${rangeLabel.toLowerCase()}`} />
      <Appear order={1}>
        <div className="grid grid-cols-2 gap-4 lg:grid-cols-4">
          <KpiCard
            label="Agents at work"
            value={rows.length}
            subtitle={hiredCount > 0 ? `of ${hiredCount} hired` : "none hired yet"}
            valueColor={rows.length > 0 ? "var(--primary)" : undefined}
          />
          <KpiCard
            label="Assignments"
            value={assignments}
            subtitle={assignments === 0 ? "no work dispatched" : "pieces of work taken"}
          />
          <KpiCard
            label="In workflows"
            value={spanMs > 0 ? formatDurationMs(spanMs) : "—"}
            subtitle="wall clock of the chains they worked in"
          />
          <KpiCard
            label="Busiest"
            value={rows[0]?.name || rows[0]?.slug || "—"}
            subtitle={rows[0] ? `${rows[0].assignments} assignments` : "nobody worked in this window"}
          />
        </div>
      </Appear>

      <Appear order={2}>
        <DashboardCard title="Who did what" icon={Bot} hint={`${rows.length}`}>
          {rows.length === 0 ? (
            <Empty icon={Bot}>
              No agent took work inside a workflow in this window. Work an agent did that no routine
              dispatched is not indexed yet — it has no chain to belong to.
            </Empty>
          ) : (
            <div className="flex flex-col">
              {rows.map((a) => {
                const chainsFor = workIn.get(a.id) ?? []
                const busiest = rows[0]?.assignments || 1
                return (
                  <div key={a.id} className="flex flex-col">
                    <Line onClick={() => onOpenEntity("agent", a.id, a.name || a.slug || a.id)}>
                      <AgentAvatar seed={a.id} alt="" className="h-4 w-4 shrink-0 rounded-full" />
                      <span className="min-w-0 flex-1 truncate font-medium">{a.name || a.slug || a.id}</span>
                      <Bar value={a.assignments} of={busiest} token="--primary" />
                      <span className="w-16 shrink-0 text-right font-mono text-[10px] tabular-nums text-muted-foreground">
                        ×{a.assignments}
                      </span>
                    </Line>
                    {chainsFor.length > 0 && (
                      <button
                        type="button"
                        onClick={() => onOpenWorkflow(chainsFor[0].origin)}
                        className="flex w-full items-center gap-1.5 rounded-md px-1.5 py-1 pl-7 text-left text-[10.5px] text-muted-foreground-soft transition-colors hover:bg-white/[0.03] hover:text-muted-foreground"
                      >
                        <span className="truncate">
                          ↳ {chainsFor.map((c) => c.routine_slug || c.started_by).join(", ")}
                          {chainsFor.some((c) => (c.issue_count ?? 0) > 0) &&
                            ` · touched ${[
                              ...new Set(
                                chainsFor.flatMap((c) => (c.issues ?? []).map((i) => i.identifier || i.id)),
                              ),
                            ].join(", ")}`}
                        </span>
                      </button>
                    )}
                  </div>
                )
              })}
            </div>
          )}
        </DashboardCard>
      </Appear>
    </Shell>
  )
}

/* ------------------------------------------------------------------ *
 *  Routines
 * ------------------------------------------------------------------ */

export interface RoutinesLensOverviewProps {
  chains: ChainSummary[]
  routines: SidebarRoutine[]
  rangeLabel: string
  /** Catalogue size, for "7 of 40" — the number that stops 7 reading as all. */
  catalogueCount: number
  /** Cron schedules in the workspace, for what is about to run. */
  schedules: FiringSchedule[]
  onOpenRoutine: (slug: string, label: string) => void
}

/**
 * Which routines ran, and how reliably.
 *
 * The bar is SUCCESS RATE, not duration. On a routine with thirty runs the
 * durations are thirty numbers nobody reads as one; "did it work" is the single
 * number that summarises the set, and it is what sends a reader into the list.
 */
export function RoutinesLensOverview({
  chains,
  routines,
  rangeLabel,
  catalogueCount,
  schedules,
  onOpenRoutine,
}: RoutinesLensOverviewProps) {
  const rows = React.useMemo(() => routineLens(chains), [chains])
  const byslug = React.useMemo(() => new Map(routines.map((r) => [r.slug, r])), [routines])

  // `now` read once per render, not per row: two countdowns computed a
  // millisecond apart can round to different minutes, and a list where one row
  // says 45m and the next says 44m for the same instant reads as a bug.
  const upcoming = React.useMemo(() => firingNext(schedules, Date.now()), [schedules])

  // Both halves of the ratio come from the SAME population, and that is the
  // whole point of computing them together here.
  //
  // The numerator used to be summed over every chain in the window while the
  // denominator came from routineLens, which SKIPS a chain with no
  // `routine_slug` — a chain whose root run retention swept. So that chain's
  // failures were subtracted from a total its runs were never counted in: four
  // clean routine runs beside one orphaned chain with two failures rendered
  // "Success 50% · 2 of 4 runs", a red number over a routine that had not
  // failed once. A ratio whose two sides count different things is not a
  // narrowed measurement, it is arithmetic on unrelated numbers.
  const routineChains = React.useMemo(
    () => chains.filter((c) => (c.routine_slug ?? "").trim() !== ""),
    [chains],
  )
  const totalRuns = rows.reduce((n, r) => n + r.runs, 0)
  const failedRuns = routineChains.reduce((n, c) => n + (c.failed_runs ?? 0), 0)
  const okRuns = Math.max(0, totalRuns - failedRuns)

  // "Workflows affected" counts the same population for the same reason: it
  // sits under a routine count, so a chain no routine ran cannot be one of the
  // workflows those routines affected.
  const failedChains = routineChains.filter((c) => chainStatus(c) === "failed").length
  const failingRoutines = rows.filter((r) => r.failed).length

  return (
    <Shell>
      <Head title="Routines" caption={`What ran, and how it went · ${rangeLabel.toLowerCase()}`} />
      <Appear order={1}>
        <div className="grid grid-cols-2 gap-4 lg:grid-cols-4">
          <KpiCard
            label="Routines that ran"
            value={rows.length}
            subtitle={catalogueCount > 0 ? `of ${catalogueCount} in the catalogue` : "none defined"}
          />
          <KpiCard
            label="Runs"
            value={totalRuns}
            subtitle={
              rows.length > 0 && rows[0].runs > 1
                ? `${rows[0].runs} from ${byslug.get(rows[0].slug)?.name || rows[0].slug}`
                : "across every routine"
            }
          />
          <KpiCard
            label="Success"
            value={totalRuns > 0 ? `${Math.round((okRuns / totalRuns) * 100)}%` : "—"}
            subtitle={totalRuns > 0 ? `${okRuns} of ${totalRuns} runs` : "nothing ran"}
            valueColor={failedRuns > 0 ? "var(--warn)" : totalRuns > 0 ? "var(--success)" : undefined}
          />
          <KpiCard
            label="Failing"
            value={failingRoutines}
            subtitle={`${failedChains} ${failedChains === 1 ? "workflow" : "workflows"} affected`}
            valueColor={failingRoutines > 0 ? "var(--destructive)" : undefined}
          />
        </div>
      </Appear>

      <Appear order={2}>
        <DashboardCard
          title="Which routines ran"
          icon={ScrollText}
          hint={rows.length > 0 ? "open one for its runs" : undefined}
        >
          {rows.length === 0 ? (
            <Empty icon={ScrollText}>
              No routine ran in this window. The catalogue of every routine — including the ones that
              have never run — is on the Routines page.
            </Empty>
          ) : (
            <div className="flex flex-col">
              {rows.map((r) => {
                const known = byslug.get(r.slug)
                const name = known?.name || r.slug
                // Per-routine failed runs, summed over its chains, so the bar
                // and the count come from the same place the rail's dot does.
                const mine = chains.filter((c) => c.routine_slug === r.slug)
                const failed = mine.reduce((n, c) => n + (c.failed_runs ?? 0), 0)
                const ok = Math.max(0, r.runs - failed)
                return (
                  <Line key={r.slug} onClick={() => onOpenRoutine(r.slug, name)}>
                    <CrewIcon
                      icon={resolveRoutineIcon(known ?? { slug: r.slug })}
                      color={resolveRoutineColor(known ?? { slug: r.slug })}
                      size="sm"
                      className="!h-4 !w-4 !rounded shrink-0"
                    />
                    <span className="min-w-0 flex-1 truncate">{name}</span>
                    <Bar value={ok} of={r.runs} token={failed > 0 ? "--warn" : "--success"} />
                    <span className="w-24 shrink-0 text-right font-mono text-[10px] tabular-nums text-muted-foreground">
                      {r.runs} {r.runs === 1 ? "run" : "runs"}
                      {failed > 0 && <span className="text-destructive"> · {failed} failed</span>}
                    </span>
                  </Line>
                )
              })}
            </div>
          )}
        </DashboardCard>
      </Appear>

      {/* The other half of a schedule-driven workspace. "What ran" is above;
          this is what is about to, which on a routine firing every minute is
          the difference between reading a page and watching one.

          Four rows and a countdown, nothing else: cron expression, timezone,
          catch-up policy and wake gate all belong on the routine's Triggers
          card where they can be CHANGED. Here it is a glance. */}
      <Appear order={3}>
        <DashboardCard
          title="Firing next"
          icon={Clock}
          hint={upcoming.length > 0 ? `${upcoming.length} scheduled` : undefined}
        >
          {upcoming.length === 0 ? (
            <Empty icon={Clock}>
              {schedules.length > 0
                ? "Every schedule in this workspace is disabled or has no next fire computed."
                : "No schedule is due. Open a routine and add one under Triggers."}
            </Empty>
          ) : (
            <div className="flex flex-col">
              {upcoming.map((s) => (
                <Line
                  key={s.id}
                  onClick={s.slug ? () => onOpenRoutine(s.slug!, byslug.get(s.slug!)?.name || s.slug!) : undefined}
                >
                  <Dot token="--muted-foreground" />
                  {/* The routine first: the reader is looking for what is about
                      to run, not for what somebody called the cron. */}
                  <span className="min-w-0 flex-1 truncate">
                    {s.slug ? byslug.get(s.slug)?.name || s.slug : s.name}
                  </span>
                  <span className="shrink-0 font-mono text-[10px] text-muted-foreground-soft">{s.cron}</span>
                  <span className="w-14 shrink-0 text-right font-mono text-[10.5px] tabular-nums text-muted-foreground">
                    {s.dueIn}
                  </span>
                </Line>
              ))}
            </div>
          )}
        </DashboardCard>
      </Appear>
    </Shell>
  )
}
