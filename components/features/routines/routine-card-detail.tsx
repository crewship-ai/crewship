"use client"

// The routine detail, as one scrolling surface of cards.
//
// Replaces the five-tab shell (Overview / Preview / Runs / Schedules /
// Advanced). The design was argued on /routines-new against the real
// renderer before landing here; what follows is that layout wired to
// real data.
//
// What went, and why:
//
//   Preview      the graph is on the page now. A tab holding a picture
//                of the thing you are already looking at exists to hide
//                the picture.
//   Advanced     three levels of nesting over four unrelated things.
//                The machinery is not gone — Editor opens beside the
//                graph, Schedules/Webhooks live inside Triggers, and
//                Versions has its own card. Nothing that worked was
//                deleted; it stopped being filed under a word that told
//                the reader nothing.
//   Wait points  belong to a RUN, not to a definition. Activity is
//                where the run is.
//
// Ordered by what an operator asks, in order: what is it → when does it
// run → is it healthy → what does it do → what can it reach → what did
// it do last.

import * as React from "react"
import Link from "next/link"
import {
  ArrowUpRight,
  Bot,
  CalendarClock,
  CheckCircle2,
  ChevronRight,
  Clock,
  Code2,
  GitBranch,
  Globe,
  KeyRound,
  Puzzle,
  ShieldAlert,
  Webhook,
  XCircle,
} from "lucide-react"

import { cn } from "@/lib/utils"
import { relTime, formatDurationDecimal } from "@/lib/time"
import { Appear, DetailCard, EntityChip, Pill, StatStrip } from "@/components/ui/detail"
import { usePipelineRunRecords } from "@/hooks/use-pipeline-run-records"
import { usePipelineSchedules } from "@/hooks/use-pipeline-schedules"
import { integrationLabel } from "@/lib/integration-labels"
import { credentialTypeLabel } from "@/lib/credential-labels"
import { brandIconForType, BrandGlyph } from "./brand-icons"
import { RoutineDefinitionCanvas } from "./routine-definition-canvas"
import { RoutineEditorTab } from "./routine-editor-tab"
import { RoutineSchedulesTab } from "./routine-schedules-tab"
import { RoutineWebhooksTab } from "./routine-webhooks-tab"
import { RoutineVersionsTab } from "./routine-versions-tab"
import { RoutineRunsTab } from "./routine-runs-tab"
import { RoutineReachCard } from "./routine-reach-card"
import type { RoutineDetail } from "./routines-detail-panel"

interface Props {
  routine: RoutineDetail
  workspaceId: string
  onChanged: () => void
}

type TriggerTab = "triggers" | "webhooks"

