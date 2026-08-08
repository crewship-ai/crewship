"use client"

// One activity, opened as its own surface.
//
// Not a right-hand panel: an activity is a thing with a shape — a trigger,
// steps, a result, a chain it belongs to — and a 340px column is where that
// shape goes to die. It takes the whole content area, the way a routine or
// an issue does.
//
// The centrepiece is the EXISTING TraceCanvas, the same node graph /activity
// has always drawn. `trace_id` on a journal row is the run id (see the CLI's
// own `--trace-id` help: "Filter by run/trace ID — narrows to one run's
// spans"), so any entry that carries one can be resolved to its run and
// rendered as the graph rather than described in prose.

import * as React from "react"
import dynamic from "next/dynamic"
import { ArrowLeft, Clock, Coins, GitBranch, Layers, ListTree } from "lucide-react"

import { DashboardCard } from "@/components/features/dashboard/dashboard-card"
import { KpiCard } from "@/components/features/dashboard/kpi-card"
import { Appear, FieldLabel, Pill } from "@/components/ui/detail"
import { Button } from "@/components/ui/button"
import { Spinner } from "@/components/ui/spinner"
import { useTrace } from "@/hooks/use-trace"
import { useStepMetrics } from "@/hooks/use-step-metrics"
import { apiFetch } from "@/lib/api-fetch"
import { journalListResponseSchema, type JournalEntry } from "@/lib/types/journal"
import {
  activitySource,
  buildSpine,
  entryCostUSD,
  entryDurationMs,
  formatDurationMs,
  runIdOf,
  scopeOf,
  severityTone,
  sourceMeta,
  type SpineLabels,
  type SpineLink,
} from "@/lib/activity-stream"
import { relTime } from "@/lib/time"

import { FeedRow } from "./feed-row"
import { iconFor } from "./activity-overview"
import { TopologyCard } from "./topology-card"

// React Flow (~200 KB+) loads only when a graph actually renders. /activity
// made this call deliberately (activity-trace-page.tsx:32) to keep
// @xyflow/react out of its initial route chunk; importing it statically here
// put it straight back into activity-new's, for a card most visits never open.
const TraceCanvas = dynamic(
  () => import("@/components/features/activity/trace-canvas").then((m) => m.TraceCanvas),
  {
    ssr: false,
    loading: () => (
      <div className="flex h-full items-center justify-center text-xs text-muted-foreground">
        Loading execution graph…
      </div>
    ),
  },
)


// Two refinements the detail does not wire yet: inline waitpoint decisions
// and heatmap shading. Empty maps are the canvas's documented "nothing to
// overlay" input, not a stub — the graph itself is complete.
const NO_WAITPOINTS: ReadonlyMap<string, string> = new Map<string, string>()
const NO_HEATMAP = new Map() as React.ComponentProps<typeof TraceCanvas>["heatmapBuckets"]

export interface ActivityDetailProps {
  entry: JournalEntry
  workspaceId: string
  labels: SpineLabels
  agentName: (id?: string) => string | undefined
  crewName: (id?: string) => string | undefined
  crewMeta: (id?: string) => { icon?: string | null; color?: string | null } | undefined
  onBack: () => void
  onSelectEntry: (e: JournalEntry) => void
  onSpineClick: (l: SpineLink) => void
}

