"use client"

import { useState } from "react"
import Link from "next/link"
import { ArrowUpRight, Hand, List, PenLine, Play } from "lucide-react"

import { PageEmpty, PageLoading } from "@/components/layout/page-state"
import { StatusPill } from "@/components/ui/status-pill"
import type { StatusTone } from "@/lib/format-status"
import { isAwaitingApproval, useActiveRoutineRuns } from "@/hooks/use-active-routine-runs"
import { usePipelineRuns, type PipelineRun } from "@/hooks/use-pipeline-runs"
import type { Pipeline } from "@/hooks/use-pipelines"
import type { RoutineDraftListEntry } from "@/lib/routine-drafts"
import { routineRunPresentation } from "@/lib/routine-run-presentation"
import { formatElapsedSince, formatStepDuration } from "./routine-cost-format"
import { RoutineGlyph } from "./routine-glyph"
import { RoutineRunPeek } from "./routine-run-peek"
import type { RoutinesView } from "./routines-explorer"

// The four list views beside Overview. Each answers one question from data
// the server already returns — no aggregate the API does not have:
//   Needs you    runs parked on a person, routines waiting for review, the
//                ones whose last run could not finish, and the turned-off;
//   Running now  the live run feed;
//   All runs     GET /pipeline-runs (the server returns at most 200);
//   Drafts       GET /pipelines/drafts.

export interface NeedsItem {
  key: string
  slug: string
  name: string
  reason: string
  tone: StatusTone
  label: string
  runId?: string
}

/** What needs a person, in the order it should be handled. */
export function needsYou(routines: Pipeline[], active: PipelineRun[]): NeedsItem[] {
  const name = (slug: string) => routines.find((r) => r.slug === slug)?.name || slug
  const out: NeedsItem[] = []
  for (const run of active) {
    if (!isAwaitingApproval(run.status)) continue
    out.push({ key: `run-${run.id}`, slug: run.pipeline_slug, name: run.pipeline_name || name(run.pipeline_slug), reason: "Waiting for a person", tone: "warn", label: "Waiting", runId: run.id })
  }
  for (const r of routines) if (r.status === "proposed") out.push({ key: `p-${r.slug}`, slug: r.slug, name: r.name || r.slug, reason: "Proposed · waiting for a Manager review", tone: "purple", label: "Proposed" })
  for (const r of routines)
    if (r.status !== "disabled" && r.status !== "proposed" && (r.last_invocation_status?.toLowerCase() === "failed" || r.last_run_outcome === "FAILED"))
      out.push({ key: `f-${r.slug}`, slug: r.slug, name: r.name || r.slug, reason: "Could not finish", tone: "danger", label: "Failed" })
  for (const r of routines) if (r.status === "disabled") out.push({ key: `d-${r.slug}`, slug: r.slug, name: r.name || r.slug, reason: "Turned off", tone: "muted", label: "Off" })
  return out
}

const TITLES: Record<Exclude<RoutinesView, "overview">, { title: string; hint: string }> = {
  needs: { title: "Needs you", hint: "Decisions, reviews and routines that stopped." },
  live: { title: "Running now", hint: "Runs in progress and waiting in line." },
  runs: { title: "All runs", hint: "The latest runs across every routine." },
  drafts: { title: "Drafts", hint: "Changes and new routines that are not live yet." },
}

const PILL_TONE: Record<string, StatusTone> = { success: "success", destructive: "danger", warn: "warn", blue: "blue", default: "muted" }

function Row({ onClick, glyph, title, sub, pill, meta }: { onClick: () => void; glyph: React.ReactNode; title: string; sub: React.ReactNode; pill?: React.ReactNode; meta?: React.ReactNode }) {
  return (
    <li>
      <button
        type="button"
        onClick={onClick}
        className="flex w-full items-center gap-3 border-b border-hairline px-4 py-2.5 text-left transition-colors hover:bg-foreground/[0.03] coarse:min-h-12"
      >
        {glyph}
        <span className="min-w-0 flex-1">
          <span className="block truncate text-body font-medium text-foreground">{title}</span>
          <span className="block truncate text-label text-muted-foreground">{sub}</span>
        </span>
        {meta && <span className="shrink-0 text-label tabular-nums text-muted-foreground">{meta}</span>}
        {pill}
      </button>
    </li>
  )
}

