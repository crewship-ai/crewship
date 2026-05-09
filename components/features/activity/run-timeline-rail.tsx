"use client"

import { useMemo, useState } from "react"
import {
  Calendar,
  Check,
  CircleDot,
  Clock,
  Loader2,
  PauseCircle,
  Search,
  ScrollText,
  Webhook,
  X,
  XCircle,
} from "lucide-react"
import { cn } from "@/lib/utils"
import type { PipelineRun } from "@/hooks/use-pipeline-runs"

// RunTimelineRail — left-side list of recent runs. Each row is one
// pipeline_run with its source pill (issue / schedule / webhook /
// manual), pipeline name, status pip, and started-at relative time.
// Selecting a row drives the canvas into trace mode for that run.
//
// Replaces the default `RunsView` table in /activity. The expandable
// step-tree per row is gone here on purpose — the canvas IS the step
// view now, no need to render it twice.
interface RunTimelineRailProps {
  runs: PipelineRun[]
  selectedRunId: string | null
  onSelect: (runId: string) => void
  loading?: boolean
  error?: string | null
}

type StatusFilter = "all" | "active" | "completed" | "failed"

export function RunTimelineRail({
  runs,
  selectedRunId,
  onSelect,
  loading,
  error,
}: RunTimelineRailProps) {
  const [filter, setFilter] = useState<StatusFilter>("all")
  const [search, setSearch] = useState("")

  const counts = useMemo(() => {
    const active = runs.filter(
      (r) => r.status === "running" || r.status === "queued" || r.status === "paused",
    ).length
    const completed = runs.filter((r) => r.status === "completed").length
    const failed = runs.filter(
      (r) => r.status === "failed" || r.status === "cancelled" || r.status === "interrupted",
    ).length
    return { active, completed, failed, total: runs.length }
  }, [runs])

  const filtered = useMemo(() => {
    let list = runs
    if (filter === "active") {
      list = list.filter(
        (r) => r.status === "running" || r.status === "queued" || r.status === "paused",
      )
    } else if (filter === "completed") {
      list = list.filter((r) => r.status === "completed")
    } else if (filter === "failed") {
      list = list.filter(
        (r) => r.status === "failed" || r.status === "cancelled" || r.status === "interrupted",
      )
    }
    if (search.trim()) {
      const q = search.toLowerCase()
      list = list.filter(
        (r) =>
          (r.pipeline_name || "").toLowerCase().includes(q) ||
          r.pipeline_slug.toLowerCase().includes(q) ||
          (r.issue_identifier || "").toLowerCase().includes(q) ||
          r.id.toLowerCase().includes(q),
      )
    }
    return list
  }, [runs, filter, search])

  return (
    <div className="flex h-full flex-col bg-card">
      {/* Search + filter row */}
      <div className="shrink-0 border-b border-white/[0.06] p-2">
        <div className="relative mb-2">
          <Search className="pointer-events-none absolute left-2 top-1/2 h-3 w-3 -translate-y-1/2 text-muted-foreground/50" />
          <input
            type="text"
            value={search}
            onChange={(e) => setSearch(e.target.value)}
            placeholder="Search runs"
            aria-label="Search runs by pipeline name, slug, or issue"
            className="h-7 w-full rounded border border-white/[0.06] bg-background pl-7 pr-7 text-xs text-foreground placeholder:text-muted-foreground/50 focus:border-blue-500/50 focus:outline-none"
          />
          {search && (
            <button
              type="button"
              onClick={() => setSearch("")}
              aria-label="Clear search"
              className="absolute right-1 top-1/2 -translate-y-1/2 rounded p-0.5 text-muted-foreground/50 hover:text-foreground"
            >
              <X className="h-3 w-3" />
            </button>
          )}
        </div>
        <div className="flex items-center gap-1">
          <FilterBtn label="All" count={counts.total} active={filter === "all"} onClick={() => setFilter("all")} />
          <FilterBtn label="Active" count={counts.active} active={filter === "active"} onClick={() => setFilter("active")} />
          <FilterBtn label="Done" count={counts.completed} active={filter === "completed"} onClick={() => setFilter("completed")} />
          <FilterBtn label="Failed" count={counts.failed} active={filter === "failed"} onClick={() => setFilter("failed")} />
        </div>
      </div>

      {/* Run list */}
      <div className="min-h-0 flex-1 overflow-y-auto">
        {loading && runs.length === 0 ? (
          <div className="flex h-full items-center justify-center text-xs text-muted-foreground">
            <Loader2 className="mr-2 h-3 w-3 animate-spin" /> Loading runs…
          </div>
        ) : error ? (
          <div className="p-3 text-xs text-rose-300">Runs unavailable: {error}</div>
        ) : filtered.length === 0 ? (
          <EmptyRail filter={filter} hasSearch={search.length > 0} />
        ) : (
          <ul className="divide-y divide-white/[0.04]">
            {filtered.map((run) => (
              <RunRailItem
                key={run.id}
                run={run}
                selected={selectedRunId === run.id}
                onSelect={() => onSelect(run.id)}
              />
            ))}
          </ul>
        )}
      </div>
    </div>
  )
}

