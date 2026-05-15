"use client"

import { useEffect, useMemo, useState } from "react"
import { useUserPreference } from "@/hooks/use-user-preference"
import { useWorkspace } from "@/hooks/use-workspace"
import { usePipelineRuns } from "@/hooks/use-pipeline-runs"
import { usePipelines } from "@/hooks/use-pipelines"
import { useTrace } from "@/hooks/use-trace"
import { useTraceSelection } from "@/hooks/use-trace-selection"
import { useRunWaitpoints } from "@/hooks/use-run-waitpoints"
import { useStepMetrics } from "@/hooks/use-step-metrics"
import { Skeleton } from "@/components/ui/skeleton"
import { Button } from "@/components/ui/button"
import {
  Flame,
  PanelLeftClose,
  PanelLeftOpen,
  PanelRightClose,
  PanelRightOpen,
  Timer,
} from "lucide-react"
import { cn } from "@/lib/utils"
import { useIsMobile } from "@/hooks/use-mobile"
import { RunTimelineRail } from "./run-timeline-rail"
import { TraceCanvas } from "./trace-canvas"
import { TraceSidePanel } from "./trace-side-panel"
import type { TraceStep } from "@/lib/trace/types"
import { shadeNodes, type HeatmapBucket, type HeatmapMode } from "@/lib/trace/percentile-heatmap"
import { buildOverviewGraph } from "@/lib/trace/build-overview-graph"
import type { Mission } from "@/lib/types/mission"

// ActivityTracePage — top-level page for /activity. Replaces the old
// OrchestrationPageShell layout (which exposed Runs / Graph / Timeline
// / Feed sub-tabs) with a single canvas + rail + panel layout.
//
// User intent: minimise context switching, keep it as a single canvas
// is the answer. Sub-tabs got cut on purpose. The legacy RunsView is
// still reachable from /orchestration as a fallback list view.
//
// URL state (single source of truth — `useTraceSelection`):
//   ?run=<id>     selects a run, drives canvas into trace mode
//   ?step=<id>    opens the side panel on that step

