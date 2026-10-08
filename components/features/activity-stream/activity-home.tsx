"use client"

// The Activity home (#2979) — the approved "Overview v3".
//
// It answers, in the order a person asks on arrival:
//
//   header        one sentence: runs · running · needs you · could not finish · cost
//   needs you     the decisions waiting, decidable here — the same items as Inbox
//   right now     what is live, and what the schedules start next
//   what ran      one lane per routine across the window; every run a bar to open
//   problems      failures grouped by cause (the server's fingerprint groups)
//   what changed  the issues those runs created and touched
//   outcomes      seven days by outcome
//   latest runs   one line per run — the journal's events stay in the Journal
//
// It is built from runs, not journal events. The page it replaces counted "139
// events" and listed "Pipeline … step notify completed"; nobody opens Activity
// to read that.

import * as React from "react"
import Link from "next/link"
import { toast } from "sonner"
import {
  AlertTriangle,
  BarChart3,
  CalendarClock,
  CircleDot,
  ExternalLink,
  GanttChartSquare,
  Inbox,
  ListChecks,
  Radio,
} from "lucide-react"

import { DashboardCard } from "@/components/features/dashboard/dashboard-card"
import { Button } from "@/components/ui/button"
import { Appear } from "@/components/ui/detail"
import { useAbilities } from "@/hooks/use-abilities"
import type { ChainSummary } from "@/hooks/use-chains"
import { useJournalSpend } from "@/hooks/use-journal-spend"
import { useWorkspaceWaitpoints } from "@/hooks/use-run-waitpoints"
import type { PendingWaitpoint } from "@/hooks/use-pending-approval"
import { useRealtimeEvent } from "@/hooks/use-realtime"
import {
  activeProblems,
  headlineParts,
  issueEffects,
  outcomesByDay,
  runLanes,
  runLine,
  upNext,
  type CalendarEvent,
  type FailureGroup,
  type HomeRun,
} from "@/lib/activity-home"
import { RUN_TONE_DOT, RUN_TONE_LABEL, runTone, triggerPhrase } from "@/lib/activity-run"
import { formatDurationMs } from "@/lib/activity-stream"
import { apiFetch } from "@/lib/api-fetch"
import { waitpointDecide } from "@/lib/api/waitpoints"
import { roleAtLeast } from "@/lib/routine-governance"
import { relTime } from "@/lib/time"
import { cn } from "@/lib/utils"

type Window = "24h" | "7d"
const WINDOW_MS: Record<Window, number> = { "24h": 24 * 3_600_000, "7d": 7 * 24 * 3_600_000 }

export interface ActivityHomeProps {
  workspaceId: string
  chains: ChainSummary[]
  onOpenRun: (runId: string) => void
  onOpenIssue: (issueId: string) => void
}

const HEAD_TONE = {
  default: "text-muted-foreground",
  primary: "text-primary",
  warn: "text-warn",
  destructive: "text-destructive",
} as const

