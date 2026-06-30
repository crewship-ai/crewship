"use client"

import { useEffect, useRef, useState } from "react"
import { useRouter } from "next/navigation"
import { X, Play, FlaskConical, Eye, Square, Check, Ban, Power, PowerOff } from "lucide-react"
import { Spinner } from "@/components/ui/spinner"
import { Button } from "@/components/ui/button"
import { Tabs, TabsContent } from "@/components/ui/tabs"
import { TabBar } from "@/components/ui/tab-bar"
import { Badge } from "@/components/ui/badge"
import { cn } from "@/lib/utils"
import { toast } from "sonner"
import { apiFetch } from "@/lib/api-fetch"
import { useAbilities } from "@/hooks/use-abilities"
import {
  routineStatusBadge,
  runDisabledReason,
  canApproveRoutine,
  canKillRoutine,
  normalizeRoutineStatus,
} from "@/lib/routine-governance"
import { buildPipelineActionRequest, canTestRun } from "@/lib/pipeline-actions"
import { integrationLabel, extractMissingIntegrations } from "@/lib/integration-labels"
import { PipelineRunActivity } from "@/components/features/activity/pipeline-run-activity"
import { usePendingApproval } from "@/hooks/use-pending-approval"
import { RoutineApprovalBanner } from "@/components/features/routines/routine-approval-banner"
import { RoutineOverviewTab } from "./routine-overview-tab"
import { RoutineEditorTab } from "./routine-editor-tab"
import { RoutineRunsTab } from "./routine-runs-tab"
import { RoutineVersionsTab } from "./routine-versions-tab"
import { RoutineSchedulesTab } from "./routine-schedules-tab"
import { RoutineWebhooksTab } from "./routine-webhooks-tab"
import { RoutineWaitpointsTab } from "./routine-waitpoints-tab"
import { RoutineDryRunReport, type DryRunResult } from "./routine-dry-run-report"
import type { RoutineManifest } from "@/lib/routine-flow"

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
  // Lifecycle status: "active" (runnable), "proposed" (awaiting MANAGER+
  // approval), "disabled" (killed by OWNER/ADMIN). Absent → "active".
  status?: "active" | "proposed" | "disabled"
  author_crew_id?: string
  author_agent_id?: string
  author_user_id?: string
  authored_via: string
  created_at: string
  updated_at: string
  head_version?: number
  // Composio connector slugs this routine needs the executing crew to
  // have connected (e.g. ["github","slack"]). Absent/empty on routines
  // with no third-party dependencies. Surfaced as chips on the Overview
  // tab and used to explain a 422 run-refusal.
  integrations_required?: string[]
  // manifest is the server-derived "blast radius" — the union of declared
  // resources and what's inferable from the step graph (integrations,
  // egress, credentials, agents, sub-routines, datastores, tools, plus
  // has_http / has_code flags). Only the detail endpoint returns it; absent
  // on list responses. Drives the flow diagram + "What it touches" panel.
  manifest?: RoutineManifest
}

interface Props {
  workspaceId: string
  slug: string
  onClose: () => void
  onChanged: () => void
}

