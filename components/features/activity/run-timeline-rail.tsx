"use client"

import { useEffect, useMemo, useState } from "react"
import { ScrollText } from "lucide-react"
import { Spinner } from "@/components/ui/spinner"
import { useUserPreference } from "@/hooks/use-user-preference"
import { useWorkspace } from "@/hooks/use-workspace"
import { usePipelines } from "@/hooks/use-pipelines"
import { usePipelineSchedules } from "@/hooks/use-pipeline-schedules"
import type { PipelineRun } from "@/hooks/use-pipeline-runs"
import {
  applyFilters,
  groupRuns,
  type GroupAxis,
  type RunFilter,
  type TriggerSource,
} from "@/lib/activity/run-filters"
import { RailToolbar, type SortAxis } from "./rail/rail-toolbar"
import { RunGroupTree } from "./rail/run-group-tree"
import { SavedViewsButton } from "./rail/saved-views"
import { apiFetch } from "@/lib/api-fetch"

// RunTimelineRail v3 — composed of toolbar + grouped tree. Replaces
// the v2 flat list with a Linear-style filter / sort / group UX.
//
// Data layout:
//   - runs: flat list from usePipelineRuns (parent passes in)
//   - schedules + pipelines + crews: fetched here for filter dropdowns
//     and the routine preview card
//   - filter / sort / group: persisted per-user

interface RunTimelineRailProps {
  runs: PipelineRun[]
  selectedRunId: string | null
  onSelect: (runId: string) => void
  loading?: boolean
  error?: string | null
  // Optional: workspace crews list. If omitted we fetch our own.
  crews?: { id: string; name: string }[]
  // Run ids that currently have a pending waitpoint — feeds the
  // "Has waitpoint" filter. Optional; absent = filter is a no-op.
  runsWithWaitpoint?: ReadonlySet<string>
}