export function ActivityHome({ workspaceId, chains, onOpenRun, onOpenIssue }: ActivityHomeProps) {
  const [win, setWin] = React.useState<Window>("24h")
  const [now, setNow] = React.useState(() => Date.now())
  React.useEffect(() => {
    const id = setInterval(() => setNow(Date.now()), 60_000)
    return () => clearInterval(id)
  }, [])

  const runs = useRecentRuns(workspaceId)
  const { waitpoints, refresh: refreshWaitpoints } = useWorkspaceWaitpoints(workspaceId)
  const spend = useJournalSpend(workspaceId, win)
  const failureGroups = useFailureGroups(workspaceId)
  const calendar = useUpcoming(workspaceId, now)
  const { role } = useAbilities()

  const from = now - WINDOW_MS[win]
  const inWindow = runs.rows.filter((r) => Date.parse(r.started_at) >= from)
  const active = runs.rows.filter((r) => ["running", "waiting"].includes(runTone(r.status)))
  const failedCount = inWindow.filter((r) => runTone(r.status) === "failed").length
  const cost = spend.data?.total_cost_usd ?? null
  const parts = headlineParts({
    runs: inWindow.length,
    running: active.filter((r) => runTone(r.status) === "running").length,
    waiting: waitpoints.length,
    failed: failedCount,
    cost,
  })
  const lanes = runLanes(inWindow, { from, to: now })
  const days = outcomesByDay(runs.rows, 7, now)
  const finished = days.reduce((n, d) => n + d.done, 0)
  const ended = days.reduce((n, d) => n + d.done + d.failed, 0)
  const effects = issueEffects(chains.filter((c) => Date.parse(c.last_activity) >= from))
  const latest = [...runs.rows].sort((a, b) => b.started_at.localeCompare(a.started_at)).slice(0, 6)
  const next = upNext(calendar, now, 3)
  const problems = activeProblems(failureGroups, runs.rows)

  return (
    <div className="mx-auto flex max-w-[1800px] flex-col gap-4 p-4 md:p-6">
      {/* ── Header ── */}
      <Appear order={0}>
        <div className="flex flex-wrap items-end gap-3">
          <div className="flex min-w-0 flex-col gap-1">
            <h1 className="text-lg font-semibold tracking-tight">{win === "24h" ? "Today" : "This week"}</h1>
            <p className="flex flex-wrap items-center gap-x-1.5 text-xs" aria-label="Summary">
              {parts.map((p, i) => (
                <React.Fragment key={p.text}>
                  {i > 0 && <span className="text-muted-foreground-soft">·</span>}
                  <span className={HEAD_TONE[p.tone]}>{p.text}</span>
                </React.Fragment>
              ))}
            </p>
          </div>
          <div className="flex-1" />
          <div role="group" aria-label="Window" className="flex overflow-hidden rounded-md border border-border font-mono text-[11px]">
            {(["24h", "7d"] as const).map((w) => (
              <button
                key={w}
                type="button"
                aria-pressed={win === w}
                onClick={() => setWin(w)}
                className={cn(
                  "px-2.5 py-1 transition-colors",
                  win === w ? "bg-foreground/[0.08] text-foreground" : "text-muted-foreground hover:text-foreground",
                )}
              >
                {w === "24h" ? "24 h" : "7 d"}
              </button>
            ))}
          </div>
        </div>
      </Appear>

      {/* ── Needs you · Right now ── */}
      <div className="grid grid-cols-1 gap-4 lg:grid-cols-[minmax(0,1.25fr)_minmax(0,1fr)]">
        <Appear order={1}>
          <NeedsYou
            workspaceId={workspaceId}
            waitpoints={waitpoints}
            runs={runs.rows}
            canDecide={roleAtLeast(role, "MANAGER")}
            onDecided={() => {
              void refreshWaitpoints()
              runs.refresh()
            }}
            onOpenRun={onOpenRun}
          />
        </Appear>
        <Appear order={2}>
          <DashboardCard
            role="region"
            aria-label="Right now"
            title="Right now"
            icon={Radio}
            hint={active.length > 0 ? `${active.length} live` : undefined}
          >
            <ul className="flex flex-col">
              {active.length === 0 && <li className="py-1 text-xs text-muted-foreground">Nothing is running.</li>}
              {active.slice(0, 4).map((r) => (
                <li key={r.id}>
                  <RunRow run={r} onOpen={onOpenRun} compact />
                </li>
              ))}
            </ul>
            <div className="mt-2 border-t border-hairline pt-2">
              <p className="eyebrow mb-1 flex items-center gap-1.5 text-muted-foreground">
                <CalendarClock className="h-3 w-3" /> Up next
              </p>
              {next.length === 0 ? (
                <p className="text-xs text-muted-foreground">No schedule fires in the next 24 hours.</p>
              ) : (
                <ul className="flex flex-col gap-0.5">
                  {next.map((e) => (
                    <li key={`${e.slug}:${e.at}`} className="flex items-center gap-2 text-xs">
                      <span className="min-w-0 flex-1 truncate text-foreground/85">{e.name || e.slug}</span>
                      <span className="font-mono text-[11px] text-muted-foreground" title={new Date(e.at).toLocaleString()}>
                        {untilWord(e.at, now)}
                      </span>
                    </li>
                  ))}
                </ul>
              )}
            </div>
          </DashboardCard>
        </Appear>
      </div>

      {/* ── What ran ── */}
      <Appear order={3}>
        <DashboardCard
          role="region"
          aria-label="What ran"
          title="What ran"
          icon={GanttChartSquare}
          hint={`${win === "24h" ? "last 24 h" : "last 7 days"} · one lane per routine · click a bar`}
        >
          {lanes.lanes.length === 0 ? (
            <p className="py-6 text-center text-xs text-muted-foreground">
              {runs.loading ? "Loading runs…" : "No routine ran in this window."}
            </p>
          ) : (
            <div className="flex flex-col gap-1.5">
              <AxisRow from={from} to={now} win={win} />
              {lanes.lanes.map((lane) => (
                <div key={lane.slug} className="grid grid-cols-[minmax(0,200px)_1fr_76px] items-center gap-3">
                  <span className="truncate text-xs" title={lane.name}>
                    {lane.name}
                  </span>
                  <span className="relative h-3.5 rounded bg-foreground/[0.04]">
                    {lane.bars.map((b) => (
                      <button
                        key={b.id}
                        type="button"
                        onClick={() => onOpenRun(b.id)}
                        aria-label={`${lane.name}: ${RUN_TONE_LABEL[b.tone]}, ${relTime(b.startedAt)}`}
                        title={`${RUN_TONE_LABEL[b.tone]} · ${new Date(b.startedAt).toLocaleString(undefined, { hour12: false })}${b.durationMs ? ` · ${formatDurationMs(b.durationMs)}` : ""}`}
                        className={cn(
                          "absolute inset-y-0.5 min-w-[4px] rounded-sm transition-[filter,transform] hover:brightness-125 hover:scale-y-125",
                          RUN_TONE_DOT[b.tone],
                        )}
                        style={{ left: `${b.left}%`, width: `${b.width}%` }}
                      />
                    ))}
                  </span>
                  <span
                    className={cn(
                      "text-right font-mono text-[11px]",
                      lane.summary.failed > 0 ? "text-destructive" : "text-muted-foreground",
                    )}
                  >
                    {lane.summary.failed > 0
                      ? `${lane.summary.failed} failed`
                      : `${lane.summary.runs} ${lane.summary.runs === 1 ? "run" : "runs"}`}
                  </span>
                </div>
              ))}
              {lanes.hidden > 0 && (
                <p className="pl-[212px] text-[11px] text-muted-foreground">+ {lanes.hidden} quieter routines</p>
              )}
              {runs.capped && (
                <p className="text-[10.5px] text-muted-foreground-soft">
                  Showing the newest {runs.rows.length} runs; older ones in this window are not drawn.
                </p>
              )}
            </div>
          )}
        </DashboardCard>
      </Appear>

      {/* ── Problems · What changed · Outcomes ── */}
      <div className="grid grid-cols-1 gap-4 lg:grid-cols-3">
        <Appear order={4}>
          <DashboardCard role="region" aria-label="Problems" title="Problems" icon={AlertTriangle} hint="7 d · grouped by cause" className="h-full">
            {problems.length === 0 ? (
              <p className="text-xs text-muted-foreground">Nothing failed in the last 7 days.</p>
            ) : (
              <ul className="flex flex-col gap-1">
                {problems.slice(0, 4).map((g) => (
                  <li key={g.fingerprint}>
                    <button
                      type="button"
                      onClick={() => onOpenRun(g.latestRunId)}
                      title={`${g.total} failures of this cause recorded in all`}
                      className="flex w-full items-start gap-2.5 rounded-md px-1.5 py-1.5 text-left transition-colors hover:bg-foreground/[0.04]"
                    >
                      <span className="w-7 shrink-0 font-mono text-xs text-destructive">{g.recent}×</span>
                      <span className="min-w-0 flex-1">
                        <span className="block truncate text-xs">{g.name}</span>
                        <span className="block truncate text-[11px] text-muted-foreground">
                          {g.step ? `at “${g.step}” · ` : ""}
                          {g.error}
                        </span>
                      </span>
                    </button>
                  </li>
                ))}
              </ul>
            )}
          </DashboardCard>
        </Appear>

        <Appear order={5}>
          <DashboardCard role="region" aria-label="What changed" title="What changed" icon={CircleDot} hint="issues" className="h-full">
            {effects.created.length + effects.touched.length === 0 ? (
              <p className="text-xs text-muted-foreground">No run in this window created or touched an issue.</p>
            ) : (
              <div className="flex flex-col gap-2.5">
                {[
                  ["created", effects.created],
                  ["touched", effects.touched],
                ].map(([word, list]) =>
                  (list as typeof effects.created).length === 0 ? null : (
                    <div key={word as string} className="flex flex-col gap-1">
                      <p className="text-xs">
                        <span className="font-mono">{(list as typeof effects.created).length}</span>{" "}
                        <span className="text-muted-foreground">
                          {(list as typeof effects.created).length === 1 ? "issue" : "issues"} {word as string}
                        </span>
                      </p>
                      <div className="flex flex-wrap gap-1">
                        {(list as typeof effects.created).slice(0, 6).map((i) => (
                          <button
                            key={i.id}
                            type="button"
                            onClick={() => onOpenIssue(i.id)}
                            title={i.title}
                            className="max-w-[180px] truncate rounded border border-border px-1.5 py-0.5 font-mono text-[10.5px] text-purple transition-colors hover:bg-foreground/[0.05]"
                          >
                            {i.identifier || i.title || i.id}
                          </button>
                        ))}
                      </div>
                    </div>
                  ),
                )}
              </div>
            )}
          </DashboardCard>
        </Appear>

        <Appear order={6}>
          <DashboardCard
            role="region"
            aria-label="Outcomes"
            title="Outcomes"
            icon={BarChart3}
            hint={ended > 0 ? `7 d · ${Math.round((finished / ended) * 100)} % finished` : "7 d"}
            className="h-full"
          >
            <OutcomeBars days={days} />
          </DashboardCard>
        </Appear>
      </div>

      {/* ── Latest runs ── */}
      <Appear order={7}>
        <DashboardCard
          role="region"
          aria-label="Latest runs"
          title="Latest runs"
          icon={ListChecks}
          action={
            roleAtLeast(role, "MANAGER") ? (
              <Link href="/journal" className="inline-flex items-center gap-1 hover:text-foreground">
                Raw events in Journal <ExternalLink className="h-3 w-3" />
              </Link>
            ) : undefined
          }
        >
          {latest.length === 0 ? (
            <p className="text-xs text-muted-foreground">{runs.loading ? "Loading runs…" : "No routine has run yet."}</p>
          ) : (
            <ul className="flex flex-col">
              {latest.map((r) => (
                <li key={r.id}>
                  <RunRow run={r} onOpen={onOpenRun} />
                </li>
              ))}
            </ul>
          )}
        </DashboardCard>
      </Appear>
    </div>
  )
}