export function RoutinesDetailPanel({ workspaceId, slug, onClose, onChanged }: Props) {
  const router = useRouter()
  const { role } = useAbilities()
  const [routine, setRoutine] = useState<RoutineDetail | null>(null)
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)
  const [busyAction, setBusyAction] = useState<string | null>(null)
  // Tracks the in-flight governance action (approve/reject/disable/enable)
  // so its button shows a spinner and the others stay disabled meanwhile.
  const [busyGov, setBusyGov] = useState<string | null>(null)
  // dryRunResult holds the `would_execute` report from the most recent
  // dry_run invocation so we can render it inline. Cleared on close.
  const [dryRunResult, setDryRunResult] = useState<DryRunResult | null>(null)
  // lastRunId holds the run_id of the most recent Run / Test run so we can
  // show its live activity rail inline (instant status after clicking).
  const [lastRunId, setLastRunId] = useState<string | null>(null)
  // abortRef tracks the in-flight fetch so a fast workspace/slug
  // switch cancels stale work. Without this, a slow network +
  // rapid-fire selection could race-overwrite the panel with the
  // wrong routine's data.
  const abortRef = useRef<AbortController | null>(null)

  // When the just-triggered run parks on an approval gate, this resolves the
  // waitpoint so we can surface an inline Approve/Reject banner + amber status
  // right here, instead of making the user hunt through the Wait points tab or
  // /inbox. Realtime events keep it live (no refresh).
  const {
    waitpoint: pendingApproval,
    deciding: decidingApproval,
    decide: decideApproval,
  } = usePendingApproval(workspaceId, lastRunId)

  const fetchRoutine = async () => {
    abortRef.current?.abort()
    const ctrl = new AbortController()
    abortRef.current = ctrl
    setLoading(true)
    setError(null)
    try {
      const res = await apiFetch(`/api/v1/workspaces/${workspaceId}/pipelines/${slug}`, {
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
    // Clear any leftover dry-run report from the previously-selected
    // routine. Without this, the violet panel above the tab bar keeps
    // rendering the prior routine's would_execute list until the user
    // manually dismisses it — a confusing "this report doesn't match
    // what I'm looking at" surface bug.
    setDryRunResult(null)
    setLastRunId(null)
    fetchRoutine()
    return () => {
      abortRef.current?.abort()
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [workspaceId, slug])

  const triggerAction = async (action: "run" | "test_run" | "dry_run") => {
    if (!routine) return
    setBusyAction(action)
    try {
      // Test run hits a slugless route with the draft definition in the body;
      // Run / Dry run address the saved pipeline by slug. See lib/pipeline-actions.
      const { url, body } = buildPipelineActionRequest(workspaceId, slug, action, routine)
      const res = await apiFetch(url, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify(body),
      })
      if (!res.ok) {
        // A run can be refused with 422 + RFC 7807 Problem Details when
        // the executing crew lacks a required integration. Parse the body
        // once: if it carries `missing_integrations`, show the actionable
        // "connect this integration" block instead of a generic failure
        // toast — and return early so we don't double-report.
        const rawBody = await res.text().catch(() => "")
        if (res.status === 422) {
          let parsed: unknown = null
          try {
            parsed = JSON.parse(rawBody)
          } catch {
            parsed = null
          }
          const missing = extractMissingIntegrations(parsed)
          if (missing.length > 0) {
            const labels = missing.map(integrationLabel)
            const detail =
              parsed && typeof parsed === "object" && typeof (parsed as Record<string, unknown>).detail === "string"
                ? String((parsed as Record<string, unknown>).detail)
                : undefined
            toast.error(
              `Tahle rutina potřebuje integraci: ${labels.join(", ")} — není připojená pro tuto crew`,
              {
                description:
                  detail ?? "Připoj chybějící integraci pro crew, která rutinu spouští, a spusť ji znovu.",
                action: {
                  label: "Spravovat integrace",
                  onClick: () => router.push("/integrations"),
                },
                duration: 10000,
              },
            )
            return
          }
        }
        throw new Error(`${res.status}: ${rawBody || res.statusText}`)
      }
      const data = await res.json().catch(() => ({}))
      if (action === "dry_run") {
        // Surface the would_execute report inline. Pre-fix this
        // payload was dropped — the toast pointed at the Runs tab
        // but dry runs don't emit step events. Now the user gets
        // per-step tier resolution + estimated cost up top.
        //
        // cost_usd / duration_ms are intentionally LEFT UNDEFINED
        // when the server doesn't return a number — coercing to 0
        // would render "$0.0000" indistinguishably from a real
        // zero-cost run. The report component falls back to summing
        // per-step estimates when the top-level total is missing.
        setDryRunResult({
          run_id: typeof data.run_id === "string" ? data.run_id : "",
          status: typeof data.status === "string" ? data.status : "DRY_RUN_OK",
          cost_usd: typeof data.cost_usd === "number" ? data.cost_usd : undefined,
          duration_ms: typeof data.duration_ms === "number" ? data.duration_ms : undefined,
          would_execute: Array.isArray(data.would_execute) ? data.would_execute : [],
        })
        toast.success("Dry-run report ready", {
          description: "Per-step tier + cost estimate shown above the tabs.",
        })
      } else {
        // Surface the just-started run's live activity rail inline.
        if (typeof data.run_id === "string" && data.run_id) setLastRunId(data.run_id)
        toast.success(`${actionLabel(action)} started`, {
          description: data.run_id
            ? `Run ${String(data.run_id).slice(0, 12)}…`
            : "Watch the activity below for live status",
        })
      }
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

  // Lifecycle governance: approve/reject a proposed routine (MANAGER+),
  // or disable/enable an existing one (OWNER/ADMIN). Each hits its own
  // endpoint, toasts the outcome, then refetches so the hero badge +
  // run-guard reflect the new status. enable/disable confirm first
  // (matches the rollback confirm() pattern in the Versions tab).
  const governanceAction = async (action: "approve" | "reject" | "disable" | "enable") => {
    if (!routine) return
    if (action === "disable" && !confirm(`Disable "${routine.name || routine.slug}"? It cannot be run until re-enabled.`)) {
      return
    }
    if (action === "reject" && !confirm(`Reject "${routine.name || routine.slug}"? The proposed routine is discarded.`)) {
      return
    }
    setBusyGov(action)
    try {
      const res = await apiFetch(`/api/v1/workspaces/${workspaceId}/pipelines/${slug}/${action}`, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
      })
      if (!res.ok) {
        const t = await res.text().catch(() => "")
        throw new Error(`${res.status}: ${t || res.statusText}`)
      }
      toast.success(governanceLabel(action))
      onChanged()
      fetchRoutine()
    } catch (e) {
      toast.error(`${governanceLabel(action)} failed`, {
        description: e instanceof Error ? e.message : String(e),
      })
    } finally {
      setBusyGov(null)
    }
  }

  const lifecycle = normalizeRoutineStatus(routine?.status)
  const lifecycleBadge = routineStatusBadge(routine?.status)
  const runGuard = runDisabledReason(routine?.status)
  const showApprovalBanner = lifecycle === "proposed" && canApproveRoutine(role)
  const showKillControl = canKillRoutine(role)

  const status = routine?.last_invocation_status?.toLowerCase()
  // Status tones share the same `bg-{c}-500/20 text-{c}-400` pattern
  // as lib/colors.ts STATUS_BADGE_CLASSES so the pill matches the
  // status pills rendered in Inbox / Issues / Activity.
  //
  // A live approval gate wins over the persisted last_invocation_status: the
  // run reads as "running" in the DB while parked, but the human is the
  // bottleneck, so we show the amber "Waiting for approval" state instead.
  const statusTone = pendingApproval
    ? { bg: "bg-amber-500/20", text: "text-amber-400", label: "Waiting for approval" }
    : status === "completed" || status === "succeeded" || status === "success"
      ? { bg: "bg-emerald-500/20", text: "text-emerald-400", label: "Last run · completed" }
      : status === "failed" || status === "error"
        ? { bg: "bg-rose-500/20", text: "text-rose-400", label: "Last run · failed" }
        : status === "running"
          ? { bg: "bg-blue-500/20", text: "text-blue-400", label: "Running…" }
          : { bg: "bg-muted", text: "text-muted-foreground", label: "Never invoked" }

  // Top-level tabs are collapsed to the three the redesign elevates
  // (Overview / Runs / Schedules); the four power-user surfaces
  // (Editor · Versions · Webhooks · Wait points) live behind a single
  // "Advanced" tab with its own sub-tab bar so the chrome reads as
  // "the routine" first and "the machinery" second.
  const [activeTab, setActiveTab] = useState("overview")
  const [advancedTab, setAdvancedTab] = useState("editor")

  return (
    <div className="flex h-full flex-col">
      {/* Hero — gradient title + slug + status pills + description + action group */}
      <div className="shrink-0 border-b border-border bg-card/40 px-6 pb-5 pt-6">
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
                  {lifecycleBadge && (
                    <span
                      className={cn(
                        "inline-flex items-center gap-1.5 rounded-full px-2.5 py-1 text-[11px] font-medium",
                        lifecycleBadge.bg,
                        lifecycleBadge.text,
                      )}
                    >
                      <span className={cn("h-1.5 w-1.5 rounded-full", lifecycleBadge.dot)} />
                      {lifecycleBadge.label}
                    </span>
                  )}
                  <span
                    className={cn(
                      "inline-flex items-center gap-1.5 rounded-full px-2.5 py-1 text-[11px] font-medium",
                      statusTone.bg,
                      statusTone.text,
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
          {/* Wrap in a span so the run-guard tooltip still shows when the
              button is disabled — disabled buttons swallow hover events. */}
          <span title={runGuard ?? "Invoke routine with empty inputs"} className="inline-flex">
            <Button
              onClick={() => triggerAction("run")}
              disabled={!!busyAction || !routine || !!runGuard}
              className="h-9 gap-2 rounded-md px-4 text-sm font-semibold"
            >
              {busyAction === "run" ? (
                <Spinner className="h-3.5 w-3.5" />
              ) : (
                <Play className="h-3.5 w-3.5 fill-current" />
              )}
              {busyAction === "run" ? "Running…" : "Run"}
            </Button>
          </span>
          {/* Wrap in a span so the explanatory tooltip still shows when the
              button is disabled — disabled buttons swallow hover events. */}
          <span
            title={
              runGuard ??
              (canTestRun(routine)
                ? "Run the draft definition on the execution tier; logs result without persisting state"
                : "Test run needs an author crew — only available for crew-authored routines")
            }
            className="inline-flex"
          >
            <Button
              variant="outline"
              onClick={() => triggerAction("test_run")}
              disabled={!!busyAction || !routine || !canTestRun(routine) || !!runGuard}
              className="h-9 gap-2 rounded-md px-4 text-sm"
            >
              <FlaskConical className="h-3.5 w-3.5" />
              {busyAction === "test_run" ? "Testing…" : "Test run"}
            </Button>
          </span>
          <span
            title={runGuard ?? "Walk DSL, render templates, compute would_execute report — no agent invocations"}
            className="inline-flex"
          >
            <Button
              variant="outline"
              onClick={() => triggerAction("dry_run")}
              disabled={!!busyAction || !routine || !!runGuard}
              className="h-9 gap-2 rounded-md px-4 text-sm"
            >
              <Eye className="h-3.5 w-3.5" />
              {busyAction === "dry_run" ? "Computing…" : "Dry run"}
            </Button>
          </span>
          <div className="flex-1" />
          {/* Enable / Disable — OWNER/ADMIN kill switch. Disable when the
              routine is active; Enable when it's disabled. Hidden for a
              proposed routine (approve/reject is the right action there). */}
          {showKillControl && routine && lifecycle !== "proposed" && (
            lifecycle === "disabled" ? (
              <Button
                variant="outline"
                onClick={() => governanceAction("enable")}
                disabled={!!busyGov || !!busyAction}
                className="h-9 gap-2 rounded-md px-3 text-sm text-emerald-400 hover:text-emerald-300"
                title="Re-enable this routine so it can run again"
              >
                {busyGov === "enable" ? <Spinner className="h-3.5 w-3.5" /> : <Power className="h-3.5 w-3.5" />}
                Enable
              </Button>
            ) : (
              <Button
                variant="outline"
                onClick={() => governanceAction("disable")}
                disabled={!!busyGov || !!busyAction}
                className="h-9 gap-2 rounded-md px-3 text-sm text-rose-400 hover:text-rose-300"
                title="Disable (kill) this routine — it cannot run until re-enabled"
              >
                {busyGov === "disable" ? <Spinner className="h-3.5 w-3.5" /> : <PowerOff className="h-3.5 w-3.5" />}
                Disable
              </Button>
            )
          )}
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

      {/* Approval banner — a proposed routine (risky / agent-authored)
          needs a MANAGER+ to promote it before it can run. Approve →
          active; Reject → discarded. Only rendered for MANAGER+ when the
          routine is in the proposed state. */}
      {showApprovalBanner && (
        <div className="flex items-center gap-3 border-b border-amber-500/30 bg-amber-500/[0.07] px-6 py-3">
          <div className="min-w-0 flex-1">
            <div className="text-sm font-medium text-amber-300">This routine is awaiting approval</div>
            <p className="mt-0.5 text-[12px] text-amber-200/70">
              It was proposed for review and can&apos;t run until a manager approves it.
            </p>
          </div>
          <Button
            size="sm"
            onClick={() => governanceAction("approve")}
            disabled={!!busyGov}
            className="h-8 gap-1.5 bg-amber-500 px-3 text-sm font-semibold text-amber-950 hover:bg-amber-400"
          >
            {busyGov === "approve" ? <Spinner className="h-3.5 w-3.5" /> : <Check className="h-3.5 w-3.5" />}
            Approve
          </Button>
          <Button
            size="sm"
            variant="outline"
            onClick={() => governanceAction("reject")}
            disabled={!!busyGov}
            className="h-8 gap-1.5 border-amber-500/40 px-3 text-sm text-amber-200 hover:text-amber-100"
          >
            {busyGov === "reject" ? <Spinner className="h-3.5 w-3.5" /> : <Ban className="h-3.5 w-3.5" />}
            Reject
          </Button>
        </div>
      )}

      {/* Dry-run report — surfaces would_execute when the user clicks
          "Dry run". Pre-fix this payload was silently dropped. */}
      {dryRunResult && (
        <RoutineDryRunReport result={dryRunResult} onClose={() => setDryRunResult(null)} />
      )}

      {/* Run activity — instant readable status for the just-triggered
          Run / Test run, so the user isn't left wondering what's happening
          after clicking. Full history stays in the Runs tab. */}
      {routine && lastRunId && (
        <div className="border-b border-white/[0.06]">
          {pendingApproval && (
            <div className="px-4 pt-3">
              <RoutineApprovalBanner
                waitpoint={pendingApproval}
                deciding={decidingApproval}
                onDecide={decideApproval}
              />
            </div>
          )}
          <PipelineRunActivity
            workspaceId={workspaceId}
            slug={routine.slug}
            runId={lastRunId}
            awaiting={
              pendingApproval
                ? { stepId: pendingApproval.step_id, ts: pendingApproval.created_at }
                : null
            }
          />
        </div>
      )}

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
            <TabBar.Item value="runs" className="h-10 text-sm">Runs</TabBar.Item>
            <TabBar.Item value="schedules" className="h-10 text-sm">Schedules</TabBar.Item>
            <TabBar.Item value="advanced" className="h-10 text-sm">Advanced</TabBar.Item>
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
            <TabsContent value="runs" className="m-0 px-6 py-5">
              <RoutineRunsTab workspaceId={workspaceId} slug={routine.slug} />
            </TabsContent>
            <TabsContent value="schedules" className="m-0 px-6 py-5">
              <RoutineSchedulesTab workspaceId={workspaceId} pipelineId={routine.id} slug={routine.slug} />
            </TabsContent>
            {/* Advanced — power-user machinery behind a sub-tab bar so the
                top-level chrome stays at three approachable surfaces. */}
            <TabsContent value="advanced" className="m-0 flex h-full flex-col p-0">
              <Tabs
                value={advancedTab}
                onValueChange={setAdvancedTab}
                className="flex flex-1 flex-col overflow-hidden"
              >
                <TabBar
                  value={advancedTab}
                  onValueChange={setAdvancedTab}
                  layoutId="routine-advanced-tabs-indicator"
                  ariaLabel="Advanced routine sections"
                  className="shrink-0 border-b border-border/60 px-4"
                >
                  <TabBar.Item value="editor" className="h-9 text-[13px]">Editor / JSON</TabBar.Item>
                  <TabBar.Item value="versions" className="h-9 text-[13px]">Versions</TabBar.Item>
                  <TabBar.Item value="webhooks" className="h-9 text-[13px]">Webhooks</TabBar.Item>
                  <TabBar.Item value="waitpoints" className="h-9 text-[13px]">Wait points</TabBar.Item>
                </TabBar>
                <div className="flex-1 overflow-auto">
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
                  <TabsContent value="webhooks" className="m-0 px-6 py-5">
                    <RoutineWebhooksTab workspaceId={workspaceId} pipelineId={routine.id} slug={routine.slug} />
                  </TabsContent>
                  <TabsContent value="waitpoints" className="m-0 px-6 py-5">
                    <RoutineWaitpointsTab workspaceId={workspaceId} slug={routine.slug} />
                  </TabsContent>
                </div>
              </Tabs>
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

function governanceLabel(a: "approve" | "reject" | "disable" | "enable"): string {
  switch (a) {
    case "approve":
      return "Routine approved"
    case "reject":
      return "Routine rejected"
    case "disable":
      return "Routine disabled"
    case "enable":
      return "Routine enabled"
  }
}
