"use client"

import { useState } from "react"
import {
  Calendar,
  ChevronDown,
  ChevronRight,
  CircleDot,
  Clock,
  Loader2,
  PauseCircle,
  Webhook,
  Zap,
} from "lucide-react"
import { cn } from "@/lib/utils"
import { relTime } from "@/lib/activity/format-time"
import { statusIcon, statusTint } from "@/lib/activity/run-status"
import type { RunGroup } from "@/lib/activity/run-filters"
import type { PipelineRun } from "@/hooks/use-pipeline-runs"
import { RoutinePreviewCard } from "./routine-preview-card"

// RunGroupTree — renders the grouped run list as a collapsible tree.
// Each top-level group can have either subgroups OR runs (never both,
// by construction in groupRuns()), and leaf rows are paginated
// "Show N more" so a 1000-run cron stream stays scrollable.

const PAGE_SIZE = 8 // runs per group before we fold into "Show more"

interface RunGroupTreeProps {
  groups: RunGroup[]
  selectedRunId: string | null
  onSelectRun: (id: string) => void
  onSelectIssue?: (identifier: string) => void
  // Workspace-level metadata used for the routine hover card.
  routineCardCtx?: {
    crewNameByPipelineSlug: Map<string, string>
    cronExprByPipelineSlug: Map<string, string>
    runsByPipelineSlug: Map<string, PipelineRun[]>
  }
}

export function RunGroupTree({
  groups,
  selectedRunId,
  onSelectRun,
  onSelectIssue,
  routineCardCtx,
}: RunGroupTreeProps) {
  if (groups.length === 0) {
    return (
      <div className="flex h-32 flex-col items-center justify-center gap-2 text-center">
        <Clock className="h-6 w-6 text-muted-foreground/30" />
        <div className="text-xs text-muted-foreground/60">No runs match the current filters.</div>
      </div>
    )
  }
  return (
    <ul className="text-xs">
      {groups.map((g) => (
        <GroupNode
          key={g.key}
          group={g}
          depth={0}
          selectedRunId={selectedRunId}
          onSelectRun={onSelectRun}
          onSelectIssue={onSelectIssue}
          routineCardCtx={routineCardCtx}
        />
      ))}
    </ul>
  )
}