/* ------------------------------------------------------------------ *
 *  Pieces
 * ------------------------------------------------------------------ */

function RunRow({ run, onOpen, compact }: { run: HomeRun; onOpen: (id: string) => void; compact?: boolean }) {
  const tone = runTone(run.status)
  return (
    <button
      type="button"
      onClick={() => onOpen(run.id)}
      className="group flex w-full items-center gap-2.5 rounded-md px-1.5 py-1.5 text-left transition-colors hover:bg-foreground/[0.04]"
    >
      <span className="relative inline-flex h-2 w-2 shrink-0">
        {tone === "running" && <span className={cn("absolute inset-0 animate-ping rounded-full opacity-60", RUN_TONE_DOT[tone])} />}
        <span className={cn("relative h-2 w-2 rounded-full", RUN_TONE_DOT[tone])} />
      </span>
      <span className={cn("shrink-0 truncate text-xs", compact ? "min-w-0 flex-1" : "w-[220px]")}>
        {run.pipeline_name || run.pipeline_slug}
      </span>
      {!compact && (
        <span className={cn("min-w-0 flex-1 truncate text-xs", tone === "failed" ? "text-destructive/90" : "text-muted-foreground")}>
          {runLine(run)}
        </span>
      )}
      {!compact && run.triggered_via && (
        <span className="hidden shrink-0 rounded border border-border px-1.5 py-px font-mono text-[10px] text-muted-foreground md:inline">
          {triggerPhrase(run)}
        </span>
      )}
      {compact && <span className="shrink-0 text-[11px] text-muted-foreground">{runLine(run)}</span>}
      <span className="w-14 shrink-0 text-right font-mono text-[10.5px] text-muted-foreground">{relTime(run.started_at)}</span>
    </button>
  )
}