export function ActivityTracePage() {
  const isMobile = useIsMobile()
  const { workspaceId, loading: wsLoading } = useWorkspace()
  const { runId, stepId, setRunId, setStepId } = useTraceSelection()

  // Rail data — list of recent runs in the workspace. Polls itself.
  const { runs, loading: runsLoading, error: runsError } = usePipelineRuns(
    workspaceId,
    "all",
  )

  // Canvas data — single run + DSL, polled while active + realtime.
  const { run, dsl, loading: traceLoading, error: traceError } = useTrace(
    workspaceId,
    runId,
  )

  // Pending waitpoints scoped to this run — drives inline Approve/
  // Deny on `wait` step nodes.
  const { waitpoints } = useRunWaitpoints(workspaceId, runId)
  const waitpointTokensByStepId = useMemo(() => {
    const m = new Map<string, string>()
    for (const w of waitpoints) m.set(w.step_id, w.token)
    return m
  }, [waitpoints])

  // Overview-mode data: missions (issues with routine_id) + pipelines.
  // Fetched here when no run is selected so the overview canvas has
  // something to render. We deliberately fetch a small page (50)
  // because the overview is a "what's bound to what" snapshot, not
  // a full dashboard.
  const { pipelines } = usePipelines(workspaceId)
  const [missions, setMissions] = useState<Mission[]>([])
  useEffect(() => {
    if (!workspaceId || runId) return
    let cancelled = false
    fetch(`/api/v1/missions?workspace_id=${encodeURIComponent(workspaceId)}&limit=50`)
      .then((r) => (r.ok ? r.json() : []))
      .then((d) => {
        if (cancelled) return
        setMissions(Array.isArray(d) ? d : [])
      })
      .catch(() => { /* non-fatal — overview just renders fewer chains */ })
    return () => {
      cancelled = true
    }
  }, [workspaceId, runId])

  const runsWithWaitpointSet = useMemo(() => {
    const s = new Set<string>()
    for (const r of runs) if (r.status === "paused") s.add(r.id)
    return s
  }, [runs])

  const overview = useMemo(() => {
    if (runId) return null
    return buildOverviewGraph({
      missions,
      pipelines,
      runs,
      runsWithWaitpoint: runsWithWaitpointSet,
    })
  }, [runId, missions, pipelines, runs, runsWithWaitpointSet])

  // Per-step duration / cost from journal entries — drives the
  // heatmap shading.
  const { metrics: stepMetrics } = useStepMetrics(
    workspaceId,
    run?.pipeline_slug,
    runId,
  )

  // Heatmap toggle — persisted per user so a viewer who always wants
  // duration shading gets it on next load.
  const [heatmapMode, setHeatmapMode] = useUserPreference<HeatmapMode>(
    "activity.heatmap.mode",
    "off",
  )

  // Pre-compute the heatmap bucket map up here. shadeNodes is fast,
  // but baking it into the canvas's memo would re-run dagreLayout
  // every time stepMetrics or heatmapMode flips. Memoizing the map
  // separately means a metric arriving over WS only re-shades node
  // borders — the node positions stay put.
  const heatmapBuckets = useMemo(() => {
    if (heatmapMode === "off" || stepMetrics.size === 0) {
      return new Map<string, HeatmapBucket>()
    }
    return shadeNodes(
      Array.from(stepMetrics, ([stepId, m]) => ({
        stepId,
        cost: m.costUsd,
        duration: m.durationMs,
      })),
      heatmapMode,
    )
  }, [stepMetrics, heatmapMode])

  // Persisted desktop preference + a derived mobile override. Without
  // the override / preference split, opening the page on a phone
  // would write `true` into the preference and then keep the rail
  // collapsed on the user's next desktop visit too — a phone-only
  // accommodation leaking into desktop.
  const [railPref, setRailPref] = useUserPreference<boolean>(
    "activity.rail.collapsed",
    false,
  )
  const railCollapsed = isMobile ? true : railPref
  const setRailCollapsed = setRailPref

  // Side panel is "auto-open when a step is selected, auto-close
  // when stepId clears". The user can also manually close it (clears
  // stepId via setStepId(null)). On mobile the panel becomes a full
  // overlay handled by AnimatePresence inside TraceSidePanel.
  const sidePanelOpen = stepId !== null
  const closeSidePanel = () => setStepId(null)

  // (Mobile rail collapse is derived, not persisted — see railCollapsed
  // above. No effect needed.)

  // Keyboard shortcuts:
  //   Esc   — close the side panel (clears stepId)
  //   ←/→   — navigate between adjacent runs in the rail
  // We bind on window so the user doesn't have to focus a specific
  // element first; matches the keyboard expectation of canvas-first
  // tools (Trigger.dev, Linear, etc.).
  useEffect(() => {
    function onKey(e: KeyboardEvent) {
      // Don't hijack typing in the rail's search field or any other
      // text input — the user is typing a query, not navigating.
      const target = e.target as HTMLElement | null
      if (target && (target.tagName === "INPUT" || target.tagName === "TEXTAREA" || target.isContentEditable)) {
        return
      }
      if (e.key === "Escape" && stepId) {
        setStepId(null)
        return
      }
      if (e.key === "ArrowDown" || e.key === "ArrowUp") {
        if (!runs.length) return
        const idx = runId ? runs.findIndex((r) => r.id === runId) : -1
        const next =
          e.key === "ArrowDown"
            ? Math.min(runs.length - 1, idx + 1)
            : Math.max(0, idx - 1)
        if (next !== idx && runs[next]) {
          e.preventDefault()
          setRunId(runs[next].id)
        }
      }
    }
    window.addEventListener("keydown", onKey)
    return () => window.removeEventListener("keydown", onKey)
  }, [runs, runId, stepId, setRunId, setStepId])

  // Resolve the selected step from the DSL. The canvas + side panel
  // both consume this so they stay in sync — no separate step lookup
  // logic in two places.
  const selectedStep: TraceStep | null = useMemo(() => {
    if (!stepId || !dsl?.steps) return null
    return dsl.steps.find((s) => s.id === stepId) ?? null
  }, [stepId, dsl])

  // Output for the selected step from the run's step_outputs map.
  const selectedOutput = useMemo(() => {
    if (!stepId || !run?.step_outputs) return undefined
    return run.step_outputs[stepId]
  }, [stepId, run?.step_outputs])

  const isFailedStep = run?.failed_at_step === stepId

  // Loading state — show skeleton while workspace ID is resolving.
  // Rail's own loading state covers the empty-runs flicker afterwards.
  if (wsLoading) {
    return (
      <div className="flex h-[calc(100vh-48px)] items-center justify-center">
        <Skeleton className="m-6 h-[600px] w-full rounded-xl" />
      </div>
    )
  }

  if (!workspaceId) {
    return (
      <div className="flex h-[calc(100vh-48px)] items-center justify-center text-sm text-muted-foreground">
        No workspace selected.
      </div>
    )
  }

  return (
    <div className="flex h-[calc(100vh-48px)] flex-col bg-background">
      {/* Top toolbar — minimal on Phase 1; heatmap toggle + view
        * switch land in Phase 4. */}
      <div className="flex h-9 shrink-0 items-center gap-1 border-b border-white/[0.08] bg-card px-2">
        <Button
          variant="ghost"
          size="icon-xs"
          aria-label={railCollapsed ? "Expand run rail" : "Collapse run rail"}
          className="text-muted-foreground/70 hover:text-foreground/80"
          onClick={() => setRailCollapsed(!railCollapsed)}
        >
          {railCollapsed ? <PanelLeftOpen className="h-3.5 w-3.5" /> : <PanelLeftClose className="h-3.5 w-3.5" />}
        </Button>
        <span className="text-[11px] font-semibold uppercase tracking-wider text-muted-foreground/70">
          Activity
        </span>
        <span className="text-[11px] text-muted-foreground/40">
          {run ? `· ${run.pipeline_name || run.pipeline_slug}` : "· no run selected"}
        </span>

        <div className="flex-1" />

        {/* Heatmap toggle — only meaningful when a run is selected */}
        {run !== null && (
          <div className="mr-2 flex items-center gap-0.5 rounded border border-white/[0.06] bg-background p-0.5">
            <HeatmapButton
              label="Off"
              icon={null}
              active={heatmapMode === "off"}
              onClick={() => setHeatmapMode("off")}
            />
            <HeatmapButton
              label="Cost"
              icon={Flame}
              active={heatmapMode === "cost"}
              onClick={() => setHeatmapMode("cost")}
            />
            <HeatmapButton
              label="Time"
              icon={Timer}
              active={heatmapMode === "duration"}
              onClick={() => setHeatmapMode("duration")}
            />
          </div>
        )}

        {sidePanelOpen && (
          <Button
            variant="ghost"
            size="icon-xs"
            aria-label="Close detail panel"
            className="text-muted-foreground/70 hover:text-foreground/80"
            onClick={closeSidePanel}
          >
            <PanelRightClose className="h-3.5 w-3.5" />
          </Button>
        )}
        {!sidePanelOpen && stepId === null && run !== null && (
          <Button
            variant="ghost"
            size="icon-xs"
            aria-label="Open detail panel"
            className="opacity-50 hover:opacity-100"
            // Disabled until a step is selected — clicking the open
            // icon with no selection would have nothing to show.
            disabled
          >
            <PanelRightOpen className="h-3.5 w-3.5" />
          </Button>
        )}
      </div>

      {/* 3-zone grid: rail | canvas | side panel */}
      <div
        className="grid min-h-0 flex-1 transition-all duration-200"
        style={{
          gridTemplateColumns: isMobile
            ? "1fr"
            : `${railCollapsed ? "0px" : "280px"} 1fr ${sidePanelOpen ? "360px" : "0px"}`,
        }}
      >
        {/* Left rail */}
        <div
          className={cn(
            "min-h-0 overflow-hidden border-r border-white/[0.06] transition-all duration-200",
            railCollapsed && "border-r-0",
          )}
        >
          {!railCollapsed && (
            <RunTimelineRail
              runs={runs}
              selectedRunId={runId}
              onSelect={setRunId}
              loading={runsLoading}
              error={runsError}
              runsWithWaitpoint={runsWithWaitpointSet}
            />
          )}
        </div>

        {/* Center canvas */}
        <div className="relative min-h-0 overflow-hidden">
          {traceLoading && !run && (
            <div className="absolute inset-0 flex items-center justify-center bg-background/50">
              <Skeleton className="h-32 w-64 rounded" />
            </div>
          )}
          {traceError && (
            <div className="absolute left-1/2 top-3 -translate-x-1/2 rounded border border-rose-500/30 bg-rose-500/10 px-3 py-1 text-xs text-rose-300">
              Trace unavailable: {traceError}
            </div>
          )}
          {/* Heatmap-on, no metrics: surface the gap honestly. The
            * journal-fetch window is capped at 200 entries per
            * pipeline, so a run from far back in history won't have
            * its step.completed events anymore. Without this hint
            * the user thinks the toggle is broken. */}
          {heatmapMode !== "off" && run !== null && stepMetrics.size === 0 && (
            <div className="absolute left-1/2 top-3 -translate-x-1/2 rounded border border-amber-500/30 bg-amber-500/10 px-3 py-1 text-[11px] text-amber-200">
              Heatmap data not available for this run — older runs may have
              rolled out of the metrics window
            </div>
          )}
          <TraceCanvas
            run={run}
            dsl={dsl}
            selectedStepId={stepId}
            onStepSelect={setStepId}
            workspaceId={workspaceId}
            waitpointTokensByStepId={waitpointTokensByStepId}
            heatmapBuckets={heatmapBuckets}
            stepMetrics={stepMetrics}
            overview={overview}
            onSelectRun={setRunId}
          />
        </div>

        {/* Right side panel */}
        <div className="min-h-0 overflow-hidden">
          <TraceSidePanel
            open={sidePanelOpen}
            step={selectedStep}
            output={selectedOutput}
            errorMessage={run?.error_message}
            isFailedStep={isFailedStep}
            onClose={closeSidePanel}
          />
        </div>
      </div>
    </div>
  )
}

function HeatmapButton({
  label,
  icon: Icon,
  active,
  onClick,
}: {
  label: string
  icon: typeof Flame | null
  active: boolean
  onClick: () => void
}) {
  return (
    <button
      type="button"
      onClick={onClick}
      aria-pressed={active}
      className={cn(
        "flex items-center gap-1 rounded px-1.5 py-0.5 text-[10px] transition-colors",
        active
          ? "bg-blue-500/15 text-blue-300"
          : "text-muted-foreground/70 hover:text-foreground/80",
      )}
    >
      {Icon && <Icon className="h-3 w-3" />}
      {label}
    </button>
  )
}