function GroupNode({
  group,
  depth,
  selectedRunId,
  onSelectRun,
  onSelectIssue,
  routineCardCtx,
}: {
  group: RunGroup
  depth: number
  selectedRunId: string | null
  onSelectRun: (id: string) => void
  onSelectIssue?: (identifier: string) => void
  routineCardCtx?: RunGroupTreeProps["routineCardCtx"]
}) {
  const [expanded, setExpanded] = useState(depth === 0)
  const [shownPage, setShownPage] = useState(1)

  const StatusIcon = statusIconFor(group)
  const tint = tintFor(group)
  const accentBorder = ACCENT_BY_KIND[group.kind] ?? "border-white/[0.10]"

  // Header row — clickable to expand/collapse. We render a routine
  // preview card on hover only when the group has a routine slug,
  // since cards built from issue/crew metadata wouldn't carry the
  // schedule/cost rollup the user actually wants to see.
  const headerInner = (
    <button
      type="button"
      onClick={() => setExpanded((v) => !v)}
      aria-expanded={expanded}
      className={cn(
        "flex w-full items-center gap-1.5 px-2 py-1.5 text-left transition-colors hover:bg-white/[0.025]",
        depth === 0 && "border-b border-white/[0.04]",
      )}
    >
      {expanded ? (
        <ChevronDown className="h-3 w-3 shrink-0 text-muted-foreground/60" />
      ) : (
        <ChevronRight className="h-3 w-3 shrink-0 text-muted-foreground/60" />
      )}
      {depth === 0 ? (
        <KindBadge kind={group.kind} />
      ) : (
        <span className={cn("h-1.5 w-1.5 shrink-0 rounded-full", tint.dot)} />
      )}
      <span className="min-w-0 flex-1 truncate">
        {depth === 0 ? (
          <span className="text-muted-foreground/80">{group.label}</span>
        ) : (
          <span className="font-medium">{group.label}</span>
        )}
        {group.metadata?.cronExpr && (
          <span className="ml-1.5 font-mono text-[10px] text-muted-foreground/60">
            {humanCron(group.metadata.cronExpr)}
          </span>
        )}
        {group.metadata?.issueIdentifier && (
          <span className="ml-1.5 inline-flex items-center gap-0.5 rounded bg-blue-500/15 px-1 py-0 text-[9px] font-medium text-blue-300">
            <CircleDot className="h-2 w-2" />
            {group.metadata.issueIdentifier}
          </span>
        )}
      </span>
      {StatusIcon && (
        <StatusIcon
          className={cn("h-3 w-3 shrink-0", tint.icon)}
          aria-hidden="true"
        />
      )}
      <span className="shrink-0 rounded bg-white/[0.06] px-1 py-0 text-[9px] tabular-nums text-muted-foreground">
        {group.totalRuns}
      </span>
    </button>
  )

  const header =
    group.kind === "routine" && group.metadata?.routineSlug && routineCardCtx ? (
      <RoutinePreviewCard
        slug={group.metadata.routineSlug}
        name={group.metadata.routineName ?? group.label}
        crewName={routineCardCtx.crewNameByPipelineSlug.get(group.metadata.routineSlug)}
        cronExpr={routineCardCtx.cronExprByPipelineSlug.get(group.metadata.routineSlug) ?? group.metadata.cronExpr}
        runs={routineCardCtx.runsByPipelineSlug.get(group.metadata.routineSlug) ?? []}
      >
        {headerInner}
      </RoutinePreviewCard>
    ) : (
      headerInner
    )

  return (
    <li>
      {header}

      {expanded && (
        <div
          className={cn(
            depth === 0 ? "" : "ml-3.5 border-l-2 pl-1",
            depth === 0 ? "" : accentBorder,
          )}
        >
          {/* Subgroups (recurse) */}
          {group.subgroups && group.subgroups.length > 0 && (
            <ul>
              {group.subgroups.map((sg) => (
                <GroupNode
                  key={sg.key}
                  group={sg}
                  depth={depth + 1}
                  selectedRunId={selectedRunId}
                  onSelectRun={onSelectRun}
                  onSelectIssue={onSelectIssue}
                  routineCardCtx={routineCardCtx}
                />
              ))}
            </ul>
          )}

          {/* Leaf runs */}
          {group.runs && group.runs.length > 0 && (
            <RunRows
              runs={group.runs}
              shownPage={shownPage}
              onShowMore={() => setShownPage((p) => p + 1)}
              selectedRunId={selectedRunId}
              onSelect={onSelectRun}
              onSelectIssue={onSelectIssue}
              accentBorder={accentBorder}
            />
          )}
        </div>
      )}
    </li>
  )
}

function RunRows({
  runs,
  shownPage,
  onShowMore,
  selectedRunId,
  onSelect,
  accentBorder,
}: {
  runs: PipelineRun[]
  shownPage: number
  onShowMore: () => void
  selectedRunId: string | null
  onSelect: (id: string) => void
  onSelectIssue?: (id: string) => void
  accentBorder: string
}) {
  const visibleCount = Math.min(runs.length, shownPage * PAGE_SIZE)
  const visible = runs.slice(0, visibleCount)
  const hidden = runs.length - visibleCount
  return (
    <ul>
      {visible.map((r) => (
        <RunRow
          key={r.id}
          run={r}
          selected={selectedRunId === r.id}
          onSelect={() => onSelect(r.id)}
          accentBorder={accentBorder}
        />
      ))}
      {hidden > 0 && (
        <li className={cn("border-l-0 px-2 py-1 text-[10px]", accentBorder)}>
          <button
            type="button"
            onClick={onShowMore}
            className="text-muted-foreground/70 hover:text-foreground"
          >
            Show {Math.min(hidden, PAGE_SIZE)} more {hidden === 1 ? "run" : "runs"}
            <span className="ml-1 text-muted-foreground/50">({hidden} hidden)</span>
          </button>
        </li>
      )}
    </ul>
  )
}

function RunRow({
  run,
  selected,
  onSelect,
  accentBorder,
}: {
  run: PipelineRun
  selected: boolean
  onSelect: () => void
  accentBorder: string
}) {
  const tint = statusTint(run.status)
  const StatusIcon = statusIcon(run.status)
  const isWait = run.status === "paused"

  return (
    <li>
      <button
        type="button"
        onClick={onSelect}
        aria-current={selected ? "true" : undefined}
        className={cn(
          "flex w-full items-center gap-1.5 px-2 py-1 text-left transition-colors hover:bg-white/[0.025]",
          accentBorder,
          selected && "bg-blue-500/10",
        )}
      >
        <span className={cn("h-4 w-4 shrink-0 rounded-full p-0.5", tint.bg)}>
          <StatusIcon className={cn("h-full w-full", tint.icon)} aria-hidden="true" />
        </span>
        <span className="min-w-0 flex-1 truncate">
          <span className="font-mono text-[10px]">{shortId(run.id)}</span>
          {isWait && (
            <span className="ml-1.5 text-[10px] text-amber-300">awaiting approval</span>
          )}
        </span>
        <span className="shrink-0 text-[10px] text-muted-foreground/50">
          {relTime(run.started_at)}
        </span>
      </button>
    </li>
  )
}