function NeedsYou({
  workspaceId,
  waitpoints,
  runs,
  canDecide,
  onDecided,
  onOpenRun,
}: {
  workspaceId: string
  waitpoints: PendingWaitpoint[]
  runs: HomeRun[]
  canDecide: boolean
  onDecided: () => void
  onOpenRun: (id: string) => void
}) {
  const [busy, setBusy] = React.useState<string | null>(null)
  const nameOf = (runId: string) => {
    const r = runs.find((x) => x.id === runId)
    return r?.pipeline_name || r?.pipeline_slug
  }
  async function decide(w: PendingWaitpoint, approved: boolean) {
    setBusy(w.token)
    const res = await waitpointDecide(workspaceId, w.token, approved)
    setBusy(null)
    if (res.ok) {
      toast.success(approved ? "Approved — the run continues" : "Rejected — the run stops here")
      onDecided()
    } else {
      toast.error(`Could not record the decision: ${res.error}`)
    }
  }
  const waiting = waitpoints.length > 0
  return (
    <DashboardCard
      role="region"
      aria-label="Needs you"
      title="Needs you"
      icon={Inbox}
      hint={waiting ? `${waitpoints.length} · same items as Inbox` : undefined}
      action={
        <Link href="/inbox" className="hover:text-foreground">
          Open Inbox →
        </Link>
      }
      className={cn("h-full", waiting && "border-warn/40 bg-warn/[0.04]")}
    >
      {!waiting ? (
        <p className="text-xs text-muted-foreground">Nothing is waiting for you.</p>
      ) : (
        <ul className="flex flex-col gap-3">
          {waitpoints.slice(0, 3).map((w) => {
            // A decision form asks for more than yes or no; that belongs on
            // the run, where the form is.
            const simple = !w.decision_form
            return (
              <li key={w.token} className="flex flex-col gap-2">
                <div className="flex flex-col gap-0.5">
                  <span className="line-clamp-2 text-sm font-medium">{firstLine(w.prompt) || "A step is waiting for a decision"}</span>
                  <span className="text-[11px] text-muted-foreground">
                    {nameOf(w.pipeline_run_id) ?? "Routine run"} · waiting {relTime(w.created_at)}
                  </span>
                </div>
                <div className="flex flex-wrap gap-2">
                  {canDecide && simple && (
                    <>
                      <Button size="sm" disabled={busy !== null} onClick={() => void decide(w, true)}>
                        Approve
                      </Button>
                      <Button size="sm" variant="outline" disabled={busy !== null} onClick={() => void decide(w, false)}>
                        Reject
                      </Button>
                    </>
                  )}
                  <Button size="sm" variant="ghost" onClick={() => onOpenRun(w.pipeline_run_id)}>
                    Open run
                  </Button>
                </div>
              </li>
            )
          })}
        </ul>
      )}
    </DashboardCard>
  )
}