export function RoutineCardDetail({ routine, workspaceId, onChanged }: Props) {
  const [editing, setEditing] = React.useState(false)
  const [selected, setSelected] = React.useState<string | null>(null)
  const [triggerTab, setTriggerTab] = React.useState<TriggerTab>("triggers")
  const [manageTriggers, setManageTriggers] = React.useState(false)
  const [manageVersions, setManageVersions] = React.useState(false)
  // Cancelling a specific run lives in RoutineRunsTab, which has the
  // per-row buttons and the RBAC handling. Dropping the tab must not
  // drop the capability, so Manage mounts the real thing rather than a
  // reimplementation of it.
  const [manageRuns, setManageRuns] = React.useState(false)

  const { records } = usePipelineRunRecords(workspaceId, routine.slug)
  const { schedules } = usePipelineSchedules(workspaceId)

  const mine = React.useMemo(
    () => schedules.filter((s) => s.target_pipeline_id === routine.id),
    [schedules, routine.id],
  )
  const nextRun = React.useMemo(() => {
    const upcoming = mine
      .filter((s) => s.enabled && s.next_run_at)
      .map((s) => new Date(s.next_run_at!).getTime())
      .filter((t) => Number.isFinite(t))
      .sort((a, b) => a - b)
    return upcoming[0] ?? null
  }, [mine])

  const steps = React.useMemo(() => {
    const raw = (routine.definition as { steps?: unknown })?.steps
    return Array.isArray(raw) ? (raw as { type?: string }[]) : []
  }, [routine.definition])

  const stats = React.useMemo(() => summarise(records), [records])
  const lastRun = records[0] ?? null

  return (
    <div className="flex flex-col gap-4 p-4">
      <Appear order={0}>
        <StatStrip
          items={[
            {
              label: "Next run",
              value: nextRun ? relTime(new Date(nextRun).toISOString()) : "—",
            },
            {
              label: "Last run",
              value: routine.last_invoked_at ? relTime(routine.last_invoked_at) : "never",
              tone: toneOf(routine.last_invocation_status),
            },
            { label: "Runs", value: String(routine.invocation_count ?? 0) },
            {
              label: "Pass rate",
              value: stats.passRate === null ? "—" : `${stats.passRate}%`,
            },
            {
              label: "Avg duration",
              value: stats.avgMs > 0 ? formatDurationDecimal(stats.avgMs) : "—",
              mono: true,
            },
          ]}
        />
      </Appear>

      <div className="grid grid-cols-1 gap-4 xl:grid-cols-3 2xl:grid-cols-4">
        <Appear order={1} className="xl:col-span-2 2xl:col-span-3">
          <DetailCard
            title="Definition"
            subtitle={`${steps.length} ${steps.length === 1 ? "step" : "steps"}`}
            bare
            action={
              <button
                type="button"
                onClick={() => setEditing((v) => !v)}
                className={cn(
                  "inline-flex items-center gap-1.5 rounded-md border px-2 py-1 text-[11px] font-medium transition-colors",
                  editing
                    ? "border-primary/40 bg-primary/15 text-primary"
                    : "border-border/60 text-muted-foreground hover:text-foreground",
                )}
              >
                <Code2 className="h-3 w-3" />
                {editing ? "Close code" : "Edit code"}
              </button>
            }
          >
            {/* The editor is a sibling, not an overlay: as a sibling it
                takes real width and the graph slides left into what is
                still visible, instead of hiding underneath it. */}
            <div className="flex h-[56vh] min-h-[380px] flex-col md:flex-row">
              <div className="relative min-h-[240px] min-w-0 flex-1">
                <RoutineDefinitionCanvas
                  definition={routine.definition}
                  slug={routine.slug}
                  name={routine.name}
                  selectedStepId={selected}
                  onStepSelect={setSelected}
                />
              </div>
              {editing && (
                <aside className="h-[45%] w-full shrink-0 overflow-auto border-t border-border/60 md:h-auto md:w-[48%] md:border-l md:border-t-0">
                  <RoutineEditorTab
                    routine={routine}
                    workspaceId={workspaceId}
                    onSaved={onChanged}
                  />
                </aside>
              )}
            </div>
          </DetailCard>
        </Appear>

        <div className="flex flex-col gap-4">
          <Appear order={2}>
            <LastRunCard
              status={routine.last_invocation_status}
              at={routine.last_invoked_at}
              runId={lastRun?.id}
              durationMs={lastRun?.duration_ms}
              costUsd={lastRun?.cost_usd}
              slug={routine.slug}
            />
          </Appear>

          <Appear order={3}>
            <DetailCard
              title={triggerTab === "triggers" ? "Triggers" : "Webhooks"}
              subtitle={triggerTab === "triggers" ? String(mine.length) : undefined}
              icon={triggerTab === "triggers" ? CalendarClock : Webhook}
              tone="purple"
              action={
                <div className="flex items-center gap-1">
                  <div className="flex items-center gap-0.5 rounded-md border border-border/60 p-0.5">
                    {(["triggers", "webhooks"] as const).map((t) => (
                      <button
                        key={t}
                        type="button"
                        onClick={() => setTriggerTab(t)}
                        aria-pressed={triggerTab === t}
                        className={cn(
                          "rounded px-1.5 py-0.5 text-[10px] font-medium capitalize transition-colors",
                          triggerTab === t
                            ? "bg-primary/15 text-primary"
                            : "text-muted-foreground hover:text-foreground",
                        )}
                      >
                        {t}
                      </button>
                    ))}
                  </div>
                  <button
                    type="button"
                    onClick={() => setManageTriggers((v) => !v)}
                    className="rounded-md border border-border/60 px-1.5 py-1 text-[10px] font-medium text-muted-foreground transition-colors hover:text-foreground"
                  >
                    {manageTriggers ? "Done" : "Manage"}
                  </button>
                </div>
              }
            >
              {/* Manage reveals the working CRUD rather than replacing
                  it: the schedule and webhook editors are unchanged, they
                  are just no longer filed under a tab called Advanced. */}
              {manageTriggers ? (
                triggerTab === "triggers" ? (
                  <RoutineSchedulesTab
                    workspaceId={workspaceId}
                    pipelineId={routine.id}
                    slug={routine.slug}
                  />
                ) : (
                  <RoutineWebhooksTab
                    workspaceId={workspaceId}
                    pipelineId={routine.id}
                    slug={routine.slug}
                  />
                )
              ) : triggerTab === "triggers" ? (
                <ScheduleList schedules={mine} />
              ) : (
                <p className="text-[12px] text-muted-foreground">
                  Inbound HTTP triggers. Press Manage to add or rotate one.
                </p>
              )}
            </DetailCard>
          </Appear>

          <Appear order={4}>
            <AccessCard routine={routine} />
          </Appear>

          {(routine.manifest?.agents?.length ?? 0) > 0 && (
            <Appear order={5}>
              <RoutineReachCard
                workspaceId={workspaceId}
                agentSlugs={routine.manifest?.agents ?? []}
              />
            </Appear>
          )}

          <Appear order={6}>
            <DetailCard
              title="Versions"
              subtitle={routine.head_version ? `v${routine.head_version}` : undefined}
              icon={GitBranch}
              action={
                <button
                  type="button"
                  onClick={() => setManageVersions((v) => !v)}
                  className="rounded-md border border-border/60 px-1.5 py-1 text-[10px] font-medium text-muted-foreground transition-colors hover:text-foreground"
                >
                  {manageVersions ? "Done" : "History"}
                </button>
              }
            >
              {manageVersions ? (
                <RoutineVersionsTab
                  workspaceId={workspaceId}
                  slug={routine.slug}
                  onRolledBack={onChanged}
                />
              ) : (
                <Metadata routine={routine} steps={steps.length} />
              )}
            </DetailCard>
          </Appear>
        </div>
      </div>

      <Appear order={7}>
        <RunsCard
          slug={routine.slug}
          workspaceId={workspaceId}
          records={records}
          manage={manageRuns}
          onManageChange={setManageRuns}
        />
      </Appear>
    </div>
  )
}