export function ActivityDetail({
  entry,
  workspaceId,
  labels,
  agentName,
  crewName,
  crewMeta,
  onBack,
  onSelectEntry,
  onSpineClick,
}: ActivityDetailProps) {
  // See runIdOf: trace_id alone misses every routine run.
  const traceID = runIdOf(entry)

  // The run behind this event, if it has one. Same hook /activity uses, so
  // the graph here is the graph there — not a second renderer that will
  // drift.
  const { run, dsl, loading: traceLoading } = useTrace(workspaceId, traceID)
  const { metrics: stepMetrics } = useStepMetrics(workspaceId, run?.pipeline_slug, traceID)
  const [stepId, setStepId] = React.useState<string | null>(null)

  const [chain, setChain] = React.useState<JournalEntry[]>([])
  const [chainLoading, setChainLoading] = React.useState(false)

  React.useEffect(() => {
    if (!traceID) {
      setChain([])
      return
    }
    let cancelled = false
    setChainLoading(true)
    apiFetch(
      // run_id, not trace_id: routine runs never set trace_id (they stamp
      // actor_id + payload.run_id), so the trace_id-only query returned an
      // empty chain for exactly the runs this graph exists to show.
      `/api/v1/journal?workspace_id=${encodeURIComponent(workspaceId)}&run_id=${encodeURIComponent(
        traceID,
      )}&limit=200`,
    )
      .then(async (r) => (r.ok ? journalListResponseSchema.parse(await r.json()) : { entries: [] }))
      .then((d) => {
        // Oldest first — a chain is read forwards, unlike a feed.
        if (!cancelled) setChain([...d.entries].sort((a, b) => a.ts.localeCompare(b.ts)))
      })
      .catch(() => {
        if (!cancelled) setChain([])
      })
      .finally(() => {
        if (!cancelled) setChainLoading(false)
      })
    return () => {
      cancelled = true
    }
  }, [traceID, workspaceId])

  const meta = sourceMeta(activitySource(entry.entry_type))
  const spine = buildSpine(entry, labels)
  const tone = severityTone(String(entry.severity))
  const duration = entryDurationMs(entry)
  const cost = entryCostUSD(entry)

  // Totals across the whole chain, not just this row — "this step took 8s"
  // matters less than "the run it belonged to took 40s and cost $0.06".
  const chainTotals = React.useMemo(() => {
    let ms = 0
    let usd = 0
    let errors = 0
    for (const c of chain) {
      ms += entryDurationMs(c) ?? 0
      usd += entryCostUSD(c) ?? 0
      if (c.severity === "error") errors += 1
    }
    return { ms, usd, errors }
  }, [chain])

  const rowProps = (e: JournalEntry) => ({
    entry: e,
    icon: iconFor(e),
    labels,
    actorName: agentName(e.agent_id),
    crewName: crewName(e.crew_id),
    agentId: e.agent_id,
    crewIcon: crewMeta(e.crew_id)?.icon,
    crewColor: crewMeta(e.crew_id)?.color,
    selected: e.id === entry.id,
    onSelect: () => onSelectEntry(e),
    onSpineClick,
  })

  return (
    <div className="mx-auto flex max-w-[1800px] flex-col gap-4 p-4 md:p-6">
      <Appear order={0}>
        <div className="flex flex-col gap-2">
          <button
            type="button"
            onClick={onBack}
            className="inline-flex w-fit items-center gap-1.5 rounded-md px-1.5 py-1 text-xs text-muted-foreground transition-colors hover:bg-muted hover:text-foreground"
          >
            <ArrowLeft className="h-3.5 w-3.5" /> Back to activity
          </button>

          <div className="flex flex-wrap items-center gap-2">
            <span
              className="rounded border border-white/[0.08] px-1.5 py-px font-mono text-[10px] uppercase tracking-wider"
              style={{ color: `var(${meta.token})` }}
            >
              {meta.label}
            </span>
            <h1 className="min-w-0 text-lg font-semibold tracking-tight">{entry.summary}</h1>
            {tone !== "default" && <Pill tone={tone}>{String(entry.severity)}</Pill>}
          </div>

          <p className="flex flex-wrap items-center gap-2 text-xs text-muted-foreground">
            <span className="font-mono">{entry.entry_type}</span>
            <span>·</span>
            <span>{new Date(entry.ts).toLocaleString(undefined, { hour12: false })}</span>
            <span>·</span>
            <span>{relTime(entry.ts)}</span>
            {agentName(entry.agent_id) && (
              <>
                <span>·</span>
                <span>{agentName(entry.agent_id)}</span>
              </>
            )}
            {crewName(entry.crew_id) && (
              <>
                <span>·</span>
                <span>{crewName(entry.crew_id)}</span>
              </>
            )}
          </p>

          {spine.length > 0 && (
            <div className="flex flex-wrap items-center gap-1">
              {spine.map((l, i) => (
                <React.Fragment key={`${l.kind}-${l.id}`}>
                  {i > 0 && <span className="text-[10px] text-muted-foreground-soft">›</span>}
                  <button
                    type="button"
                    onClick={() => onSpineClick(l)}
                    className="rounded bg-white/[0.05] px-1.5 py-px text-[11px] text-foreground/85 transition-colors hover:bg-white/[0.1]"
                  >
                    <span className="mr-1 font-mono text-[9.5px] uppercase text-muted-foreground-soft">
                      {l.kind}
                    </span>
                    {l.label}
                  </button>
                </React.Fragment>
              ))}
            </div>
          )}
        </div>
      </Appear>

      <Appear order={1}>
        <div className="grid grid-cols-2 gap-4 lg:grid-cols-4">
          <KpiCard
            label="This event"
            value={formatDurationMs(duration)}
            subtitle={cost != null ? `$${cost.toFixed(4)}` : scopeOf(entry)}
          />
          <KpiCard
            label="Chain duration"
            value={chainTotals.ms > 0 ? formatDurationMs(chainTotals.ms) : "—"}
            subtitle={`${chain.length} ${chain.length === 1 ? "event" : "events"}`}
          />
          <KpiCard
            label="Chain cost"
            value={`$${chainTotals.usd.toFixed(3)}`}
            subtitle={traceID ? "across the trace" : "no trace id"}
          />
          <KpiCard
            label="Errors in chain"
            value={chainTotals.errors}
            subtitle={chainTotals.errors === 0 ? "clean" : "see below"}
          />
        </div>
      </Appear>

      {/* ── The graph. The reason this opens as a page. ───────────── */}
      <Appear order={2}>
        <DashboardCard
          title="Execution graph"
          icon={GitBranch}
          hint={run ? run.pipeline_slug : traceID ? "resolving…" : "no run behind this event"}
        >
          {!traceID && (
            <div className="flex flex-col items-center justify-center gap-1.5 py-10 text-center">
              <ListTree className="h-4 w-4 text-muted-foreground-soft" />
              <p className="max-w-[320px] text-[11px] text-muted-foreground-soft">
                This event carries no trace id, so there is no execution chain to draw. Events
                emitted outside a run — a migration, a status change — look like this.
              </p>
            </div>
          )}
          {traceID && traceLoading && !run && (
            <div className="flex items-center justify-center gap-2 py-10 text-xs text-muted-foreground">
              <Spinner className="h-3.5 w-3.5" /> Loading the run…
            </div>
          )}
          {traceID && !traceLoading && !run && (
            <div className="flex flex-col items-center justify-center gap-1.5 py-10 text-center">
              <ListTree className="h-4 w-4 text-muted-foreground-soft" />
              <p className="max-w-[320px] text-[11px] text-muted-foreground-soft">
                The trace id on this event does not resolve to a stored run — it may have been
                pruned, or emitted by something that is not a routine.
              </p>
            </div>
          )}
          {run && (
            <div className="h-[420px] w-full overflow-hidden rounded-md border border-white/[0.06]">
              <TraceCanvas
                run={run}
                dsl={dsl}
                selectedStepId={stepId}
                onStepSelect={setStepId}
                workspaceId={workspaceId}
                waitpointTokensByStepId={NO_WAITPOINTS}
                heatmapBuckets={NO_HEATMAP}
                stepMetrics={stepMetrics}
                initialFocus="start"
                centerOnSelect
              />
            </div>
          )}
        </DashboardCard>
      </Appear>

      {/* ── How this came about, across features ─────────────────── */}
      <Appear order={3}>
        <TopologyCard
          workspaceId={workspaceId}
          anchor={entry.mission_id || traceID || entry.id}
          anchorLabel={
            entry.mission_id ? (labels.issues?.[entry.mission_id] ?? "this issue") : "this run"
          }
        />
      </Appear>

      {/* ── Everything that happened under the same trace ─────────── */}
      <Appear order={4}>
        <DashboardCard
          title="Chain"
          icon={Layers}
          hint={chainLoading ? "loading…" : `${chain.length} events`}
          action={
            traceID ? (
              <button
                type="button"
                onClick={() => void navigator.clipboard?.writeText(traceID)}
                className="text-primary hover:underline"
              >
                Copy trace id
              </button>
            ) : undefined
          }
        >
          {chain.length === 0 && !chainLoading && (
            <div className="flex flex-col items-center justify-center gap-1.5 py-8 text-center">
              <Clock className="h-4 w-4 text-muted-foreground-soft" />
              <p className="text-[11px] text-muted-foreground-soft">
                Nothing else was recorded under this trace.
              </p>
            </div>
          )}
          {chainLoading && chain.length === 0 && (
            <div className="flex items-center justify-center gap-2 py-8 text-xs text-muted-foreground">
              <Spinner className="h-3.5 w-3.5" /> Loading the chain…
            </div>
          )}
          {chain.length > 0 && (
            <div className="flex flex-col">
              {chain.map((c) => (
                <FeedRow key={c.id} {...rowProps(c)} />
              ))}
            </div>
          )}
        </DashboardCard>
      </Appear>

      {/* ── The raw record, last, for when the rendering is not enough ─ */}
      {(entry.payload && Object.keys(entry.payload).length > 0) ||
      (entry.refs && Object.keys(entry.refs).length > 0) ? (
        <Appear order={5}>
          <DashboardCard title="Record" icon={Coins} hint={entry.id}>
            <div className="flex flex-col gap-3">
              <div className="flex flex-col gap-1">
                <FieldLabel>Identity</FieldLabel>
                <dl className="grid grid-cols-[92px_minmax(0,1fr)] gap-x-3 gap-y-1 text-[11.5px]">
                  <dt className="font-mono text-[10.5px] text-muted-foreground-soft">entry id</dt>
                  <dd className="m-0 truncate font-mono text-foreground/85">{entry.id}</dd>
                  {traceID && (
                    <>
                      <dt className="font-mono text-[10.5px] text-muted-foreground-soft">trace id</dt>
                      <dd className="m-0 truncate font-mono text-foreground/85">{traceID}</dd>
                    </>
                  )}
                  {entry.mission_id && (
                    <>
                      <dt className="font-mono text-[10.5px] text-muted-foreground-soft">issue</dt>
                      <dd className="m-0 truncate font-mono text-foreground/85">{entry.mission_id}</dd>
                    </>
                  )}
                  <dt className="font-mono text-[10.5px] text-muted-foreground-soft">actor</dt>
                  <dd className="m-0 truncate text-foreground/85">
                    {entry.actor_type}
                    {entry.actor_id ? ` · ${entry.actor_id}` : ""}
                  </dd>
                </dl>
              </div>
              {entry.payload && Object.keys(entry.payload).length > 0 && (
                <div className="flex flex-col gap-1">
                  <FieldLabel>Payload</FieldLabel>
                  <pre className="max-h-72 overflow-auto rounded border border-white/[0.06] bg-background p-2 font-mono text-[10.5px] text-muted-foreground">
                    {JSON.stringify(entry.payload, null, 2)}
                  </pre>
                </div>
              )}
            </div>
          </DashboardCard>
        </Appear>
      ) : null}

      <Appear order={6}>
        <div className="flex justify-center">
          <Button size="sm" variant="outline" onClick={onBack}>
            Back to activity
          </Button>
        </div>
      </Appear>
    </div>
  )
}