function AxisRow({ from, to, win }: { from: number; to: number; win: Window }) {
  const ticks = 6
  const labels = Array.from({ length: ticks + 1 }, (_, i) => {
    const t = new Date(from + ((to - from) * i) / ticks)
    if (i === ticks) return "now"
    return win === "24h"
      ? t.toLocaleTimeString(undefined, { hour: "2-digit", minute: "2-digit", hour12: false })
      : t.toLocaleDateString(undefined, { weekday: "short" })
  })
  return (
    <div className="grid grid-cols-[minmax(0,200px)_1fr_76px] gap-3 font-mono text-[10px] text-muted-foreground-soft">
      <span />
      <span className="flex justify-between">
        {labels.map((l, i) => (
          <span key={i}>{l}</span>
        ))}
      </span>
      <span />
    </div>
  )
}

function OutcomeBars({ days }: { days: ReturnType<typeof outcomesByDay> }) {
  const max = Math.max(1, ...days.map((d) => d.done + d.failed + d.other))
  return (
    <div className="flex h-[120px] items-end gap-2">
      {days.map((d) => {
        const total = d.done + d.failed + d.other
        return (
          <div key={d.key} className="flex flex-1 flex-col items-center gap-1" title={`${d.label}: ${d.done} completed, ${d.failed} could not finish${d.other ? `, ${d.other} other` : ""}`}>
            <div className="flex h-[90px] w-full flex-col justify-end gap-px">
              {total === 0 ? (
                <div className="h-px w-full bg-foreground/[0.08]" />
              ) : (
                <>
                  {d.failed > 0 && <div className="rounded-sm bg-destructive" style={{ height: `${(d.failed / max) * 100}%` }} />}
                  {d.other > 0 && <div className="rounded-sm bg-warn" style={{ height: `${(d.other / max) * 100}%` }} />}
                  {d.done > 0 && <div className="rounded-sm bg-success" style={{ height: `${(d.done / max) * 100}%` }} />}
                </>
              )}
            </div>
            <span className={cn("font-mono text-[9.5px]", d.today ? "text-foreground" : "text-muted-foreground-soft")}>{d.label}</span>
          </div>
        )
      })}
    </div>
  )
}

