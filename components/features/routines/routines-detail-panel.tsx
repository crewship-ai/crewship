"use client"

import { useEffect, useRef, useState } from "react"
import { X, Play, FlaskConical, Eye, Square } from "lucide-react"
import { Button } from "@/components/ui/button"
import { Tabs, TabsList, TabsTrigger, TabsContent } from "@/components/ui/tabs"
import { Badge } from "@/components/ui/badge"
import { toast } from "sonner"
import { RoutineOverviewTab } from "./routine-overview-tab"
import { RoutineEditorTab } from "./routine-editor-tab"
import { RoutineRunsTab } from "./routine-runs-tab"
import { RoutineVersionsTab } from "./routine-versions-tab"
import { RoutineSchedulesTab } from "./routine-schedules-tab"
import { RoutineWebhooksTab } from "./routine-webhooks-tab"
import { RoutineWaitpointsTab } from "./routine-waitpoints-tab"

// RoutinesDetailPanel — right-side detail for the selected routine.
// Hosts the seven sub-tabs (Overview, Editor, Runs, Versions,
// Schedules, Webhooks, Waitpoints) plus the action toolbar
// (Run / TestRun / DryRun / Cancel). Subscribes to the same routine
// state the list view reads, so refresh after a successful Run is
// already covered by usePipelines' WS subscription in the layout.

export interface RoutineDetail {
  id: string
  slug: string
  name: string
  description?: string
  dsl_version: string
  definition: Record<string, unknown>
  definition_hash: string
  ephemeral: boolean
  workspace_visible: boolean
  invocation_count: number
  last_invoked_at?: string
  last_invocation_status?: string
  author_crew_id?: string
  author_agent_id?: string
  author_user_id?: string
  authored_via: string
  created_at: string
  updated_at: string
  head_version?: number
}

interface Props {
  workspaceId: string
  slug: string
  onClose: () => void
  onChanged: () => void
}