export function RunTimelineRail({
  runs,
  selectedRunId,
  onSelect,
  loading,
  error,
  crews: crewsProp,
  runsWithWaitpoint,
}: RunTimelineRailProps) {
  const { workspaceId } = useWorkspace()
  const { pipelines } = usePipelines(workspaceId)
  const { schedules } = usePipelineSchedules(workspaceId)
  const [crews, setCrews] = useState<{ id: string; name: string }[]>(crewsProp ?? [])

  // Fetch crews if the parent didn't supply them. Cheap one-shot —
  // crew list rarely changes mid-session.
  useEffect(() => {
    if (crewsProp || !workspaceId) return
    apiFetch(`/api/v1/crews?workspace_id=${encodeURIComponent(workspaceId)}`)
      .then((r) => (r.ok ? r.json() : []))
      .then((d) => setCrews(Array.isArray(d) ? d.map((c) => ({ id: c.id, name: c.name })) : []))
      .catch(() => { /* non-fatal — filter dropdown just shows no options */ })
  }, [crewsProp, workspaceId])

  // Persisted user state
  const [filter, setFilter] = useUserPreference<RunFilter>("activity.rail.filter", {
    status: "all",
  })
  const [sort, setSort] = useUserPreference<SortAxis>("activity.rail.sort", "newest")
  const [group, setGroup] = useUserPreference<GroupAxis>("activity.rail.group", "source")
  const [search, setSearch] = useState("")

  // Build filter dropdown options from the actual run set so we
  // never offer a filter dimension with zero matches.
  const options = useMemo(() => {
    const sourceSet = new Set<TriggerSource>()
    const slugSet = new Set<string>()
    for (const r of runs) {
      if (r.triggered_via) sourceSet.add(r.triggered_via as TriggerSource)
      if (r.pipeline_slug) slugSet.add(r.pipeline_slug)
    }
    const routineList: { slug: string; name: string }[] = []
    const slugToName = new Map<string, string>()
    for (const r of runs) slugToName.set(r.pipeline_slug, r.pipeline_name || r.pipeline_slug)
    for (const slug of slugSet) {
      routineList.push({ slug, name: slugToName.get(slug) ?? slug })
    }
    routineList.sort((a, b) => a.name.localeCompare(b.name))
    return {
      crews,
      routines: routineList,
      sources: Array.from(sourceSet),
    }
  }, [runs, crews])

  // Apply search via the same filter pipeline so the toolbar's
  // filter chip count and the body stay consistent.
  const effectiveFilter = useMemo<RunFilter>(
    () => ({ ...filter, search: search.trim() || undefined }),
    [filter, search],
  )
  const filteredRuns = useMemo(
    () => applyFilters(runs, effectiveFilter, runsWithWaitpoint),
    [runs, effectiveFilter, runsWithWaitpoint],
  )
  const sortedRuns = useMemo(() => sortRuns(filteredRuns, sort), [filteredRuns, sort])

  const counts = useMemo(() => {
    const ofStatus = (statuses: string[]) =>
      runs.filter((r) => statuses.includes(r.status)).length
    return {
      active: ofStatus(["running", "queued", "paused"]),
      all: runs.length,
      completed: ofStatus(["completed"]),
      failed: ofStatus(["failed", "cancelled", "interrupted"]),
    }
  }, [runs])

  // Build group context — schedules + crews + per-routine run buckets
  // for the hover card.
  const ctx = useMemo(() => {
    const cronBySlug = new Map<string, string>()
    const scheduleByPipelineSlug = new Map<string, typeof schedules[number]>()
    for (const s of schedules) {
      if (s.target_pipeline_slug) {
        if (s.cron_expr) cronBySlug.set(s.target_pipeline_slug, s.cron_expr)
        scheduleByPipelineSlug.set(s.target_pipeline_slug, s)
      }
    }
    const crewNameById = new Map(crews.map((c) => [c.id, c.name]))
    const routineNameBySlug = new Map<string, string>()
    for (const p of pipelines) routineNameBySlug.set(p.slug, p.name)

    const runsByPipelineSlug = new Map<string, PipelineRun[]>()
    for (const r of runs) {
      const arr = runsByPipelineSlug.get(r.pipeline_slug) ?? []
      arr.push(r)
      runsByPipelineSlug.set(r.pipeline_slug, arr)
    }
    const crewNameByPipelineSlug = new Map<string, string>()
    for (const p of pipelines) {
      const cName = p.author_crew_id ? crewNameById.get(p.author_crew_id) : undefined
      if (cName) crewNameByPipelineSlug.set(p.slug, cName)
    }

    return {
      cronBySlug,
      crewNameById,
      routineNameBySlug,
      runsByPipelineSlug,
      crewNameByPipelineSlug,
      scheduleByPipelineSlug,
    }
  }, [schedules, crews, pipelines, runs])

  const groups = useMemo(
    () => groupRuns(sortedRuns, group, ctx),
    [sortedRuns, group, ctx],
  )

  return (
    <div className="flex h-full flex-col bg-card">
      <div className="relative">
        <RailToolbar
          filter={filter}
          onFilterChange={setFilter}
          search={search}
          onSearchChange={setSearch}
          sort={sort}
          onSortChange={setSort}
          group={group}
          onGroupChange={setGroup}
          counts={counts}
          options={options}
        />
        <div className="absolute right-2 top-2">
          <SavedViewsButton
            current={{ filter, sort, group }}
            onApply={(v) => {
              setFilter(v.filter)
              setSort(v.sort)
              setGroup(v.group)
            }}
          />
        </div>
      </div>

      <div className="min-h-0 flex-1 overflow-y-auto">
        {loading && runs.length === 0 ? (
          <div className="flex h-full items-center justify-center text-xs text-muted-foreground">
            <Spinner className="mr-2 h-3 w-3" /> Loading runs…
          </div>
        ) : error ? (
          <div className="p-3 text-xs text-rose-300">Runs unavailable: {error}</div>
        ) : runs.length === 0 ? (
          <div className="flex flex-col items-center justify-center gap-2 p-6 text-center">
            <ScrollText className="h-6 w-6 text-muted-foreground/30" />
            <div className="text-xs text-muted-foreground">No runs in the workspace yet.</div>
          </div>
        ) : (
          <RunGroupTree
            groups={groups}
            selectedRunId={selectedRunId}
            onSelectRun={onSelect}
            routineCardCtx={{
              crewNameByPipelineSlug: ctx.crewNameByPipelineSlug,
              cronExprByPipelineSlug: ctx.cronBySlug,
              runsByPipelineSlug: ctx.runsByPipelineSlug,
              scheduleByPipelineSlug: ctx.scheduleByPipelineSlug,
            }}
          />
        )}
      </div>

      <div className="shrink-0 border-t border-white/[0.06] px-2 py-1.5 text-[10px] text-muted-foreground/60">
        <span className="tabular-nums">
          {filteredRuns.length}
          {filteredRuns.length !== runs.length && (
            <span className="text-muted-foreground/40"> / {runs.length}</span>
          )}{" "}
          runs · {counts.active} active · {counts.failed} failed
        </span>
      </div>
    </div>
  )
}

function sortRuns(runs: PipelineRun[], sort: SortAxis): PipelineRun[] {
  const sorted = [...runs]
  if (sort === "newest") {
    sorted.sort((a, b) => parseTime(b.started_at) - parseTime(a.started_at))
  } else if (sort === "oldest") {
    sorted.sort((a, b) => parseTime(a.started_at) - parseTime(b.started_at))
  } else if (sort === "cost-desc") {
    sorted.sort((a, b) => b.cost_usd - a.cost_usd)
  } else if (sort === "duration-desc") {
    sorted.sort((a, b) => b.duration_ms - a.duration_ms)
  }
  return sorted
}

function parseTime(iso?: string): number {
  if (!iso) return 0
  const t = new Date(iso).getTime()
  return Number.isNaN(t) ? 0 : t
}
