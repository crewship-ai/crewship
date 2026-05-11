"use client"

import { useEffect, useRef, useState } from "react"
import { X, Play, FlaskConical, Eye, Square, Loader2 } from "lucide-react"
import { Button } from "@/components/ui/button"
import { Tabs, TabsContent } from "@/components/ui/tabs"
import { TabBar } from "@/components/ui/tab-bar"
import { Badge } from "@/components/ui/badge"
import { cn } from "@/lib/utils"
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

  const status = routine?.last_invocation_status?.toLowerCase()
  const statusTone =
    status === "completed" || status === "succeeded" || status === "success"
      ? { bg: "bg-emerald-500/12", text: "text-emerald-300", ring: "ring-emerald-500/30", label: "Last run · completed" }
      : status === "failed" || status === "error"
        ? { bg: "bg-rose-500/12", text: "text-rose-300", ring: "ring-rose-500/30", label: "Last run · failed" }
        : status === "running"
          ? { bg: "bg-blue-500/12", text: "text-blue-300", ring: "ring-blue-500/30", label: "Running…" }
          : { bg: "bg-white/[0.04]", text: "text-muted-foreground", ring: "ring-white/[0.08]", label: "Never invoked" }

  const [activeTab, setActiveTab] = useState("overview")

  return (
    <div className="flex h-full flex-col">
      {/* Hero — gradient title + slug + status pills + description + action group */}
      <div
        className={cn(
          "shrink-0 border-b border-white/[0.06] px-6 pb-5 pt-6",
          "bg-gradient-to-br from-blue-500/[0.05] via-transparent to-violet-500/[0.03]",
        )}
      >
        <div className="flex items-start gap-4">
          <div className="min-w-0 flex-1">
            {loading ? (
              <div className="space-y-2">
                <div className="h-3 w-32 animate-pulse rounded bg-muted/30" />
                <div className="h-8 w-72 animate-pulse rounded bg-muted/40" />
                <div className="h-3 w-44 animate-pulse rounded bg-muted/30" />
              </div>
            ) : (
              <>
                {/* Status + meta pills */}
                <div className="flex flex-wrap items-center gap-2">
                  <span
                    className={cn(
                      "inline-flex items-center gap-1.5 rounded-full px-2.5 py-1 text-[11px] font-medium ring-1",
                      statusTone.bg,
                      statusTone.text,
                      statusTone.ring,
                    )}
                  >
                    <span className={cn("h-1.5 w-1.5 rounded-full", statusTone.text === "text-muted-foreground" ? "bg-muted-foreground/60" : "bg-current")} />
                    {statusTone.label}
                  </span>
                  <Badge variant="outline" className="px-2 py-0 text-[11px]">DSL v{routine?.dsl_version}</Badge>
                  <Badge variant="outline" className="px-2 py-0 text-[11px]">
                    {routine?.workspace_visible ? "workspace" : "private"}
                  </Badge>
                  {routine?.ephemeral && (
                    <Badge variant="outline" className="px-2 py-0 text-[11px]">ephemeral</Badge>
                  )}
                  {routine?.head_version != null && (
                    <Badge variant="outline" className="px-2 py-0 text-[11px] font-mono">v{routine.head_version}</Badge>
                  )}
                </div>

                {/* Title + slug */}
                <h1 className="mt-3 truncate text-2xl font-semibold tracking-tight">
                  {routine?.name || slug}
                </h1>
                <div className="mt-1 truncate font-mono text-xs text-muted-foreground">
                  {routine?.slug || slug}
                </div>

                {/* Description */}
                {routine?.description && (
                  <p className="mt-3 max-w-3xl text-sm leading-relaxed text-foreground/80">
                    {routine.description}
                  </p>
                )}
              </>
            )}
          </div>
          <Button
            size="sm"
            variant="ghost"
            onClick={onClose}
            className="h-8 w-8 shrink-0 p-0"
            aria-label="Close routine details"
          >
            <X className="h-4 w-4" aria-hidden="true" />
          </Button>
        </div>

        {/* Action group — pill button row */}
        <div className="mt-5 flex items-center gap-2">
          <Button
            onClick={() => triggerAction("run")}
            disabled={!!busyAction || !routine}
            className={cn(
              "h-9 gap-2 rounded-md px-4 text-sm font-semibold shadow-lg shadow-blue-500/20",
              "bg-gradient-to-r from-blue-500 to-violet-500 text-white hover:brightness-110",
              "disabled:opacity-50 disabled:shadow-none",
            )}
            title="Invoke routine with empty inputs"
          >
            {busyAction === "run" ? (
              <Loader2 className="h-3.5 w-3.5 animate-spin" />
            ) : (
              <Play className="h-3.5 w-3.5 fill-current" />
            )}
            {busyAction === "run" ? "Running…" : "Run"}
          </Button>
          <Button
            variant="outline"
            onClick={() => triggerAction("test_run")}
            disabled={!!busyAction || !routine}
            className="h-9 gap-2 rounded-md px-4 text-sm"
            title="Run on execution tier; logs result without persisting state"
          >
            <FlaskConical className="h-3.5 w-3.5" />
            {busyAction === "test_run" ? "Testing…" : "Test run"}
          </Button>
          <Button
            variant="outline"
            onClick={() => triggerAction("dry_run")}
            disabled={!!busyAction || !routine}
            className="h-9 gap-2 rounded-md px-4 text-sm"
            title="Walk DSL, render templates, compute would_execute report — no agent invocations"
          >
            <Eye className="h-3.5 w-3.5" />
            {busyAction === "dry_run" ? "Computing…" : "Dry run"}
          </Button>
          <div className="flex-1" />
          <Button
            variant="ghost"
            className="h-9 gap-2 rounded-md px-3 text-sm text-muted-foreground hover:text-rose-400"
            onClick={() => toast.info("Select an active run in the Runs tab to cancel it")}
            title="Cancel an active run (select run in Runs tab)"
            disabled
          >
            <Square className="h-3.5 w-3.5" />
            Cancel
          </Button>
        </div>
      </div>

      {/* Tab bar — primitive with animated underline */}
      {routine && (
        <Tabs value={activeTab} onValueChange={setActiveTab} className="flex flex-1 flex-col overflow-hidden">
          <TabBar
            value={activeTab}
            onValueChange={setActiveTab}
            layoutId="routine-detail-tabs-indicator"
            ariaLabel="Routine sections"
            className="shrink-0 px-4"
          >
            <TabBar.Item value="overview" className="h-10 text-sm">Overview</TabBar.Item>
            <TabBar.Item value="editor" className="h-10 text-sm">Editor</TabBar.Item>
            <TabBar.Item value="runs" className="h-10 text-sm">Runs</TabBar.Item>
            <TabBar.Item value="versions" className="h-10 text-sm">Versions</TabBar.Item>
            <TabBar.Item value="schedules" className="h-10 text-sm">Schedules</TabBar.Item>
            <TabBar.Item value="webhooks" className="h-10 text-sm">Webhooks</TabBar.Item>
            <TabBar.Item value="waitpoints" className="h-10 text-sm">Wait points</TabBar.Item>
          </TabBar>

          {error && (
            <div className="m-4 rounded-md border border-red-500/30 bg-red-500/5 px-3 py-2 text-sm text-red-400">
              {error}
            </div>
          )}

          <div className="flex-1 overflow-auto">
            <TabsContent value="overview" className="m-0 px-6 py-5">
              <RoutineOverviewTab routine={routine} workspaceId={workspaceId} />
            </TabsContent>
            <TabsContent value="editor" className="m-0 h-full p-0">
              <RoutineEditorTab
                routine={routine}
                workspaceId={workspaceId}
                onSaved={() => {
                  fetchRoutine()
                  onChanged()
                }}
              />
            </TabsContent>
            <TabsContent value="runs" className="m-0 px-6 py-5">
              <RoutineRunsTab workspaceId={workspaceId} slug={routine.slug} />
            </TabsContent>
            <TabsContent value="versions" className="m-0 px-6 py-5">
              <RoutineVersionsTab
                workspaceId={workspaceId}
                slug={routine.slug}
                onRolledBack={() => {
                  fetchRoutine()
                  onChanged()
                }}
              />
            </TabsContent>
            <TabsContent value="schedules" className="m-0 px-6 py-5">
              <RoutineSchedulesTab workspaceId={workspaceId} pipelineId={routine.id} slug={routine.slug} />
            </TabsContent>
            <TabsContent value="webhooks" className="m-0 px-6 py-5">
              <RoutineWebhooksTab workspaceId={workspaceId} pipelineId={routine.id} slug={routine.slug} />
            </TabsContent>
            <TabsContent value="waitpoints" className="m-0 px-6 py-5">
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
