"use client"

// One run, opened from the Activity rail (#2979).
//
// The rail row used to open a chain page: a "workflow" chip, the chain's raw
// id, a Depth counter, a graph and a list of run ids to click again before
// anything about the run itself showed. The page a person wants is the run:
// did it work, why not, what it did step by step, what it caused, and what to
// do next. So a row whose chain starts at a routine run opens THIS — the
// approved design's detail, built from the parts /routines already uses so a
// run reads the same on both pages:
//
//   header       name, status in the rail's words, trigger, when · Retry / Stop / Open routine
//   banner       the approval (same item as Inbox) or why it failed
//   facts        started · duration · cost · steps · trigger
//   steps        RoutineStepSpine — attempts, for-each items and agent work inside steps
//   caused       the work further down the chain: sub-runs, agent work, inbox asks
//   side column  linked to · what it changed · previous runs · raw events (admins)
//
// A run id that is not a routine run (agent work, deep links) falls back to
// TypedRunDetail, which already knows those records.

import * as React from "react"
import Link from "next/link"
import { toast } from "sonner"
import {
  Bot,
  ChevronRight,
  CircleDot,
  ExternalLink,
  GitBranch,
  History,
  Inbox,
  Link2,
  RotateCcw,
  ScrollText,
  Sparkles,
  Square,
  type LucideIcon,
} from "lucide-react"

import { DashboardCard } from "@/components/features/dashboard/dashboard-card"
import { TypedRunDetail } from "@/components/features/activity/typed-run-detail"
import { RoutineApprovalBanner } from "@/components/features/routines/routine-approval-banner"
import { RoutineResultContent } from "@/components/features/routines/routine-result-content"
import { RoutineStepSpine } from "@/components/features/routines/routine-step-spine"
import { routineViewHref } from "@/components/features/routines/routine-navigation"
import { Button } from "@/components/ui/button"
import { Appear, StatStrip } from "@/components/ui/detail"
import { Spinner } from "@/components/ui/spinner"
import { useAbilities } from "@/hooks/use-abilities"
import type { ChainSummary } from "@/hooks/use-chains"
import { usePendingApproval } from "@/hooks/use-pending-approval"
import { usePipelineRunRecords } from "@/hooks/use-pipeline-run-records"
import { useRunExecutions } from "@/hooks/use-run-executions"
import { useTrace } from "@/hooks/use-trace"
import { formatDurationMs } from "@/lib/activity-stream"
import {
  RUN_TONE_CLASS,
  RUN_TONE_DOT,
  RUN_TONE_LABEL,
  chainBranches,
  historyStrip,
  isActiveRun,
  linkedEntities,
  runActions,
  runStatusLabel,
  runTone,
  stepProgress,
  triggerPhrase,
  type BranchGraph,
  type LinkedEntity,
  type RunTreeNode,
} from "@/lib/activity-run"
import { apiFetch } from "@/lib/api-fetch"
import { roleAtLeast } from "@/lib/routine-governance"
import { routineRunBanner } from "@/lib/routine-run-presentation"
import { relTime } from "@/lib/time"
import { cn } from "@/lib/utils"

export interface ActivityRunPageProps {
  workspaceId: string
  runId: string
  /** The rail row that led here, when there was one — its issues and agents. */
  chain?: ChainSummary
  routineName?: string
  onOpenNode: (kind: string, ref: string) => void
}

export function ActivityRunPage(props: ActivityRunPageProps) {
  const { workspaceId, runId } = props
  const trace = useTrace(workspaceId, runId)
  // Not a routine run: agent work, or a record only the journal knows. The
  // typed detail probes /runs and says honestly what it found.
  if (trace.error && !trace.run) {
    return <TypedRunDetail key={`${workspaceId}:${runId}`} workspaceId={workspaceId} runId={runId} />
  }
  if (!trace.run) {
    return (
      <div role="status" className="flex items-center justify-center gap-2 py-20 text-xs text-muted-foreground">
        <Spinner className="h-3.5 w-3.5" /> Loading run…
      </div>
    )
  }
  return <RunPage {...props} trace={trace as LoadedTrace} />
}