/* ------------------------------------------------------------------ *
 *  Pieces                                                             *
 * ------------------------------------------------------------------ */

function toneOf(status?: string): "success" | "destructive" | "default" {
  const s = status?.toLowerCase()
  if (s === "completed" || s === "succeeded" || s === "success") return "success"
  if (s === "failed" || s === "error") return "destructive"
  return "default"
}

function summarise(records: { status: string; duration_ms?: number }[]) {
  if (records.length === 0) return { passRate: null as number | null, avgMs: 0 }
  const terminal = records.filter((r) => {
    const s = r.status?.toLowerCase()
    return s === "completed" || s === "succeeded" || s === "failed" || s === "error"
  })
  const ok = terminal.filter((r) => {
    const s = r.status?.toLowerCase()
    return s === "completed" || s === "succeeded"
  }).length
  const durations = records.map((r) => r.duration_ms ?? 0).filter((d) => d > 0)
  return {
    passRate: terminal.length > 0 ? Math.round((ok / terminal.length) * 100) : null,
    avgMs: durations.length > 0 ? durations.reduce((a, b) => a + b, 0) / durations.length : 0,
  }
}

/**
 * Last run, with a tinted header.
 *
 * The one place on the page where colour carries meaning rather than
 * decoration: it says how this ended before a word is read, and does it
 * with a 6%-opacity gradient rather than shouting.
 */
function LastRunCard({
  status,
  at,
  runId,
  durationMs,
  costUsd,
  slug,
}: {
  status?: string
  at?: string
  runId?: string
  durationMs?: number
  costUsd?: number
  slug: string
}) {
  const tone = toneOf(status)
  const ok = tone === "success"
  const bad = tone === "destructive"
  const Icon = ok ? CheckCircle2 : bad ? XCircle : Clock

  if (!at) {
    return (
      <DetailCard title="Last run">
        <p className="text-[12px] text-muted-foreground">
          This routine hasn&apos;t been invoked yet.
        </p>
      </DetailCard>
    )
  }

  return (
    <div className="overflow-hidden rounded-xl border border-border/60 bg-card">
      <div
        className={cn(
          "flex items-center gap-3 border-b border-border/40 px-4 py-3",
          ok && "bg-gradient-to-r from-success/[0.06] to-transparent",
          bad && "bg-gradient-to-r from-destructive/[0.06] to-transparent",
        )}
      >
        <div
          className={cn(
            "flex h-8 w-8 shrink-0 items-center justify-center rounded-full",
            ok && "bg-success/20 text-success",
            bad && "bg-destructive/20 text-destructive",
            !ok && !bad && "bg-primary/20 text-primary",
          )}
        >
          <Icon className="h-4 w-4" />
        </div>
        <div className="min-w-0 flex-1">
          <div className="text-[13px] font-medium capitalize">
            Last run · {status ?? "unknown"}
          </div>
          {runId && (
            <div className="truncate font-mono text-[10px] text-muted-foreground">{runId}</div>
          )}
        </div>
      </div>
      <div className="space-y-2 px-4 py-3">
        <dl className="grid grid-cols-3 gap-2 text-[11px]">
          <Fact label="started" value={relTime(at)} />
          <Fact
            label="duration"
            value={durationMs && durationMs > 0 ? formatDurationDecimal(durationMs) : "—"}
          />
          <Fact label="cost" value={costUsd ? `$${costUsd.toFixed(4)}` : "—"} />
        </dl>
        <Link
          href={activityHref(slug, runId)}
          className="inline-flex items-center gap-1 text-[11px] text-primary hover:underline"
        >
          Open full trace
          <ArrowUpRight className="h-3 w-3" />
        </Link>
      </div>
    </div>
  )
}