export function RoutinesDetailPanel({ workspaceId, slug, onClose, onChanged }: Props) {
  const [routine, setRoutine] = useState<RoutineDetail | null>(null)
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)
  const [busyAction, setBusyAction] = useState<string | null>(null)
  // abortRef tracks the in-flight fetch so a fast workspace/slug
  // switch cancels stale work. Without this, a slow network +
  // rapid-fire selection could race-overwrite the panel with the
  // wrong routine's data.
  const abortRef = useRef<AbortController | null>(null)

  const fetchRoutine = async () => {
    abortRef.current?.abort()
    const ctrl = new AbortController()
    abortRef.current = ctrl
    setLoading(true)
    setError(null)
    try {
      const res = await fetch(`/api/v1/workspaces/${workspaceId}/pipelines/${slug}`, {
        signal: ctrl.signal,
      })
      if (ctrl.signal.aborted) return
      if (!res.ok) throw new Error(`fetch routine: ${res.status}`)
      const r: RoutineDetail = await res.json()
      if (ctrl.signal.aborted) return
      setRoutine(r)
    } catch (e) {
      if (ctrl.signal.aborted) return
      setError(e instanceof Error ? e.message : String(e))
      // Stale data from the previous slug would be misleading next
      // to a "fetch failed" banner; clear so the panel reflects the
      // current selection's failure state rather than the prior one.
      setRoutine(null)
    } finally {
      if (!ctrl.signal.aborted) setLoading(false)
    }
  }

  useEffect(() => {
    fetchRoutine()
    return () => {
      abortRef.current?.abort()
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [workspaceId, slug])

  const triggerAction = async (action: "run" | "test_run" | "dry_run") => {
    setBusyAction(action)
    try {
      const res = await fetch(`/api/v1/workspaces/${workspaceId}/pipelines/${slug}/${action}`, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ inputs: {} }),
      })
      if (!res.ok) {
        const t = await res.text().catch(() => "")
        throw new Error(`${res.status}: ${t || res.statusText}`)
      }
      const data = await res.json().catch(() => ({}))
      toast.success(`${actionLabel(action)} started`, {
        description:
          action === "dry_run"
            ? "Dry-run report ready — see Runs tab"
            : data.run_id
              ? `Run ${String(data.run_id).slice(0, 12)}…`
              : "Watch the Runs tab for live status",
      })
      onChanged()
      // Re-fetch so invocation_count + last_invocation_status update.
      fetchRoutine()
    } catch (e) {
      toast.error(`${actionLabel(action)} failed`, {
        description: e instanceof Error ? e.message : String(e),
      })
    } finally {
      setBusyAction(null)
    }
  }

  return (
    <div className="flex h-full flex-col">
      {/* Header */}
      <div className="flex items-start justify-between border-b border-white/[0.06] px-4 py-3 shrink-0">
        <div className="min-w-0 flex-1">
          {loading ? (
            <div className="h-5 w-40 animate-pulse rounded bg-muted/40" />
          ) : (
            <>
              <div className="flex items-center gap-2">
                <h2 className="truncate text-sm font-semibold">
                  {routine?.name || slug}
                </h2>
                {routine?.ephemeral && (
                  <Badge variant="outline" className="text-[10px]">ephemeral</Badge>
                )}
              </div>
              <div className="mt-0.5 flex items-center gap-2 font-mono text-[10px] text-muted-foreground">
                <span>{routine?.slug || slug}</span>
                {routine?.head_version && <span>v{routine.head_version}</span>}
              </div>
            </>
          )}
        </div>
        <Button
          size="sm"
          variant="ghost"
          onClick={onClose}
          className="h-7 w-7 p-0"
          aria-label="Close routine details"
        >
          <X className="h-3 w-3" aria-hidden="true" />
        </Button>
      </div>

      {/* Action toolbar */}
      <div className="flex items-center gap-1 border-b border-white/[0.06] bg-card/30 px-3 py-2 shrink-0">
        <Button
          size="sm"
          variant="default"
          className="h-7 gap-1.5 text-xs"
          onClick={() => triggerAction("run")}
          disabled={!!busyAction || !routine}
          title="Invoke routine with empty inputs"
        >
          <Play className="h-3 w-3" />
          {busyAction === "run" ? "Running…" : "Run"}
        </Button>
        <Button
          size="sm"
          variant="outline"
          className="h-7 gap-1.5 text-xs"
          onClick={() => triggerAction("test_run")}
          disabled={!!busyAction || !routine}
          title="Run on execution tier; logs result without persisting state"
        >
          <FlaskConical className="h-3 w-3" />
          Test run
        </Button>
        <Button
          size="sm"
          variant="outline"
          className="h-7 gap-1.5 text-xs"
          onClick={() => triggerAction("dry_run")}
          disabled={!!busyAction || !routine}
          title="Walk DSL, render templates, compute would_execute report — no agent invocations"
        >
          <Eye className="h-3 w-3" />
          Dry run
        </Button>
        <div className="flex-1" />
        <Button
          size="sm"
          variant="ghost"
          className="h-7 gap-1.5 text-xs text-muted-foreground hover:text-red-400"
          onClick={() => toast.info("Cancel uses POST /runs/{run_id}/cancel — wire from Runs tab when run is selected")}
          title="Cancel an active run (select run in Runs tab)"
          disabled
        >
          <Square className="h-3 w-3" />
          Cancel
        </Button>
      </div>

      {/* Body */}
      {error && (
        <div className="m-4 rounded-md border border-red-500/30 bg-red-500/5 px-3 py-2 text-xs text-red-400">
          {error}
        </div>
      )}

      {routine && (
        <Tabs defaultValue="overview" className="flex flex-1 flex-col overflow-hidden">
          <TabsList className="m-2 grid grid-cols-7 h-7">
            <TabsTrigger value="overview" className="text-[10px]">Overview</TabsTrigger>
            <TabsTrigger value="editor" className="text-[10px]">Editor</TabsTrigger>
            <TabsTrigger value="runs" className="text-[10px]">Runs</TabsTrigger>
            <TabsTrigger value="versions" className="text-[10px]">Versions</TabsTrigger>
            <TabsTrigger value="schedules" className="text-[10px]">Schedules</TabsTrigger>
            <TabsTrigger value="webhooks" className="text-[10px]">Webhooks</TabsTrigger>
            <TabsTrigger value="waitpoints" className="text-[10px]">Wait</TabsTrigger>
          </TabsList>

          <div className="flex-1 overflow-auto">
            <TabsContent value="overview" className="m-0 p-3">
              <RoutineOverviewTab routine={routine} />
            </TabsContent>
            <TabsContent value="editor" className="m-0 p-0 h-full">
              <RoutineEditorTab routine={routine} />
            </TabsContent>
            <TabsContent value="runs" className="m-0 p-3">
              <RoutineRunsTab workspaceId={workspaceId} slug={routine.slug} />
            </TabsContent>
            <TabsContent value="versions" className="m-0 p-3">
              <RoutineVersionsTab
                workspaceId={workspaceId}
                slug={routine.slug}
                onRolledBack={() => {
                  fetchRoutine()
                  onChanged()
                }}
              />
            </TabsContent>
            <TabsContent value="schedules" className="m-0 p-3">
              <RoutineSchedulesTab workspaceId={workspaceId} pipelineId={routine.id} slug={routine.slug} />
            </TabsContent>
            <TabsContent value="webhooks" className="m-0 p-3">
              <RoutineWebhooksTab workspaceId={workspaceId} pipelineId={routine.id} slug={routine.slug} />
            </TabsContent>
            <TabsContent value="waitpoints" className="m-0 p-3">
              <RoutineWaitpointsTab workspaceId={workspaceId} slug={routine.slug} />
            </TabsContent>
          </div>
        </Tabs>
      )}
    </div>
  )
}

function actionLabel(a: "run" | "test_run" | "dry_run"): string {
  return a === "run" ? "Run" : a === "test_run" ? "Test run" : "Dry run"
}