type LoadedTrace = ReturnType<typeof useTrace> & { run: NonNullable<ReturnType<typeof useTrace>["run"]> }

function RunPage({
  workspaceId,
  runId,
  chain,
  routineName,
  onOpenNode,
  trace,
}: ActivityRunPageProps & { trace: LoadedTrace }) {
  const { run, dsl, refresh } = trace
  const active = isActiveRun(run.status)
  const tone = runTone(run.status)
  const executions = useRunExecutions(workspaceId, runId, active)
  const approval = usePendingApproval(workspaceId, runId)
  const { role } = useAbilities()
  const actions = runActions(run.status, role)
  const graph = useChainGraph(workspaceId, runId)
  const { records } = usePipelineRunRecords(workspaceId, run.pipeline_slug || null)
  const eventCount = useRunEventCount(workspaceId, runId)
  const [busy, setBusy] = React.useState<"retry" | "stop" | null>(null)

  const name = routineName || run.pipeline_name || run.pipeline_slug || "Routine run"
  const steps = dsl?.steps?.map((s) => ({ id: s.id, name: s.name, type: s.type }))
  const progress = stepProgress(steps, executions.byStep)
  const banner = routineRunBanner({ run, steps })
  const branches = React.useMemo(() => (graph ? chainBranches(graph, runId) : []), [graph, runId])
  const links = linkedEntities({
    routine: run.pipeline_slug ? { slug: run.pipeline_slug, name } : null,
    issues: chain?.issues,
    graphIssues: graph?.nodes.filter((n) => n.kind === "issue").map((n) => ({ ref: n.ref, label: n.label })),
    agents: chain?.agents,
  })
  // The chain's issues are this run's only when the chain starts here; a run
  // opened from deeper in a chain does not own what its parent touched.
  const changed = chain?.origin === runId ? (chain.issues ?? []) : []
  const history = historyStrip(records, runId)

  async function retry() {
    setBusy("retry")
    try {
      const res = await apiFetch(
        `/api/v1/workspaces/${encodeURIComponent(workspaceId)}/pipelines/runs/${encodeURIComponent(runId)}/replay`,
        { method: "POST", headers: { "Content-Type": "application/json" }, body: "{}" },
      )
      const body = (await res.json().catch(() => ({}))) as { run_id?: string; error?: string }
      if (!res.ok) throw new Error(body.error || `HTTP ${res.status}`)
      toast.success("Started again with the same inputs")
      if (body.run_id) onOpenNode("run", body.run_id)
    } catch (e) {
      toast.error(`Could not start it again: ${e instanceof Error ? e.message : "unknown error"}`)
    } finally {
      setBusy(null)
    }
  }

  async function stop() {
    setBusy("stop")
    try {
      const res = await apiFetch(
        `/api/v1/workspaces/${encodeURIComponent(workspaceId)}/pipelines/runs/${encodeURIComponent(runId)}/cancel`,
        { method: "POST" },
      )
      if (!res.ok) throw new Error(`HTTP ${res.status}`)
      toast.success("Stopping — the run halts before its next step")
      void refresh()
    } catch (e) {
      toast.error(`Could not stop it: ${e instanceof Error ? e.message : "unknown error"}`)
    } finally {
      setBusy(null)
    }
  }

  const spineRecord = {
    lookup: (stepId: string) => ({
      execution: executions.byStep.get(stepId),
      hasOutput: Object.hasOwn(run.step_outputs ?? {}, stepId),
      output: run.step_outputs?.[stepId],
    }),
    outputsAvailable: run.step_outputs_available !== false,
    subSpans: run.sub_spans as Record<string, unknown> | undefined,
    currentStepId: run.current_step_id,
    failedStepId: run.failed_at_step,
    active,
    executionsError: executions.error,
    executionsTruncated: executions.truncated,
    onRetryExecutions: executions.refresh,
    onLoadMoreExecutions: executions.loadMore,
  }

  const showBanner = !approval.waitpoint && (tone === "failed" || tone === "stopped" || tone === "waiting")

  return (
    <div className="mx-auto flex max-w-[1800px] flex-col gap-4 p-4 md:p-6">
      {/* ── Header ── */}
      <Appear order={0}>
        <header className="flex flex-wrap items-start gap-3">
          <div className="flex min-w-0 flex-1 flex-col gap-1.5">
            <div className="flex flex-wrap items-center gap-2">
              <span className="inline-flex items-center gap-1 rounded border border-foreground/[0.08] px-1.5 py-px font-mono text-[10px] uppercase tracking-wider text-muted-foreground">
                <ScrollText className="h-3 w-3" /> Routine run
              </span>
              <span className={cn("inline-flex items-center gap-1.5 text-xs font-medium", RUN_TONE_CLASS[tone])}>
                <span className="relative inline-flex h-2 w-2">
                  {tone === "running" && (
                    <span className={cn("absolute inset-0 animate-ping rounded-full opacity-60", RUN_TONE_DOT[tone])} />
                  )}
                  <span className={cn("relative h-2 w-2 rounded-full", RUN_TONE_DOT[tone])} />
                </span>
                {runStatusLabel(run.status)}
              </span>
            </div>
            <h1 className="min-w-0 truncate text-lg font-semibold tracking-tight">{name}</h1>
            <p className="flex flex-wrap items-center gap-x-2 gap-y-0.5 text-xs text-muted-foreground">
              <span>{triggerPhrase(run)}</span>
              <span aria-hidden>·</span>
              <span title={run.started_at ? new Date(run.started_at).toLocaleString() : undefined}>
                {run.started_at ? relTime(run.started_at) : "not started"}
              </span>
              {run.issue_identifier && (
                <>
                  <span aria-hidden>·</span>
                  <span className="font-mono">{run.issue_identifier}</span>
                </>
              )}
              <span aria-hidden>·</span>
              <span className="font-mono text-muted-foreground-soft">{runId}</span>
            </p>
          </div>
          <div className="flex shrink-0 flex-wrap items-center gap-2">
            {actions.retry && (
              <Button size="sm" variant="outline" disabled={busy !== null} onClick={() => void retry()}>
                <RotateCcw className="mr-1.5 h-3.5 w-3.5" />
                {tone === "failed" ? "Retry" : "Run again"}
              </Button>
            )}
            {actions.stop && (
              <Button size="sm" variant="outline" disabled={busy !== null} onClick={() => void stop()}>
                <Square className="mr-1.5 h-3.5 w-3.5" /> Stop
              </Button>
            )}
            {run.pipeline_slug && (
              <Button size="sm" variant="ghost" asChild>
                <Link href={routineViewHref(run.pipeline_slug, "definition")}>
                  Open routine <ExternalLink className="ml-1.5 h-3.5 w-3.5" />
                </Link>
              </Button>
            )}
          </div>
        </header>
      </Appear>

      {/* ── Banner: the decision, or why it stopped ── */}
      {approval.waitpoint && (
        <Appear order={1}>
          <RoutineApprovalBanner
            waitpoint={approval.waitpoint}
            deciding={approval.deciding}
            onDecide={async (approved, comment, answer) => {
              const ok = await approval.decide(approved, comment, answer)
              if (ok) void refresh()
              return ok
            }}
          />
        </Appear>
      )}
      {showBanner && banner.title && (
        <Appear order={1}>
          <section
            aria-label={tone === "failed" ? "Why it failed" : "Where it stands"}
            className={cn(
              "rounded-card border px-4 py-3",
              tone === "failed" ? "border-destructive/40 bg-destructive/[0.06]" : "border-warn/40 bg-warn/[0.06]",
            )}
          >
            <p className={cn("eyebrow", tone === "failed" ? "text-destructive" : "text-warn")}>
              {tone === "failed" ? "Why it failed" : banner.title}
            </p>
            {tone === "failed" && <p className="mt-1 text-sm font-medium text-foreground">{banner.title}</p>}
            {banner.detail && <p className="mt-1 text-sm text-foreground/85">{banner.detail}</p>}
            {(banner.kept || banner.notDone) && (
              <p className="mt-1.5 text-xs text-muted-foreground">
                {[banner.kept, banner.notDone].filter(Boolean).join(" · ")}
              </p>
            )}
          </section>
        </Appear>
      )}

      {/* ── Facts ── */}
      <Appear order={2}>
        <StatStrip
          items={[
            {
              label: "Started",
              value: run.started_at
                ? new Date(run.started_at).toLocaleString(undefined, { hour12: false, dateStyle: "short", timeStyle: "short" })
                : "—",
            },
            {
              label: "Duration",
              value: run.duration_ms > 0 ? formatDurationMs(run.duration_ms) : active ? "running…" : "—",
              mono: true,
            },
            { label: "Cost", value: typeof run.cost_usd === "number" ? `$${run.cost_usd.toFixed(2)}` : "—", mono: true },
            {
              label: "Steps",
              value: progress ? `${progress.done} of ${progress.total}` : "—",
              tone: tone === "failed" ? "destructive" : "default",
            },
            { label: "Trigger", value: triggerPhrase(run) },
          ]}
        />
      </Appear>

      <div className="grid grid-cols-1 gap-4 xl:grid-cols-[minmax(0,1fr)_340px]">
        {/* ── Left: what it did ── */}
        <div className="flex min-w-0 flex-col gap-4">
          <Appear order={3}>
            {dsl ? (
              <RoutineStepSpine
                behavior={run.behavior}
                workspaceId={workspaceId}
                definition={dsl}
                record={spineRecord}
                initialLimit={12}
                title="Steps"
              />
            ) : (
              <p className="rounded-card border border-border bg-card p-4 text-sm text-muted-foreground">
                The recipe this run executed is no longer available. Its result and events remain.
              </p>
            )}
          </Appear>

          {branches.length > 0 && (
            <Appear order={4}>
              <DashboardCard
                role="region"
                aria-label="Caused by this run"
                title="Caused by this run"
                icon={GitBranch}
                hint={`${countTree(branches)} · sub-runs, agent work, asks`}
              >
                <ul className="flex flex-col">
                  {branches.map((n) => (
                    <BranchRow key={n.id} node={n} depth={0} onOpenNode={onOpenNode} />
                  ))}
                </ul>
              </DashboardCard>
            </Appear>
          )}

          {!active && run.output != null && run.output !== "" && (
            <Appear order={5}>
              <DashboardCard title="Result" icon={Sparkles}>
                <div className="max-h-[50dvh] overflow-auto break-words">
                  <RoutineResultContent output={run.output} />
                </div>
              </DashboardCard>
            </Appear>
          )}
        </div>

        {/* ── Right: where it belongs ── */}
        <div className="flex min-w-0 flex-col gap-4">
          <Appear order={3}>
            <DashboardCard role="region" aria-label="Linked to" title="Linked to" icon={Link2}>
              {links.length === 0 ? (
                <p className="text-xs text-muted-foreground">Nothing else is linked to this run.</p>
              ) : (
                <div className="flex flex-wrap gap-1.5">
                  {links.map((l) => (
                    <LinkChip key={`${l.kind}:${l.ref}`} link={l} onOpenNode={onOpenNode} />
                  ))}
                </div>
              )}
            </DashboardCard>
          </Appear>

          <Appear order={4}>
            <DashboardCard role="region" aria-label="What it changed" title="What it changed" icon={CircleDot}>
              {changed.length === 0 ? (
                <p className="text-xs text-muted-foreground">
                  {active
                    ? "Nothing yet — changes appear as steps finish."
                    : tone === "failed"
                      ? "No issue was created or changed before it stopped."
                      : "No issue was created or changed by this run."}
                </p>
              ) : (
                <ul className="flex flex-col gap-1">
                  {changed.map((i) => (
                    <li key={i.id}>
                      <button
                        type="button"
                        onClick={() => onOpenNode("issue", i.id)}
                        className="flex w-full items-center gap-2 rounded-md px-1.5 py-1 text-left text-xs transition-colors hover:bg-foreground/[0.04]"
                      >
                        <span className="w-14 shrink-0 text-[10.5px] text-muted-foreground">
                          {i.created ? "Created" : "Touched"}
                        </span>
                        {i.identifier && <span className="font-mono text-[10.5px] text-purple">{i.identifier}</span>}
                        <span className="min-w-0 truncate">{i.title || i.identifier || i.id}</span>
                      </button>
                    </li>
                  ))}
                </ul>
              )}
            </DashboardCard>
          </Appear>

          {history.length > 1 && (
            <Appear order={5}>
              <DashboardCard
                role="region"
                aria-label="Previous runs"
                title="Previous runs"
                icon={History}
                hint={`${history.filter((d) => d.tone === "failed").length} of ${history.length} failed`}
              >
                <div className="flex flex-wrap items-center gap-1">
                  {history.map((d) => (
                    <button
                      key={d.id}
                      type="button"
                      aria-label={`${RUN_TONE_LABEL[d.tone]} run, ${relTime(d.startedAt)}${d.current ? " (this one)" : ""}`}
                      title={`${RUN_TONE_LABEL[d.tone]} · ${relTime(d.startedAt)}`}
                      onClick={() => !d.current && onOpenNode("run", d.id)}
                      className={cn(
                        "h-4 w-2.5 rounded-sm transition-transform hover:-translate-y-0.5",
                        RUN_TONE_DOT[d.tone],
                        d.current ? "ring-2 ring-foreground/70 ring-offset-1 ring-offset-card" : "opacity-80",
                      )}
                    />
                  ))}
                </div>
              </DashboardCard>
            </Appear>
          )}

          {/* The Journal is admin-only (lib/nav-sections), so is the way in. */}
          {roleAtLeast(role, "ADMIN") && (
            <Appear order={6}>
              <Link
                href={`/journal?trace_id=${encodeURIComponent(runId)}`}
                className="inline-flex items-center gap-1.5 self-start rounded-md px-1.5 py-1 font-mono text-[11px] text-muted-foreground transition-colors hover:bg-muted hover:text-foreground"
              >
                {eventCount != null ? `${eventCount} raw events` : "Raw events"} · Journal
                <ExternalLink className="h-3 w-3" />
              </Link>
            </Appear>
          )}
        </div>
      </div>
    </div>
  )
}