function Fact({ label, value }: { label: string; value: string }) {
  return (
    <div>
      <dt className="text-[10px] uppercase tracking-wider text-muted-foreground-soft">{label}</dt>
      <dd className="tabular-nums text-foreground/85">{value}</dd>
    </div>
  )
}

/**
 * Where a run row lands.
 *
 * Filtered to this routine AND selecting the run — arriving at an
 * unfiltered rail of every run in the workspace makes the reader rebuild
 * the context they came with.
 */
function activityHref(slug: string, runId?: string): string {
  const params = new URLSearchParams({ pipeline: slug })
  if (runId) params.set("run", runId)
  return `/activity?${params.toString()}`
}

function ScheduleList({
  schedules,
}: {
  schedules: { id: string; name: string; cron_expr: string; timezone: string; enabled: boolean; next_run_at?: string }[]
}) {
  if (schedules.length === 0) {
    return (
      <p className="text-[12px] text-muted-foreground">
        No cron triggers. Press Manage to add one.
      </p>
    )
  }
  return (
    <ul className="space-y-2.5 text-[12px]">
      {schedules.map((s) => (
        <li key={s.id} className="flex items-start gap-2">
          <CalendarClock
            className={cn(
              "mt-0.5 h-3.5 w-3.5 shrink-0",
              s.enabled ? "text-muted-foreground" : "text-muted-foreground-soft",
            )}
          />
          <div className="min-w-0 flex-1">
            <div className={cn("truncate", s.enabled ? "text-foreground/90" : "text-muted-foreground")}>
              {s.name}
            </div>
            <div className="flex flex-wrap items-baseline gap-x-2 text-[10px] text-muted-foreground">
              <span className="font-mono">{s.cron_expr}</span>
              <span aria-hidden>·</span>
              <span>{s.timezone}</span>
              {s.enabled && s.next_run_at && (
                <>
                  <span aria-hidden>·</span>
                  <span className="text-info">next {relTime(s.next_run_at)}</span>
                </>
              )}
              {!s.enabled && (
                <>
                  <span aria-hidden>·</span>
                  <span>paused</span>
                </>
              )}
            </div>
          </div>
        </li>
      ))}
    </ul>
  )
}

/**
 * Everything the routine can reach, as one row of chips.
 *
 * Four cards answering one question — "what could this do if it
 * misbehaved?" — meant nobody read the third. Real brand marks where
 * one exists: a chip reading "Gmail" beside a generic puzzle piece has
 * stopped carrying its own meaning.
 */
function AccessCard({ routine }: { routine: RoutineDetail }) {
  const m = routine.manifest
  const items: { key: string; label: string; icon: React.ComponentType<{ className?: string }>; risk?: boolean }[] = []

  for (const i of m?.integrations ?? routine.integrations_required ?? []) {
    items.push({ key: `i:${i}`, label: integrationLabel(i), icon: Puzzle, risk: true })
  }
  for (const c of m?.credentials ?? []) {
    items.push({ key: `c:${c.type}`, label: credentialTypeLabel(c.type), icon: KeyRound, risk: true })
  }
  for (const a of m?.agents ?? []) {
    items.push({ key: `a:${a}`, label: a, icon: Bot })
  }
  for (const e of m?.egress ?? []) {
    items.push({ key: `e:${e}`, label: e, icon: Globe, risk: true })
  }

  return (
    <DetailCard
      title="Access"
      subtitle="what this can reach"
      icon={ShieldAlert}
      tone="warn"
      footer={items.length > 0 ? "Amber marks reach a reviewer should look at twice." : undefined}
    >
      {items.length === 0 ? (
        <p className="text-[12px] text-muted-foreground">
          Nothing outside Crewship — no integrations, credentials or egress.
        </p>
      ) : (
        <div className="flex flex-wrap gap-1.5">
          {items.map((item) => {
            const brand = brandIconForType(item.label)
            return (
              <EntityChip
                key={item.key}
                icon={
                  brand
                    ? () => <BrandGlyph brand={brand} fallback={item.icon} className="h-3 w-3" />
                    : item.icon
                }
                label={item.label}
                tone={item.risk ? "warn" : "default"}
              />
            )
          })}
        </div>
      )}
    </DetailCard>
  )
}