export function RoutinesViewPane({
  view,
  workspaceId,
  routines,
  drafts,
  onSelect,
  onOpenRun,
}: {
  view: Exclude<RoutinesView, "overview">
  workspaceId: string
  routines: Pipeline[]
  drafts: RoutineDraftListEntry[] | null
  onSelect: (slug: string) => void
  onOpenRun: (slug: string, runId: string) => void
}) {
  const { runs: active } = useActiveRoutineRuns()
  const { runs, loading } = usePipelineRuns(view === "runs" ? workspaceId : null, "all", 200, { poll: false })
  const bySlug = new Map(routines.map((r) => [r.slug, r]))
  const glyph = (slug: string) => <RoutineGlyph routine={bySlug.get(slug) ?? { slug }} size="sm" />
  const t = TITLES[view]
  const [peek, setPeek] = useState<string | null>(null)

  let body: React.ReactNode
  if (view === "needs") {
    const items = needsYou(routines, active)
    body = items.length === 0 ? (
      <PageEmpty icon={Hand} title="Nothing needs you" description="No decision is waiting, nothing is up for review and every routine finished its last run." />
    ) : (
      <ul aria-label="Needs you">
        {items.map((i) => (
          <Row
            key={i.key}
            onClick={() => (i.runId ? onOpenRun(i.slug, i.runId) : onSelect(i.slug))}
            glyph={glyph(i.slug)}
            title={i.name}
            sub={i.reason}
            pill={<StatusPill tone={i.tone} label={i.label} />}
          />
        ))}
      </ul>
    )
  } else if (view === "live") {
    const live = active.filter((r) => !isAwaitingApproval(r.status))
    body = live.length === 0 ? (
      <PageEmpty icon={Play} title="Nothing is running" description="Runs show here while they work, with the step they are on." />
    ) : (
      <ul aria-label="Running now">
        {live.map((r) => (
          <Row
            key={r.id}
            onClick={() => onOpenRun(r.pipeline_slug, r.id)}
            glyph={glyph(r.pipeline_slug)}
            title={r.pipeline_name || r.pipeline_slug}
            sub={r.status === "queued" ? "Waiting in line" : r.current_step_id ? `On step ${r.current_step_id}` : "Starting"}
            meta={r.started_at ? formatElapsedSince(r.started_at) : undefined}
            pill={<StatusPill tone="blue" label={r.status === "queued" ? "Queued" : "Running"} live={r.status !== "queued"} />}
          />
        ))}
      </ul>
    )
  } else if (view === "runs") {
    body = loading && runs.length === 0 ? (
      <PageLoading />
    ) : runs.length === 0 ? (
      <PageEmpty icon={List} title="No runs yet" description="Every run of every routine shows here, newest first." />
    ) : (
      <ul aria-label="All runs">
        {runs.map((r) => {
          const p = routineRunPresentation(r)
          return (
            <Row
              key={r.id}
              // A row opens the run peek (RT-PEEK); Open run in it opens the page.
              onClick={() => setPeek(r.id)}
              glyph={glyph(r.pipeline_slug)}
              title={r.pipeline_name || r.pipeline_slug}
              sub={`${r.started_at ? new Date(r.started_at).toLocaleString(undefined, { dateStyle: "medium", timeStyle: "short" }) : "Not started"} · ${r.triggered_via || "manual"}`}
              meta={r.duration_ms ? formatStepDuration(r.duration_ms) : undefined}
              pill={<StatusPill tone={PILL_TONE[p.tone] ?? "muted"} label={p.label} />}
            />
          )
        })}
      </ul>
    )
  } else {
    body = drafts == null ? (
      <PageLoading />
    ) : drafts.length === 0 ? (
      <PageEmpty icon={PenLine} title="No drafts" description="A draft holds changes that are not live yet, or a new routine before it is published." />
    ) : (
      <ul aria-label="Drafts">
        {drafts.map((d) => {
          const routine = bySlug.get(d.slug)
          const isNew = !routine || !(routine.head_version ?? 0)
          return (
            <Row
              key={d.slug}
              onClick={() => onSelect(d.slug)}
              glyph={glyph(d.slug)}
              title={routine?.name || d.slug}
              sub={`${isNew ? "Not live yet" : "Changes not live"} · draft r${d.revision}${d.updated_at ? ` · ${new Date(d.updated_at).toLocaleDateString()}` : ""}`}
            />
          )
        })}
      </ul>
    )
  }

  return (
    <section aria-label={t.title} className="flex h-full min-w-0 flex-col overflow-auto">
      <div className="flex items-end gap-3 px-4 pb-2 pt-4 md:px-6">
        <div className="min-w-0 flex-1">
          <h2 className="text-heading font-semibold text-foreground">{t.title}</h2>
          <p className="text-label text-muted-foreground">{t.hint}</p>
        </div>
        {view === "runs" && (
          <Link href="/activity?lens=routines" className="inline-flex items-center gap-1 text-label text-muted-foreground hover:text-foreground">
            Runs and history in Activity <ArrowUpRight className="h-3 w-3" />
          </Link>
        )}
      </div>
      <div className="min-h-0 flex-1 md:px-2">{body}</div>
      {view === "runs" && peek && (
        <RoutineRunPeek
          workspaceId={workspaceId}
          runs={runs}
          runId={peek}
          routineName={(r) => r.pipeline_name || r.pipeline_slug}
          onChange={setPeek}
          onClose={() => setPeek(null)}
          onOpen={(id) => {
            const r = runs.find((x) => x.id === id)
            setPeek(null)
            if (r) onOpenRun(r.pipeline_slug, id)
          }}
        />
      )}
    </section>
  )
}