/* ------------------------------------------------------------------ *
 *  Pieces
 * ------------------------------------------------------------------ */

const BRANCH_KIND: Record<string, { label: string; icon: LucideIcon }> = {
  run: { label: "Sub-run", icon: ScrollText },
  assignment: { label: "Agent", icon: Bot },
  inbox: { label: "Ask", icon: Inbox },
  automation: { label: "Rule", icon: GitBranch },
}

function countTree(nodes: RunTreeNode[]): number {
  return nodes.reduce((n, c) => n + 1 + countTree(c.children), 0)
}

function BranchRow({
  node,
  depth,
  onOpenNode,
}: {
  node: RunTreeNode
  depth: number
  onOpenNode: (kind: string, ref: string) => void
}) {
  const [open, setOpen] = React.useState(true)
  const kind = BRANCH_KIND[node.kind] ?? { label: node.kind, icon: CircleDot }
  const Icon = kind.icon
  const tone = runTone(node.status)
  return (
    <li>
      <div className="group flex items-center gap-2 rounded-md py-1.5 pr-1.5 transition-colors hover:bg-foreground/[0.03]" style={{ paddingLeft: 6 + depth * 18 }}>
        {node.children.length > 0 ? (
          <button
            type="button"
            aria-label={open ? "Collapse" : "Expand"}
            aria-expanded={open}
            onClick={() => setOpen(!open)}
            className="text-muted-foreground hover:text-foreground"
          >
            <ChevronRight className={cn("h-3.5 w-3.5 transition-transform duration-150", open && "rotate-90")} />
          </button>
        ) : (
          <span className="w-3.5" />
        )}
        <span aria-hidden className={cn("h-1.5 w-1.5 shrink-0 rounded-full", node.status ? RUN_TONE_DOT[tone] : "bg-muted-foreground/40")} />
        <span className="w-14 shrink-0 font-mono text-[10px] uppercase tracking-wide text-muted-foreground-soft">{kind.label}</span>
        <button
          type="button"
          onClick={() => onOpenNode(node.kind, node.ref)}
          className="flex min-w-0 flex-1 items-center gap-1.5 text-left text-xs"
        >
          <Icon className="h-3.5 w-3.5 shrink-0 text-muted-foreground" />
          <span className="truncate group-hover:underline">{node.label}</span>
        </button>
        {node.status && <span className={cn("text-[10.5px]", RUN_TONE_CLASS[tone])}>{runStatusLabel(node.status)}</span>}
        <span className="w-14 text-right font-mono text-[10.5px] tabular-nums text-muted-foreground">
          {node.durationMs != null ? formatDurationMs(node.durationMs) : ""}
        </span>
      </div>
      {open && node.children.length > 0 && (
        <ul>
          {node.children.map((c) => (
            <BranchRow key={c.id} node={c} depth={depth + 1} onOpenNode={onOpenNode} />
          ))}
        </ul>
      )}
    </li>
  )
}