/**
 * The flat facts.
 *
 * Low weight on their own, which is why they sit inside the Versions
 * card rather than taking one of their own — but paired with the version
 * history they answer "what changed since the run that worked", which is
 * the question you have precisely when something has broken.
 */
function Metadata({ routine, steps }: { routine: RoutineDetail; steps: number }) {
  const rows: [string, string][] = [
    ["DSL version", routine.dsl_version],
    ["Visibility", routine.workspace_visible ? "workspace" : "private"],
    ["Hash", routine.definition_hash ? `${routine.definition_hash.slice(0, 10)}…` : "—"],
    ["Steps", String(steps)],
    ["Created", routine.created_at ? relTime(routine.created_at) : "—"],
    ["Updated", routine.updated_at ? relTime(routine.updated_at) : "—"],
  ]
  return (
    <dl className="grid grid-cols-2 gap-x-4 gap-y-2.5 text-[11px]">
      {rows.map(([k, v]) => (
        <div key={k}>
          <dt className="text-[10px] uppercase tracking-wider text-muted-foreground-soft">{k}</dt>
          <dd className="mt-0.5 truncate font-mono text-foreground/85">{v}</dd>
        </div>
      ))}
    </dl>
  )
}

function RunsCard({
  slug,
  workspaceId,
  records,
  manage,
  onManageChange,
}: {
  slug: string
  workspaceId: string
  records: { id: string; status: string; started_at?: string; duration_ms?: number; cost_usd?: number; triggered_via?: string }[]
  manage: boolean
  onManageChange: (next: boolean) => void
}) {
  return (
    <DetailCard
      title="Runs"
      subtitle={records.length > 0 ? String(records.length) : undefined}
      icon={Clock}
      bare
      action={
        <button
          type="button"
          onClick={() => onManageChange(!manage)}
          className="rounded-md border border-border/60 px-1.5 py-1 text-[10px] font-medium text-muted-foreground transition-colors hover:text-foreground"
        >
          {manage ? "Done" : "Manage"}
        </button>
      }
    >
      {manage ? (
        <div className="px-4 py-3">
          <RoutineRunsTab workspaceId={workspaceId} slug={slug} />
        </div>
      ) : (
        <RunsList slug={slug} records={records} />
      )}
    </DetailCard>
  )
}

function RunsList({
  slug,
  records,
}: {
  slug: string
  records: { id: string; status: string; started_at?: string; duration_ms?: number; cost_usd?: number; triggered_via?: string }[]
}) {
  return (
    <>
      {records.length === 0 ? (
        <p className="px-4 py-3 text-[12px] text-muted-foreground">No runs recorded yet.</p>
      ) : (
        <ul className="divide-y divide-border/40">
          {records.slice(0, 8).map((r) => {
            const tone = toneOf(r.status)
            const Icon = tone === "success" ? CheckCircle2 : tone === "destructive" ? XCircle : Clock
            return (
              <li key={r.id}>
                <Link
                  href={activityHref(slug, r.id)}
                  className="grid grid-cols-[auto_1fr_auto_auto] items-center gap-3 px-4 py-2.5 transition-colors hover:bg-white/[0.025]"
                >
                  <Icon
                    className={cn(
                      "h-4 w-4 shrink-0",
                      tone === "success" && "text-success",
                      tone === "destructive" && "text-destructive",
                      tone === "default" && "text-muted-foreground",
                    )}
                  />
                  <div className="min-w-0">
                    <div className="truncate font-mono text-[11px] text-foreground/85">{r.id}</div>
                    <div className="text-[10px] uppercase tracking-wide text-muted-foreground">
                      {r.triggered_via ?? "manual"}
                    </div>
                  </div>
                  <div className="text-right text-[11px] tabular-nums text-muted-foreground">
                    {r.started_at ? relTime(r.started_at) : "—"}
                  </div>
                  <ChevronRight className="h-3.5 w-3.5 text-muted-foreground-soft" />
                </Link>
              </li>
            )
          })}
        </ul>
      )}
      <div className="border-t border-border/60 px-4 py-2">
        <Link
          href={activityHref(slug)}
          className="inline-flex items-center gap-1 text-[11px] text-primary hover:underline"
        >
          All runs in Activity
          <ArrowUpRight className="h-3 w-3" />
        </Link>
      </div>
    </>
  )
}

// Referenced for the Pill import so the identity chrome in the panel
// above can keep using the same tone vocabulary.
void Pill