function FilterBtn({
  label,
  count,
  active,
  onClick,
}: {
  label: string
  count: number
  active: boolean
  onClick: () => void
}) {
  return (
    <button
      type="button"
      onClick={onClick}
      className={cn(
        "flex items-center gap-1 rounded px-1.5 py-0.5 text-[10px] transition-colors",
        active
          ? "bg-blue-500/15 text-blue-300"
          : "text-muted-foreground/70 hover:text-foreground/80",
      )}
    >
      <span>{label}</span>
      <span
        className={cn(
          "rounded px-1 py-0 text-[9px] tabular-nums",
          active ? "bg-blue-500/20 text-blue-200" : "bg-white/[0.06] text-foreground/40",
        )}
      >
        {count}
      </span>
    </button>
  )
}

function RunRailItem({
  run,
  selected,
  onSelect,
}: {
  run: PipelineRun
  selected: boolean
  onSelect: () => void
}) {
  const StatusIcon = statusIcon(run.status)
  const tint = statusTint(run.status)

  return (
    <li>
      <button
        type="button"
        onClick={onSelect}
        aria-current={selected ? "true" : undefined}
        className={cn(
          "flex w-full items-start gap-2 px-3 py-2 text-left transition-colors",
          selected ? "bg-blue-500/10" : "hover:bg-white/[0.03]",
        )}
      >
        <span
          className={cn(
            "mt-0.5 flex h-5 w-5 shrink-0 items-center justify-center rounded-full",
            tint.bg,
          )}
        >
          <StatusIcon className={cn("h-3 w-3", tint.icon)} />
        </span>
        <div className="min-w-0 flex-1">
          <div className="flex items-center gap-1.5">
            <span className="truncate text-xs font-medium">
              {run.pipeline_name || run.pipeline_slug}
            </span>
            <SourcePill run={run} />
          </div>
          <div className="mt-0.5 flex items-center gap-1.5 text-[10px] text-muted-foreground/60">
            <span className="truncate font-mono">{run.id}</span>
            <span>·</span>
            <span className="shrink-0">{relTime(run.started_at)}</span>
          </div>
        </div>
      </button>
    </li>
  )
}

function SourcePill({ run }: { run: PipelineRun }) {
  if (run.triggered_via === "issue" && run.issue_identifier) {
    return (
      <span className="inline-flex shrink-0 items-center gap-0.5 rounded bg-blue-500/15 px-1 py-0 text-[9px] font-medium text-blue-300">
        <CircleDot className="h-2 w-2" />
        {run.issue_identifier}
      </span>
    )
  }
  if (run.triggered_via === "schedule") {
    return (
      <span className="inline-flex shrink-0 items-center gap-0.5 rounded bg-violet-500/15 px-1 py-0 text-[9px] font-medium text-violet-300">
        <Calendar className="h-2 w-2" />
        cron
      </span>
    )
  }
  if (run.triggered_via === "webhook") {
    return (
      <span className="inline-flex shrink-0 items-center gap-0.5 rounded bg-amber-500/15 px-1 py-0 text-[9px] font-medium text-amber-300">
        <Webhook className="h-2 w-2" />
        hook
      </span>
    )
  }
  if (run.triggered_via === "call_pipeline") {
    return (
      <span className="shrink-0 rounded bg-white/[0.08] px-1 py-0 text-[9px] font-medium text-muted-foreground">
        sub
      </span>
    )
  }
  return null
}

function EmptyRail({ filter, hasSearch }: { filter: StatusFilter; hasSearch: boolean }) {
  if (hasSearch) {
    return (
      <div className="flex flex-col items-center justify-center gap-2 p-6 text-center">
        <Search className="h-6 w-6 text-muted-foreground/30" />
        <div className="text-xs text-muted-foreground">No matches</div>
      </div>
    )
  }
  const messages: Record<StatusFilter, string> = {
    active: "No routines running.",
    all: "No runs in the workspace yet.",
    completed: "No completed runs yet.",
    failed: "No failed runs.",
  }
  return (
    <div className="flex flex-col items-center justify-center gap-2 p-6 text-center">
      <ScrollText className="h-6 w-6 text-muted-foreground/30" />
      <div className="text-xs text-muted-foreground">{messages[filter]}</div>
    </div>
  )
}

// ── helpers (mirror runs-view.tsx, kept local to avoid circular imports) ─

function statusIcon(status: string) {
  switch (status) {
    case "running":
    case "queued":
      return Loader2
    case "paused":
      return PauseCircle
    case "completed":
      return Check
    case "failed":
    case "cancelled":
    case "interrupted":
      return XCircle
    default:
      return Clock
  }
}

function statusTint(status: string) {
  switch (status) {
    case "running":
    case "queued":
      return { bg: "bg-blue-500/15", icon: "animate-spin text-blue-400" }
    case "paused":
      return { bg: "bg-amber-500/15", icon: "text-amber-400 animate-pulse" }
    case "completed":
      return { bg: "bg-emerald-500/15", icon: "text-emerald-400" }
    case "failed":
    case "cancelled":
    case "interrupted":
      return { bg: "bg-rose-500/15", icon: "text-rose-400" }
    default:
      return { bg: "bg-white/[0.06]", icon: "text-muted-foreground" }
  }
}

function relTime(iso?: string) {
  if (!iso) return "—"
  const d = new Date(iso)
  if (Number.isNaN(d.getTime())) return "—"
  const diff = Date.now() - d.getTime()
  if (Math.abs(diff) < 60_000) return "just now"
  const mins = Math.round(Math.abs(diff) / 60_000)
  if (mins < 60) return `${mins}m ago`
  const hrs = Math.round(mins / 60)
  if (hrs < 24) return `${hrs}h ago`
  return `${Math.round(hrs / 24)}d ago`
}