const LINK_STYLE: Record<LinkedEntity["kind"], { label: string; icon: LucideIcon; tone: string }> = {
  routine: { label: "Routine", icon: ScrollText, tone: "text-info" },
  issue: { label: "Issue", icon: CircleDot, tone: "text-purple" },
  agent: { label: "Agent", icon: Bot, tone: "text-primary" },
}

function LinkChip({ link, onOpenNode }: { link: LinkedEntity; onOpenNode: (kind: string, ref: string) => void }) {
  const style = LINK_STYLE[link.kind]
  const Icon = style.icon
  const body = (
    <>
      <Icon className={cn("h-3 w-3 shrink-0", style.tone)} />
      <span className="text-[10px] uppercase tracking-wide text-muted-foreground-soft">{style.label}</span>
      <span className="truncate">{link.label}</span>
    </>
  )
  const cls =
    "inline-flex max-w-full items-center gap-1.5 rounded-md border border-border bg-foreground/[0.02] px-2 py-1 text-xs transition-colors hover:bg-foreground/[0.06]"
  if (link.kind === "routine") {
    return (
      <Link href={routineViewHref(link.ref, "history")} className={cls}>
        {body}
      </Link>
    )
  }
  return (
    <button type="button" onClick={() => onOpenNode(link.kind, link.ref)} className={cls}>
      {body}
    </button>
  )
}

