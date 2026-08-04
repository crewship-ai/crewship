"use client"

import { useEffect, useRef, useState } from "react"
import { useRouter } from "next/navigation"
import { Play, Eye, Square, Check, Ban } from "lucide-react"
import { Spinner } from "@/components/ui/spinner"
import { Button } from "@/components/ui/button"
import { cn } from "@/lib/utils"
import { STATUS_BADGE_CLASSES, STATUS_DOT_CLASSES } from "@/lib/colors"
import { AgentlessBadge } from "./routine-agentless-badge"
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
import { buildPipelineActionRequest } from "@/lib/pipeline-actions"
import { usePipelineRunRecords, isActiveRunStatus } from "@/hooks/use-pipeline-run-records"
import { integrationLabel, extractMissingIntegrations } from "@/lib/integration-labels"
import { credentialTypeLabel, extractMissingCredentials } from "@/lib/credential-labels"
import { extractProblemDetail } from "@/lib/problem-details"
import { PipelineRunActivity } from "@/components/features/activity/pipeline-run-activity"
import { usePendingApproval } from "@/hooks/use-pending-approval"
import { RoutineApprovalBanner } from "@/components/features/routines/routine-approval-banner"
import { RoutineActionsMenu } from "./routine-actions-menu"
import { RoutineCardDetail } from "./routine-card-detail"
import { RoutineDryRunReport, type DryRunResult } from "./routine-dry-run-report"
import { isAgentless, type RoutineManifest } from "@/lib/routine-flow"