function firstLine(s: string | undefined): string {
  return (s ?? "").split(/\r?\n/).map((l) => l.replace(/^#+\s*/, "").trim()).find(Boolean) ?? ""
}

function untilWord(iso: string, now: number): string {
  const ms = Date.parse(iso) - now
  if (ms < 60_000) return "now"
  if (ms < 3_600_000) return `in ${Math.round(ms / 60_000)} min`
  const d = new Date(iso)
  return d.toLocaleTimeString(undefined, { hour: "2-digit", minute: "2-digit", hour12: false })
}

/* ------------------------------------------------------------------ *
 *  Data
 * ------------------------------------------------------------------ */

const RUN_CAP = 200

/** The workspace's routine runs of the last seven days, newest first, live. */
function useRecentRuns(workspaceId: string) {
  const [rows, setRows] = React.useState<HomeRun[]>([])
  const [loading, setLoading] = React.useState(true)
  const [tick, setTick] = React.useState(0)
  const refresh = React.useCallback(() => setTick((t) => t + 1), [])
  React.useEffect(() => {
    const ctrl = new AbortController()
    const since = new Date(Date.now() - WINDOW_MS["7d"]).toISOString()
    apiFetch(
      `/api/v1/workspaces/${encodeURIComponent(workspaceId)}/pipeline-runs?since=${encodeURIComponent(since)}&limit=${RUN_CAP}`,
      { signal: ctrl.signal },
    )
      .then(async (r) => (r.ok ? ((await r.json()) as { rows?: HomeRun[] }) : null))
      .then((b) => {
        if (!ctrl.signal.aborted && b) setRows(b.rows ?? [])
      })
      .catch(() => {})
      .finally(() => {
        if (!ctrl.signal.aborted) setLoading(false)
      })
    return () => ctrl.abort()
  }, [workspaceId, tick])
  useRealtimeEvent("pipeline.run.started", refresh)
  useRealtimeEvent("pipeline.run.completed", refresh)
  useRealtimeEvent("pipeline.run.failed", refresh)
  return { rows, loading, refresh, capped: rows.length >= RUN_CAP }
}


/** Failures grouped by the server's fingerprint — same step, same error. */
function useFailureGroups(workspaceId: string): FailureGroup[] {
  const [groups, setGroups] = React.useState<FailureGroup[]>([])
  React.useEffect(() => {
    const ctrl = new AbortController()
    apiFetch(`/api/v1/workspaces/${encodeURIComponent(workspaceId)}/pipelines/runs/errors?limit=5`, { signal: ctrl.signal })
      .then(async (r) => (r.ok ? ((await r.json()) as { groups?: FailureGroup[] }) : null))
      .then((b) => {
        if (!ctrl.signal.aborted && b) setGroups(b.groups ?? [])
      })
      .catch(() => {})
    return () => ctrl.abort()
  }, [workspaceId])
  return groups
}

/** Planned runs in the next 24 hours, from the routines calendar. */
function useUpcoming(workspaceId: string, now: number): CalendarEvent[] {
  const [events, setEvents] = React.useState<CalendarEvent[]>([])
  const hour = Math.floor(now / 3_600_000)
  React.useEffect(() => {
    const ctrl = new AbortController()
    const from = new Date(hour * 3_600_000).toISOString().replace(/\.\d{3}Z$/, "Z")
    const to = new Date((hour + 25) * 3_600_000).toISOString().replace(/\.\d{3}Z$/, "Z")
    apiFetch(
      `/api/v1/workspaces/${encodeURIComponent(workspaceId)}/pipelines/calendar?${new URLSearchParams({ from, to })}`,
      { signal: ctrl.signal },
    )
      .then(async (r) => (r.ok ? ((await r.json()) as { events?: CalendarEvent[] }) : null))
      .then((b) => {
        if (!ctrl.signal.aborted && b) setEvents(b.events ?? [])
      })
      .catch(() => {})
    return () => ctrl.abort()
  }, [workspaceId, hour])
  return events
}