/* ------------------------------------------------------------------ *
 *  Data
 * ------------------------------------------------------------------ */

/** The causal walk around this run — what it caused, and what it belongs to. */
function useChainGraph(workspaceId: string, runId: string): BranchGraph | null {
  const [graph, setGraph] = React.useState<BranchGraph | null>(null)
  React.useEffect(() => {
    const ctrl = new AbortController()
    setGraph(null)
    apiFetch(`/api/v1/chains/${encodeURIComponent(runId)}?workspace_id=${encodeURIComponent(workspaceId)}`, {
      signal: ctrl.signal,
    })
      .then(async (r) => (r.ok ? ((await r.json()) as BranchGraph) : null))
      .then((g) => {
        if (!ctrl.signal.aborted && g) setGraph({ nodes: g.nodes ?? [], edges: g.edges ?? [] })
      })
      .catch(() => {
        /* The page stands without the walk; only "caused" and links thin out. */
      })
    return () => ctrl.abort()
  }, [workspaceId, runId])
  return graph
}

/** How many journal events carry this run — the size of the technical record. */
function useRunEventCount(workspaceId: string, runId: string): number | null {
  const [count, setCount] = React.useState<number | null>(null)
  React.useEffect(() => {
    const ctrl = new AbortController()
    setCount(null)
    apiFetch(
      `/api/v1/journal/count?workspace_id=${encodeURIComponent(workspaceId)}&run_id=${encodeURIComponent(runId)}`,
      { signal: ctrl.signal },
    )
      .then(async (r) => (r.ok ? ((await r.json()) as { total?: number }) : null))
      .then((b) => {
        if (!ctrl.signal.aborted && typeof b?.total === "number") setCount(b.total)
      })
      .catch(() => {})
    return () => ctrl.abort()
  }, [workspaceId, runId])
  return count
}