// RoutinesDetailPanel — right-side detail for the selected routine.
// Hosts the seven sub-tabs (Overview, Editor, Runs, Versions,
// Schedules, Webhooks, Waitpoints) plus the action toolbar
// (Run / DryRun / Cancel). Subscribes to the same routine state the
// list view reads, so refresh after a successful Run is already
// covered by usePipelines' WS subscription in the layout.

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
  // Presentation, stored in columns of its own so recolouring a routine
  // never touches definition_json — which is hashed, versioned, and what
  // a save_token binds to. Absent = unset; the UI derives a stable icon
  // from the slug instead.
  icon?: string
  color?: string
  // Why this routine is `proposed`, from the risk classifier. Present
  // only while a proposal is open. Without it the reviewer is asked to
  // approve something they cannot see.
  risk_reasons?: string[]
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
  // lastRunId holds the run_id of the most recent Run so we can show its
  // live activity rail inline (instant status after clicking).
  const [lastRunId, setLastRunId] = useState<string | null>(null)
  // cancelling gates the header Cancel button while its POST is in
  // flight (same pattern as busyAction for Run / Dry run).
  const [cancelling, setCancelling] = useState(false)
  // Bumped by the kebab's "Edit definition". A counter rather than a
  // boolean so asking twice reopens the editor after the user closed it.
  const [editRequest, setEditRequest] = useState(0)
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

  // Live run records for THIS routine power the header Cancel button.
  // The hook already refreshes on pipeline.run.* WS events, so the
  // button's enabled state tracks run starts/finishes without polling.
  const { records: runRecords, refresh: refreshRunRecords } = usePipelineRunRecords(workspaceId, slug)
  const activeRuns = runRecords.filter((r) => isActiveRunStatus(r.status))
  // Prefer the run this panel just started (lastRunId); otherwise a
  // lone active run is unambiguous. Several active runs with no known
  // lastRunId → don't guess, send the user to the Runs tab to pick.
  const cancelTarget =
    activeRuns.find((r) => r.id === lastRunId) ?? (activeRuns.length === 1 ? activeRuns[0] : undefined)

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

  const triggerAction = async (action: "run" | "dry_run") => {
    if (!routine) return
    setBusyAction(action)
    try {
      // Run / Dry run both address the saved pipeline by slug — `run` executes
      // for real, `dry_run` is a static preview. See lib/pipeline-actions.
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
          const detail = extractProblemDetail(parsed)
          const missing = extractMissingIntegrations(parsed)
          if (missing.length > 0) {
            const labels = missing.map(integrationLabel)
            toast.error(
              `This routine needs the ${labels.join(", ")} integration${labels.length > 1 ? "s" : ""} — not connected for this crew`,
              {
                description:
                  detail ?? "Connect the missing integration for the crew that runs this routine, then run it again.",
                action: {
                  label: "Manage integrations",
                  onClick: () => router.push("/integrations"),
                },
                duration: 10000,
              },
            )
            return
          }
          // Same actionable UX for a missing vault credential (422 carries
          // `missing_credentials` instead) — name the credential type and
          // point at the vault, never a raw JSON toast.
          const missingCreds = extractMissingCredentials(parsed)
          if (missingCreds.length > 0) {
            const labels = missingCreds.map(credentialTypeLabel)
            toast.error(
              `This routine needs ${labels.length > 1 ? "" : "a "}${labels.join(", ")} credential${labels.length > 1 ? "s" : ""} — not in this crew's vault`,
              {
                description:
                  detail ?? "Add the missing credential to the crew that runs this routine, then run it again.",
                action: {
                  label: "Manage credentials",
                  onClick: () => router.push("/credentials"),
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
          // manifest is the declared blast radius the redefined dry_run returns
          // alongside the step plan. Pass it through untyped-guarded; the report
          // tolerates its absence (older server builds) via isManifestEmpty.
          manifest:
            data.manifest && typeof data.manifest === "object"
              ? (data.manifest as RoutineDetail["manifest"])
              : undefined,
        })
        toast.success("Plan preview ready", {
          description: "Step plan + declared resources shown above the tabs.",
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

  // Cancel the routine's active run. Targets cancelTarget (the run
  // this panel started, or the lone active run); when several runs are
  // active and none is ours, deep-link to the Runs tab where each row
  // has its own cancel button. RBAC: manage-tier — MEMBERs get a 403.
  const cancelActiveRun = async () => {
    if (!cancelTarget) {
      // No tab to send them to any more. The per-run cancel buttons are
      // in the Runs card's Manage view, on this same page.
      toast.info("Multiple runs are active — open Runs → Manage and cancel the one you mean")
      return
    }
    setCancelling(true)
    try {
      const res = await apiFetch(
        `/api/v1/workspaces/${workspaceId}/pipelines/runs/${cancelTarget.id}/cancel`,
        { method: "POST" },
      )
      if (!res.ok) {
        if (res.status === 403) {
          throw new Error("You don't have permission to cancel runs (manager role or above required)")
        }
        const t = await res.text().catch(() => "")
        throw new Error(`${res.status}: ${t || res.statusText}`)
      }
      toast.success("Cancel requested", {
        description: `Run ${cancelTarget.id.slice(0, 12)}… will stop at the next step boundary.`,
      })
      refreshRunRecords()
      onChanged()
      fetchRoutine()
    } catch (e) {
      toast.error("Cancel failed", {
        description: e instanceof Error ? e.message : String(e),
      })
    } finally {
      setCancelling(false)
    }
  }

  const lifecycle = normalizeRoutineStatus(routine?.status)
  const lifecycleBadge = routineStatusBadge(routine?.status)
  const runGuard = runDisabledReason(routine?.status)
  const showApprovalBanner = lifecycle === "proposed" && canApproveRoutine(role)
  const showKillControl = canKillRoutine(role)

  const status = routine?.last_invocation_status?.toLowerCase()
  // Run-status pill routes its colors through the shared palette
  // (lib/colors STATUS_BADGE_CLASSES + STATUS_DOT_CLASSES) so it matches
  // the status pills rendered in Inbox / Issues / Activity — failed reads
  // red (not rose), running reads cyan (IN_PROGRESS), and an approval gate
  // reads violet (AWAITING_APPROVAL).
  //
  // A live approval gate wins over the persisted last_invocation_status: the
  // run reads as "running" in the DB while parked, but the human is the
  // bottleneck, so we show the awaiting-approval state instead.
  const runStatus: { token: string; label: string } = pendingApproval
    ? { token: "AWAITING_APPROVAL", label: "Waiting for approval" }
    : status === "completed" || status === "succeeded" || status === "success"
      ? { token: "COMPLETED", label: "Last run · completed" }
      : status === "failed" || status === "error"
        ? { token: "FAILED", label: "Last run · failed" }
        : status === "running"
          ? { token: "IN_PROGRESS", label: "Running…" }
          : { token: "PENDING", label: "Never invoked" }

  // Top-level tabs are collapsed to the three the redesign elevates
  // (Overview / Runs / Schedules); the four power-user surfaces
  // (Editor · Versions · Webhooks · Wait points) live behind a single
  // "Advanced" tab with its own sub-tab bar so the chrome reads as
  // "the routine" first and "the machinery" second.

  return (
    <div className="flex h-full flex-col">
      {/* The identity used to live here as a fixed band above the scroll
          area. It is a card now, inside the scroll, so the page reads as
          one stack of cards and the name scrolls with what it names. The
          panel keeps the ACTION handlers — RBAC guards, busy states, run
          guards — and hands them down as a node. */}
      {loading && (
        <div className="space-y-2 p-6">
          <div className="h-3 w-32 animate-pulse rounded bg-muted/30" />
          <div className="h-8 w-72 animate-pulse rounded bg-muted/40" />
          <div className="h-3 w-96 animate-pulse rounded bg-muted/20" />
        </div>
      )}

      {/* Approval banner — a proposed routine (risky / agent-authored)
          needs a MANAGER+ to promote it before it can run. Approve →
          active; Reject → discarded. Only rendered for MANAGER+ when the
          routine is in the proposed state. */}
      {showApprovalBanner && (
        <div className="flex flex-wrap items-center gap-x-3 gap-y-2 border-b border-warn/30 bg-warn/[0.07] px-6 py-3">
          <div className="min-w-0 flex-1">
            <div className="text-sm font-medium text-warn">This routine is awaiting approval</div>
            {/* The reasons the classifier gave. Without them the banner
                asks a manager to approve something they cannot see —
                and the reasons already existed, written into the inbox
                item at save time; nothing read them back. */}
            {routine?.risk_reasons && routine.risk_reasons.length > 0 ? (
              <p className="mt-0.5 text-[12px] text-warn/80">
                Flagged because it {routine.risk_reasons.join(", ")}.
              </p>
            ) : (
              <p className="mt-0.5 text-[12px] text-warn/70">
                It was proposed for review and can&apos;t run until a manager approves it.
              </p>
            )}
            <a
              href="/inbox"
              className="mt-1 inline-flex items-center gap-1 text-[11px] text-warn underline-offset-2 hover:underline"
            >
              Open the review item in Inbox
            </a>
          </div>
          <div className="flex shrink-0 items-center gap-2">
            <Button
              size="sm"
              onClick={() => governanceAction("approve")}
              disabled={!!busyGov || !!busyAction}
              className="h-8 gap-1.5 bg-warn px-3 text-sm font-semibold text-background hover:bg-warn/90"
            >
              {busyGov === "approve" ? <Spinner className="h-3.5 w-3.5" /> : <Check className="h-3.5 w-3.5" />}
              Approve
            </Button>
            <Button
              size="sm"
              variant="outline"
              onClick={() => governanceAction("reject")}
              disabled={!!busyGov || !!busyAction}
              className="h-8 gap-1.5 px-3 text-sm"
            >
              <Ban className="h-3.5 w-3.5" />
              Reject
            </Button>
          </div>
        </div>
      )}

      {/* Dry-run report — surfaces would_execute when the user clicks
          "Dry run". Pre-fix this payload was silently dropped. */}
      {dryRunResult && (
        <RoutineDryRunReport result={dryRunResult} onClose={() => setDryRunResult(null)} />
      )}

      {/* Run activity — instant readable status for the just-triggered
          Run, so the user isn't left wondering what's happening
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

      {/* One scrolling surface of cards, no tabs.
          A tab is a hiding place: 38 routines had zero schedules between
          them while Schedules was a tab nobody clicked. Everything that
          worked is still here — the editor opens beside the graph,
          schedules and webhooks live inside Triggers, versions have
          their own card. What went is the filing, not the machinery.
          Wait points went to Activity, where the run they belong to is. */}
      {routine && (
        <div className="flex flex-1 flex-col overflow-hidden">
          {error && (
            <div className="m-4 rounded-md border border-destructive/30 bg-destructive/5 px-3 py-2 text-sm text-destructive">
              {error}
            </div>
          )}
          <div className="flex-1 overflow-auto">
            <RoutineCardDetail
              routine={routine}
              workspaceId={workspaceId}
              onChanged={() => {
                fetchRoutine()
                onChanged()
              }}
              editRequest={editRequest}
              statusPills={
                <>
                  {lifecycleBadge && (
                    <span
                      className={cn(
                        "inline-flex items-center gap-1.5 rounded-full px-2.5 py-0.5 type-meta font-medium",
                        lifecycleBadge.className,
                      )}
                    >
                      <span className={cn("h-1.5 w-1.5 rounded-full", lifecycleBadge.dot)} />
                      {lifecycleBadge.label}
                    </span>
                  )}
                  <span
                    className={cn(
                      "inline-flex items-center gap-1.5 rounded-full px-2.5 py-0.5 type-meta font-medium",
                      STATUS_BADGE_CLASSES[runStatus.token],
                    )}
                  >
                    <span className={cn("h-1.5 w-1.5 rounded-full", STATUS_DOT_CLASSES[runStatus.token])} />
                    {runStatus.label}
                  </span>
                  <AgentlessBadge agentless={isAgentless(routine.definition)} />
                </>
              }
              actions={
                <>
                  {/* Wrapped in a span so the run-guard tooltip still
                      shows on a disabled button — disabled buttons
                      swallow hover events. */}
                  <span title={runGuard ?? "Invoke routine with empty inputs"} className="inline-flex">
                    <Button
                      onClick={() => triggerAction("run")}
                      disabled={!!busyAction || !!runGuard}
                      className="h-8 gap-1.5 rounded-lg px-3 text-[12px] font-medium"
                    >
                      {busyAction === "run" ? (
                        <Spinner className="h-3.5 w-3.5" />
                      ) : (
                        <Play className="h-3.5 w-3.5 fill-current" />
                      )}
                      {busyAction === "run" ? "Running…" : "Run"}
                    </Button>
                  </span>
                  <span
                    title={runGuard ?? "Static plan preview — walks the DSL + shows declared resources; no agents invoked"}
                    className="inline-flex"
                  >
                    <Button
                      variant="outline"
                      onClick={() => triggerAction("dry_run")}
                      disabled={!!busyAction || !!runGuard}
                      className="h-8 gap-1.5 rounded-lg px-3 text-[12px] font-medium"
                    >
                      <Eye className="h-3.5 w-3.5" />
                      {busyAction === "dry_run" ? "Computing…" : "Dry run"}
                    </Button>
                  </span>
                  {/* Cancel stays a visible button, not a menu item. An
                      active run is precisely when you need it, and one
                      click deeper is the wrong direction for the action
                      that stops something already burning tokens. */}
                  <span
                    title={
                      activeRuns.length === 0
                        ? "No active run to cancel"
                        : cancelTarget
                          ? `Cancel run ${cancelTarget.id.slice(0, 12)}…`
                          : "Multiple runs are active — open Runs → Manage and pick one"
                    }
                    className="inline-flex"
                  >
                    <Button
                      variant="ghost"
                      className="h-8 gap-1.5 rounded-lg px-3 text-[12px] font-medium text-muted-foreground hover:text-destructive"
                      onClick={cancelActiveRun}
                      disabled={cancelling || activeRuns.length === 0}
                    >
                      {cancelling ? <Spinner className="h-3.5 w-3.5" /> : <Square className="h-3.5 w-3.5" />}
                      Cancel
                    </Button>
                  </span>
                  <RoutineActionsMenu
                    routine={routine}
                    workspaceId={workspaceId}
                    onEditCode={() => setEditRequest((n) => n + 1)}
                    onChanged={() => {
                      fetchRoutine()
                      onChanged()
                    }}
                    onClose={onClose}
                    lifecycle={lifecycle}
                    showKillControl={showKillControl}
                    onGovernance={governanceAction}
                    governanceBusy={!!busyGov || !!busyAction}
                  />
                </>
              }
            />
          </div>
        </div>
      )}
    </div>
  )
}

function actionLabel(a: "run" | "dry_run"): string {
  return a === "run" ? "Run" : "Dry run"
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