// ── helpers ────────────────────────────────────────────────────────

function statusIconFor(group: RunGroup) {
  switch (group.status) {
    case "running":
      return Loader2
    case "paused":
      return PauseCircle
    case "completed":
      return null
    case "failed":
      return null
    default:
      return null
  }
}

function tintFor(group: RunGroup) {
  switch (group.status) {
    case "running":
      return { dot: "bg-blue-400", icon: "animate-spin text-blue-400" }
    case "paused":
      return { dot: "bg-amber-400 animate-pulse", icon: "text-amber-400" }
    case "failed":
      return { dot: "bg-rose-400", icon: "text-rose-400" }
    case "completed":
      return { dot: "bg-emerald-400", icon: "text-emerald-400" }
    default:
      return { dot: "bg-white/30", icon: "text-muted-foreground" }
  }
}

const ACCENT_BY_KIND: Record<RunGroup["kind"], string> = {
  cron: "border-violet-500/30",
  issue: "border-blue-500/30",
  webhook: "border-amber-500/30",
  manual: "border-white/[0.10]",
  call_pipeline: "border-purple-500/30",
  crew: "border-cyan-500/30",
  routine: "border-violet-500/30",
  all: "border-white/[0.06]",
}

function KindBadge({ kind }: { kind: RunGroup["kind"] }) {
  const map: Record<RunGroup["kind"], { Icon: typeof Calendar; cls: string; text: string }> = {
    cron: { Icon: Calendar, cls: "bg-violet-500/15 text-violet-300", text: "Cron" },
    issue: { Icon: CircleDot, cls: "bg-blue-500/15 text-blue-300", text: "Issue" },
    webhook: { Icon: Webhook, cls: "bg-amber-500/15 text-amber-300", text: "Webhook" },
    manual: { Icon: Zap, cls: "bg-white/[0.06] text-muted-foreground", text: "Manual" },
    call_pipeline: { Icon: CircleDot, cls: "bg-purple-500/15 text-purple-300", text: "Sub-routine" },
    crew: { Icon: CircleDot, cls: "bg-cyan-500/15 text-cyan-300", text: "Crew" },
    routine: { Icon: CircleDot, cls: "bg-violet-500/15 text-violet-300", text: "Routine" },
    all: { Icon: CircleDot, cls: "bg-white/[0.06] text-muted-foreground", text: "All" },
  }
  const m = map[kind]
  return (
    <span className={cn("inline-flex items-center gap-0.5 rounded px-1 py-0 text-[9px] font-medium", m.cls)}>
      <m.Icon className="h-2 w-2" /> {m.text}
    </span>
  )
}

function shortId(id: string): string {
  // prn_8a3c7e9b4f → prn_8a3c
  if (id.length > 12 && id.startsWith("prn_")) return id.slice(0, 8)
  return id
}

// humanCron — turn a cron expression into a short label like "every
// 1h", "daily 9:00", "every 5m". Falls back to the raw expression
// when the pattern doesn't match a known shortcut. We don't ship a
// full cron parser — these five patterns cover ~90% of what shows
// up in real schedules.
function humanCron(expr: string): string {
  const e = expr.trim()
  if (e === "* * * * *") return "every minute"
  if (/^\*\/(\d+) \* \* \* \*$/.test(e)) {
    const m = e.match(/^\*\/(\d+) \* \* \* \*$/)!
    return `every ${m[1]}m`
  }
  if (/^\d+ \*\/(\d+) \* \* \*$/.test(e)) {
    const m = e.match(/^\d+ \*\/(\d+) \* \* \*$/)!
    return `every ${m[1]}h`
  }
  if (/^\d+ \d+ \* \* \*$/.test(e)) {
    const [m, h] = e.split(" ")
    return `daily ${h}:${m.padStart(2, "0")}`
  }
  if (/^\d+ \d+ \* \* [0-7]$/.test(e)) return `weekly`
  return e
}
