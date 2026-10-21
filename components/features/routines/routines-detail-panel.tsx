"use client"

import { formatRoutineTime } from "@/lib/routine-time"
import { useEffect, useRef, useState } from "react"
import { useRouter } from "next/navigation"
import { Play, Check, Ban, Inbox, Eye, Upload, Power } from "lucide-react"
import { Button } from "@/components/ui/button"
import { StatusPill } from "@/components/ui/status-pill"
import { ConfirmDialog } from "@/components/ui/confirm-dialog"
import { SubBarPrimary, SubBarSecondary } from "@/components/layout/sub-bar"
import { PageError, PageNoAccess, PageNotFound } from "@/components/layout/page-state"
import {
  CreateSurface,
  CreateSurfaceBody,
  CreateSurfaceFooter,
  CreateSurfaceHeader,
} from "@/components/layout/create-surface"
import type { StatusTone } from "@/lib/format-status"
import { AgentlessBadge } from "./routine-agentless-badge"
import { toast } from "@/lib/toast"
import { apiFetch } from "@/lib/api-fetch"
import { encodeApiPathSegment } from "@/lib/api-path-segment"
import { loadRoutineDraft, RoutineDraftError } from "@/lib/routine-drafts"
import { RoutineStartIntent } from "@/lib/routine-start-intent"
import { useAbilities } from "@/hooks/use-abilities"
import {
  runDisabledReason,
  canApproveRoutine,
  canKillRoutine,
  normalizeRoutineStatus,
  roleAtLeast,
  routinePermissions,
  BREAKER_ON_AGAIN,
  BREAKER_OFF_HINT,
} from "@/lib/routine-governance"
import { buildPipelineActionRequest } from "@/lib/pipeline-actions"
import { routineInputSpecs, type RoutineInputSpec } from "@/lib/routine-inputs"
import { RoutineRunInputsDialog } from "./routine-run-inputs-dialog"
import {
  usePipelineRunRecords,
  isActiveRunStatus,
} from "@/hooks/use-pipeline-run-records"
import { usePipelineSchedules } from "@/hooks/use-pipeline-schedules"
import type { Pipeline } from "@/hooks/use-pipelines"
import { integrationLabel, extractMissingIntegrations } from "@/lib/integration-labels"
import { credentialTypeLabel, extractMissingCredentials } from "@/lib/credential-labels"
import { extractProblemDetail } from "@/lib/problem-details"
import { PipelineRunActivity } from "@/components/features/activity/pipeline-run-activity"
import { usePendingApproval } from "@/hooks/use-pending-approval"
import { RoutineApprovalBanner } from "@/components/features/routines/routine-approval-banner"
import { RoutineActionsMenu } from "./routine-actions-menu"
import { RoutineProposalAsk, type RoutineAskDefinition } from "./routine-proposal-ask"
import { RoutinePage, type RoutinePageActionContext } from "./routine-page"
import { RoutineLiveRunBanner } from "./routine-live-run-banner"
import { RoutineDryRunReport, type DryRunResult } from "./routine-dry-run-report"
import { isAgentless, type RoutineManifest } from "@/lib/routine-flow"
import { useAccessMe } from "@/hooks/use-access-me"

// RoutinesDetailPanel — loads the selected routine and owns what acts on it:
// Run (with its access guard and input form), Test (a dry run), Stop, the
// governance actions (approve, reject, turn off and on) and Turn back on for a
// schedule the circuit breaker paused. It hands them to the routine page
// (routine-page.tsx) as the top-bar actions and banners.

export interface RoutineDetail {
  behavior?: import("@/lib/routine-behavior").RoutineBehavior
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
  // The review row to deep-link the banner at. The server builds the
  // id from (kind, source_id) inside the inbox writer; reconstructing
  // it here would be a second copy of that rule.
  inbox_item_id?: string
  // A saved draft newer than the published version (contract §"API
  // additions"). Absent when nothing is drafted, and on older servers.
  draft?: import("@/hooks/use-pipelines").PipelineDraftSummary
  /**
   * The slug has a saved draft and no published routine yet — a copy, a
   * draft the lead saved, or one saved from the CLI. The page is built from
   * the draft document; Run waits for the first publication.
   */
  draft_only?: boolean
  // Files the recipe runs — every steps[].script.path with its language,
  // the steps that use it, size and whether it is on the crew share. `[]`
  // when nothing is declared; absent on older servers, where the recipe's own
  // script paths stand in (lib/routine-files.ts).
  files?: import("@/lib/routine-files").RoutineFile[]
}

interface Props {
  workspaceId: string
  slug: string
  onClose: () => void
  onChanged: () => void
  onRunStarted?: (runId: string) => void
  /** The crew's name for the header line ("Ops crew · runs as …"). */
  crewName?: string
  /** Every routine, for the step editor's "Run another routine". */
  routines?: Pipeline[]
  /** The top bar's action slot; the page actions render there. */
  /** Put the page actions in the SubBar above (SubBarActions). */
  actionsInBar?: boolean
  /** Opens another routine (a duplicate saved as a draft). */
  onOpenRoutine?: (slug: string) => void
}

type GovernanceAction = "approve" | "reject" | "disable" | "enable"

export function RoutinesDetailPanel({
  workspaceId,
  slug,
  onClose,
  onChanged,
  onRunStarted,
  crewName,
  routines,
  actionsInBar,
  onOpenRoutine,
}: Props) {
  const router = useRouter()
  const startIntent = useRef(new RoutineStartIntent())
  const { role, capabilities } = useAbilities()
  const [confirmGov, setConfirmGov] = useState<GovernanceAction | null>(null)
  const [deleteRequest, setDeleteRequest] = useState(0)
  const [testResult, setTestResult] = useState<DryRunResult | null>(null)
  const [fetchStatus, setFetchStatus] = useState<number | null>(null)
  const workspaceSegment = encodeApiPathSegment(workspaceId)
  const routineSegment = encodeApiPathSegment(slug)
  const { schedules, update: updateSchedule } = usePipelineSchedules(workspaceSegment && routineSegment ? workspaceId : null)
  const { access: effectiveAccess, loading: accessLoading, error: accessError, refresh: refreshAccess } = useAccessMe(
    workspaceSegment && routineSegment ? `/api/v1/workspaces/${workspaceSegment}/pipelines/${routineSegment}/access/me` : undefined,
  )
  const runAccess = effectiveAccess?.actions.run
  const mayRun = runAccess?.state === "allowed" || runAccess?.state === "conditional"
  const [routine, setRoutine] = useState<RoutineDetail | null>(null)
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)
  const [busyAction, setBusyAction] = useState<string | null>(null)
  // Tracks the in-flight governance action (approve/reject/disable/enable)
  // so its button shows a spinner and the others stay disabled meanwhile.
  const [busyGov, setBusyGov] = useState<string | null>(null)
  // lastRunId holds the run_id of the most recent Run so we can show its
  // live activity rail inline (instant status after clicking).
  const [lastRunId, setLastRunId] = useState<string | null>(null)
  // cancelling gates the banner's Stop while its POST is in flight (same
  // pattern as busyAction for Run).
  const [cancelling, setCancelling] = useState(false)
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

  // Live run records for THIS routine power the live-run banner under the
  // header. The hook already refreshes on pipeline.run.* WS events, so the
  // banner tracks run starts/finishes without polling.
  const { records: runRecords, refresh: refreshRunRecords } = usePipelineRunRecords(
    workspaceSegment && routineSegment ? workspaceId : null,
    workspaceSegment && routineSegment ? slug : null,
  )
  const activeRuns = runRecords.filter((r) => isActiveRunStatus(r.status))

  const fetchRoutine = async () => {
    abortRef.current?.abort()
    const ctrl = new AbortController()
    abortRef.current = ctrl
    setLoading(true)
    setError(null)
    setFetchStatus(null)
    try {
      if (!workspaceSegment || !routineSegment) throw new Error("Routine unavailable: invalid identifier")
      const res = await apiFetch(`/api/v1/workspaces/${workspaceSegment}/pipelines/${routineSegment}`, {
        signal: ctrl.signal,
      })
      if (ctrl.signal.aborted) return
      if (!res.ok) setFetchStatus(res.status)
      if (res.status === 404) {
        // Not published yet: a draft alone can still be read, edited and
        // published from this page (publishing a draft creates the routine).
        const fromDraft = await routineFromDraftOnly(workspaceId, slug, ctrl.signal)
        if (ctrl.signal.aborted) return
        if (fromDraft) {
          setRoutine(fromDraft)
          return
        }
      }
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
    setLastRunId(null)
    // And close any open input form. It belongs to the routine that was
    // selected when Run was pressed, and the panel has just been pointed
    // at a different one — see the note on pendingRun.
    setPendingRun(null)
    fetchRoutine()
    return () => {
      abortRef.current?.abort()
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [workspaceId, slug])

  // Run asks for the routine's inputs first, when it declares any.
  //
  // The slug is carried WITH the specs, and both halves matter. This panel
  // is a detail view over a list: picking another row changes `slug` under
  // a dialog that is already open, and `triggerAction` posts to whatever
  // `slug` is at submit time. Holding only the specs meant the form could
  // show routine A's fields, above routine B's name, and start routine B
  // with A's values — the wrong routine, with inputs it never declared.
  //
  // Keyed on workspace AND slug, and guarded twice: the effect above
  // closes the form on a selection change, and the submit below refuses a
  // mismatch anyway. The first is the behaviour; the second is what makes a
  // missed re-render harmless instead of expensive, and it only earns that
  // description by matching every dependency the effect has.
  const [pendingRun, setPendingRun] = useState<{
    workspaceId: string
    slug: string
    inputs: RoutineInputSpec[]
    /** Test asks the same questions, then dry-runs. */
    mode?: "run" | "test" | "later"
  } | null>(null)

  // The Run button's two paths. A routine with declared inputs opens the
  // form; one without runs immediately, which is what that button has
  // always done and must go on doing.
  const startRun = () => {
    if (!mayRun) return
    // The loaded routine and the selected slug can disagree. `fetchRoutine`
    // sets `loading` but leaves `routine` on the PREVIOUS one until the new
    // response lands, and the toolbar renders under `{routine && …}` — so
    // for the width of that fetch the button is live while the panel is
    // showing the last routine's definition. Opening the form there would
    // build it from routine A's inputs and post it to routine B, which is
    // the same defect as a stale dialog reached through a different door.
    if (!routine || routine.slug !== slug) return
    const specs = routineInputSpecs(routine.definition)
    setPendingRun({ workspaceId, slug, inputs: specs })
  }

  const triggerAction = async (action: "run", inputs: Record<string, unknown> = {}) => {
    if (!routine || routine.slug !== slug) return
    const { url, body } = buildPipelineActionRequest(
      workspaceId,
      slug,
      action,
      routine,
      inputs,
    )
    const attempt = startIntent.current.begin(url, body)
    if (!attempt) return
    let accepted = false
    setBusyAction(action)
    try {
      // Addresses the saved pipeline by slug. Dry run used to share this
      // path; its panel is gone — two thirds of it repeated the graph
      // and the Access card, and its one unique fact, the model each
      // step resolves to, now sits on the node it describes.
      const res = await apiFetch(url, {
        method: "POST",
        headers: {
          "Content-Type": "application/json",
          Prefer: "respond-async",
          "Idempotency-Key": attempt.key,
        },
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
                  detail ??
                  "Connect the missing integration for the crew that runs this routine, then run it again.",
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
                  detail ??
                  "Add the missing credential to the crew that runs this routine, then run it again.",
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
      const data = await res.json()
      if (typeof data.run_id !== "string" || !data.run_id)
        throw new Error(
          "The start could not be confirmed. Retry to recover the same run.",
        )
      accepted = true
      {
        // Surface the just-started run's live activity rail inline.
        if (typeof data.run_id === "string" && data.run_id) {
          setLastRunId(data.run_id)
          onRunStarted?.(data.run_id)
        }
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
      attempt.finish(accepted)
      setBusyAction(null)
    }
  }

  // Lifecycle governance: approve/reject a proposed routine (MANAGER+),
  // or disable/enable an existing one (OWNER/ADMIN). Each hits its own
  // endpoint, toasts the outcome, then refetches so the hero badge +
  // run-guard reflect the new status. enable/disable confirm first
  // (matches the rollback confirm() pattern in the Versions tab).
  // Approve is the reviewer's one click; Reject, Turn off and Turn on ask
  // first, in the shared confirmation dialog.
  const requestGovernance = (action: GovernanceAction) => {
    if (action === "approve") void governanceAction(action)
    else setConfirmGov(action)
  }

  /** Resolves true once the server accepted the action. */
  const governanceAction = async (action: GovernanceAction): Promise<boolean> => {
    if (!routine) return false
    setBusyGov(action)
    try {
      if (!workspaceSegment || !routineSegment) throw new Error("Routine unavailable: invalid identifier")
      const res = await apiFetch(
        `/api/v1/workspaces/${workspaceSegment}/pipelines/${routineSegment}/${action}`,
        {
          method: "POST",
          headers: { "Content-Type": "application/json" },
        },
      )
      if (!res.ok) {
        const t = await res.text().catch(() => "")
        throw new Error(`${res.status}: ${t || res.statusText}`)
      }
      toast.success(governanceLabel(action))
      onChanged()
      fetchRoutine()
      void refreshAccess()
      return true
    } catch (e) {
      toast.error(`${governanceFailedLabel(action)}`, {
        description: e instanceof Error ? e.message : String(e),
      })
      return false
    } finally {
      setBusyGov(null)
    }
  }

  // Test: the saved routine without side effects (POST …/dry_run, Manager and
  // above). Asks for the inputs first when the routine declares any.
  const runTest = async (inputs: Record<string, unknown> = {}) => {
    if (!routine || routine.slug !== slug) return
    const { url, body } = buildPipelineActionRequest(workspaceId, slug, "dry_run", routine, inputs)
    setBusyAction("test")
    try {
      const res = await apiFetch(url, { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify(body) })
      const data = await res.json().catch(() => null)
      if (!res.ok || !data) throw new Error((data && (data.error || extractProblemDetail(data))) || `The test was refused (${res.status}).`)
      setTestResult(data as DryRunResult)
    } catch (e) {
      toast.error("Test failed", { description: e instanceof Error ? e.message : String(e) })
    } finally {
      setBusyAction(null)
    }
  }
  // Test opens the same dialog on Test run (v67 D-RTRUN).
  const startTest = () => {
    if (!routine || routine.slug !== slug) return
    setPendingRun({ workspaceId, slug, inputs: routineInputSpecs(routine.definition), mode: "test" })
  }

  // Later: a one-time start. The server holds it until fire_at and lists it
  // with the routine's triggers, where it can be cancelled.
  const planStart = async (inputs: Record<string, unknown>, fireAt: string, pinnedVersion: number | null) => {
    if (!routine || routine.slug !== slug) return
    setBusyAction("later")
    try {
      const res = await apiFetch(`/api/v1/workspaces/${encodeURIComponent(workspaceId)}/pipelines/${encodeURIComponent(slug)}/run`, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ inputs, fire_at: fireAt, ...(pinnedVersion != null ? { pinned_version: pinnedVersion } : {}) }),
      })
      if (!res.ok) {
        const data = await res.json().catch(() => null)
        throw new Error((data && (data.error || extractProblemDetail(data))) || `The start was refused (${res.status}).`)
      }
      toast.success("Start planned", { description: `${formatRoutineTime(fireAt)} · cancel it in Triggers` })
      onChanged()
    } catch (e) {
      toast.error("Could not plan the start", { description: e instanceof Error ? e.message : String(e) })
    } finally {
      setBusyAction(null)
    }
  }

  // A schedule the circuit breaker turned off. Turning it back on is the
  // schedule's own PATCH (Owner/Admin); the failure count restarts at zero
  // when it is on again (updateSchedule).
  const breaker = schedules.find(
    (s) => (s.target_pipeline_id === routine?.id || s.target_pipeline_slug === slug) && !s.enabled && s.disabled_reason === "circuit_breaker",
  )
  const turnBackOn = async () => {
    if (!breaker) return
    setBusyGov("enable")
    try {
      await updateSchedule(breaker.id, { cron_expr: breaker.cron_expr, enabled: true })
      toast.success("Schedule is on again", { description: BREAKER_ON_AGAIN })
      onChanged()
    } catch (e) {
      toast.error("Could not turn the schedule back on", { description: e instanceof Error ? e.message : String(e) })
    } finally {
      setBusyGov(null)
    }
  }

  // Stop one active run — the banner names which. Stop is the one word for
  // it everywhere (contract vocabulary); the server route is still /cancel.
  // RBAC: manage-tier — MEMBERs get a 403.
  const stopRun = async (runId: string) => {
    setCancelling(true)
    try {
      const res = await apiFetch(
        `/api/v1/workspaces/${workspaceId}/pipelines/runs/${runId}/cancel`,
        { method: "POST" },
      )
      if (!res.ok) {
        if (res.status === 403) {
          throw new Error(
            "Stopping runs needs Owner or Admin.",
          )
        }
        const t = await res.text().catch(() => "")
        throw new Error(`${res.status}: ${t || res.statusText}`)
      }
      toast.success("Stop requested", {
        description: "Pending work stops at the next step boundary. What already happened is not undone.",
      })
      refreshRunRecords()
      onChanged()
      fetchRoutine()
    } catch (e) {
      toast.error("Stop failed", {
        description: e instanceof Error ? e.message : String(e),
      })
    } finally {
      setCancelling(false)
    }
  }

  const lifecycle = normalizeRoutineStatus(routine?.status)
  const runGuard = accessLoading || !runAccess
    ? accessError ? "Your access could not be determined" : "Checking your access…"
    : runAccess.state === "denied"
    ? runAccess.reason === "missing_role_or_capability"
      ? "Running requires a manager role or an explicit Run routines permission"
      : `Run unavailable: ${runAccess.reason.replaceAll("_", " ")}`
    : routine?.draft_only
    ? "Publish the draft first — nothing can run until then"
    : runDisabledReason(routine?.status)
  const canEdit = roleAtLeast(role, "MANAGER")
  const permissions = routinePermissions(role, capabilities)
  const showApproval = lifecycle === "proposed" && canApproveRoutine(role)
  const draftOnly = !!routine?.draft_only
  const published = !!routine && !draftOnly && (routine.head_version ?? 0) > 0

  const latestOutcome = runRecords[0]?.outcome
  const status =
    latestOutcome === "FAILED"
      ? "failed"
      : (runRecords[0]?.status ?? routine?.last_invocation_status)?.toLowerCase()
  // A live approval gate wins over the persisted last_invocation_status: the
  // run reads as "running" in the DB while parked, but the human is the
  // bottleneck. NEEDS_HUMAN is the outcome of a run that finished, not a
  // waitpoint: nothing to approve, so it reads as needing attention.
  const runStatus: { tone: StatusTone; label: string } | null =
    pendingApproval || status === "waiting"
      ? { tone: "warn", label: "Waiting for a person" }
      : latestOutcome === "NEEDS_HUMAN"
        ? { tone: "warn", label: "Last run · needs your attention" }
        : status === "completed" || status === "succeeded" || status === "success"
          ? { tone: "success", label: "Last run · completed" }
          : status === "failed" || status === "error"
            ? { tone: "danger", label: "Last run · could not finish" }
            : status === "running"
              ? { tone: "blue", label: "Running" }
              : status
                ? { tone: "muted", label: `Last run · ${status}` }
                : null

  const runButton = routine && (
    // Wrapped in a span so the run-guard tooltip still shows on a disabled
    // button — disabled buttons swallow hover events.
    <span
      title={runGuard ?? (routineInputSpecs(routine.definition).length > 0 ? "Fill in this run's inputs, then run" : "Run this routine")}
      className="inline-flex"
    >
      <SubBarPrimary icon={Play} onClick={startRun} disabled={!!busyAction || !!runGuard}>
        {busyAction === "run" ? "Running…" : "Run"}
      </SubBarPrimary>
    </span>
  )
  const menu = routine && (
    <RoutineActionsMenu
      routine={routine}
      workspaceId={workspaceId}
      onChanged={() => {
        fetchRoutine()
        onChanged()
      }}
      onDeleted={onClose}
      onOpenRoutine={onOpenRoutine}
      lifecycle={lifecycle}
      onGovernance={requestGovernance}
      governanceBusy={!!busyGov || !!busyAction}
      requestDelete={deleteRequest}
    />
  )
  const pageActions = (ctx: RoutinePageActionContext) => {
    if (!routine) return null
    if (draftOnly) {
      return (
        <>
          {canEdit && routine.draft && (
            <>
              <SubBarSecondary icon={Eye} onClick={ctx.testDraft} disabled={ctx.testingDraft} title="Test the draft without side effects">
                {ctx.testingDraft ? "Testing…" : "Test draft"}
              </SubBarSecondary>
              <SubBarPrimary icon={Upload} onClick={ctx.openPublish}>
                Publish
              </SubBarPrimary>
            </>
          )}
          {menu}
        </>
      )
    }
    if (showApproval) {
      return (
        <>
          <SubBarSecondary icon={Ban} onClick={() => requestGovernance("reject")} disabled={!!busyGov || !!busyAction}>
            Reject
          </SubBarSecondary>
          <SubBarPrimary icon={busyGov === "approve" ? undefined : Check} onClick={() => requestGovernance("approve")} disabled={!!busyGov || !!busyAction}>
            {busyGov === "approve" ? "Approving…" : "Approve routine"}
          </SubBarPrimary>
          {menu}
        </>
      )
    }
    if (breaker) {
      return (
        <>
          {canEdit && published && (
            <SubBarSecondary icon={Eye} onClick={startTest} disabled={!!busyAction} title="Run without side effects">
              {busyAction === "test" ? "Testing…" : "Test run"}
            </SubBarSecondary>
          )}
          {permissions.manage ? (
            <SubBarPrimary icon={Power} onClick={() => void turnBackOn()} disabled={!!busyGov}>
              Turn back on
            </SubBarPrimary>
          ) : (
            runButton
          )}
          {menu}
        </>
      )
    }
    return (
      <>
        {canEdit && published && (
          <SubBarSecondary icon={Eye} onClick={startTest} disabled={!!busyAction || lifecycle !== "active"} title="Run without side effects">
            {busyAction === "test" ? "Testing…" : "Test"}
          </SubBarSecondary>
        )}
        {runButton}
        {menu}
      </>
    )
  }

  // Only the routine's own answer decides "gone" or "not yours"; a failed
  // draft lookup behind a 404 is reported as what it was.
  const notFound = !routine && fetchStatus === 404 && !!error?.startsWith("fetch routine")
  const noAccess = !routine && fetchStatus === 403 && !!error?.startsWith("fetch routine")

  return (
    <div className="flex h-full flex-col">
      {loading && !routine && (
        <div className="flex flex-col gap-3 p-6" role="status" aria-label="Loading routine">
          <div className="flex items-center gap-3">
            <div className="h-11 w-11 animate-pulse rounded-lg bg-muted/40" />
            <div className="flex flex-col gap-2">
              <div className="h-5 w-64 animate-pulse rounded bg-muted/40" />
              <div className="h-3 w-40 animate-pulse rounded bg-muted/30" />
            </div>
          </div>
          <div className="h-8 w-96 max-w-full animate-pulse rounded bg-muted/20" />
        </div>
      )}

      {/* A SIBLING of the routine block, not a child of it: fetchRoutine sets
          the error and then clears `routine`. */}
      {error && !routine && !loading && (
        notFound ? (
          <PageNotFound
            what="routine"
            description="It may have been deleted, or the link is wrong. Runs it already made stay readable in Activity."
            back={{ label: "Back to routines", onClick: onClose }}
          />
        ) : noAccess ? (
          <PageNoAccess area="this routine" primary={{ label: "Back to routines", onClick: onClose }} />
        ) : (
          <PageError title="This routine could not load" description="Nothing was changed." onRetry={() => void fetchRoutine()} detail={error} />
        )
      )}

      {routine && (
        <div className="flex-1 overflow-hidden">
          <RoutinePage
            key={routine.slug}
            routine={routine}
            workspaceId={workspaceId}
            crewName={crewName}
            routines={routines}
            records={runRecords}
            lifecycle={lifecycle}
            canEdit={canEdit}
            canManage={permissions.manage}
            canRun={mayRun && !runGuard}
            actions={pageActions}
            actionsInBar={actionsInBar}
            onChanged={() => {
              fetchRoutine()
              onChanged()
            }}
            onRun={startRun}
            onTest={published ? startTest : undefined}
            onOpenRun={(runId) => onRunStarted?.(runId)}
            onGovernance={requestGovernance}
            onDelete={() => setDeleteRequest((n) => n + 1)}
            pills={
              <>
                {lifecycle === "proposed" && <StatusPill size="md" tone="purple" label="Proposed" />}
                {lifecycle === "disabled" && <StatusPill size="md" tone="muted" label="Turned off" />}
                {breaker && <StatusPill size="md" tone="danger" label={`Turned off after ${breaker.consecutive_failures} failures in a row`} />}
                {runStatus && <StatusPill size="md" tone={runStatus.tone} label={runStatus.label} live={runStatus.tone === "blue"} />}
                <AgentlessBadge agentless={isAgentless(routine.definition)} />
                {routine.ephemeral && <StatusPill size="md" tone="warn" label="One-off" />}
              </>
            }
            top={
              <>
                {lifecycle === "proposed" && (
                  <section className="flex flex-col gap-1.5 rounded-lg border border-purple/25 bg-purple/[0.06] px-4 py-3" data-testid="routine-proposal">
                    <p className="text-sm font-medium">Proposed · waiting for approval</p>
                    {routine.risk_reasons && routine.risk_reasons.length > 0 ? (
                      <p className="text-xs text-muted-foreground">Flagged because it {routine.risk_reasons.join(", ")}. A Manager or above approves; until then it cannot run.</p>
                    ) : (
                      <p className="text-xs text-muted-foreground">It was proposed for review and cannot run until a Manager approves it.</p>
                    )}
                    {/* The reasons are the category; this is the thing itself. */}
                    <RoutineProposalAsk definition={routine.definition as RoutineAskDefinition | undefined} />
                    <Button
                      variant="ghost"
                      size="sm"
                      className="w-fit gap-1.5"
                      onClick={() => router.push(routine.inbox_item_id ? `/inbox?item=${encodeURIComponent(routine.inbox_item_id)}` : "/inbox")}
                      title="Open the review item in Inbox"
                    >
                      <Inbox className="h-3.5 w-3.5" />
                      Open in Inbox
                    </Button>
                  </section>
                )}
                {breaker && (
                  <section className="flex flex-col gap-1 rounded-lg border border-destructive/30 bg-destructive/[0.06] px-4 py-3" data-testid="routine-breaker">
                    <p className="text-sm font-medium">Turned off after {breaker.consecutive_failures} failures in a row</p>
                    <p className="text-xs text-muted-foreground">
                      The schedule “{breaker.name}” is paused; webhooks and manual runs still work.
                      {permissions.manage ? ` ${BREAKER_OFF_HINT}` : " An Owner or Admin can turn it back on."}
                    </p>
                  </section>
                )}
                {lastRunId && (
                  <div className="overflow-hidden rounded-lg border border-hairline">
                    {pendingApproval && (
                      <div className="px-4 pt-3">
                        <RoutineApprovalBanner waitpoint={pendingApproval} deciding={decidingApproval} onDecide={decideApproval} />
                      </div>
                    )}
                    <PipelineRunActivity
                      workspaceId={workspaceId}
                      slug={routine.slug}
                      runId={lastRunId}
                      awaiting={pendingApproval ? { stepId: pendingApproval.step_id, ts: pendingApproval.created_at } : null}
                    />
                  </div>
                )}
                <RoutineLiveRunBanner
                  runs={activeRuns}
                  definition={routine.definition}
                  canStop={canKillRoutine(role)}
                  stopping={cancelling}
                  onOpen={(runId) => onRunStarted?.(runId)}
                  onStop={stopRun}
                />
              </>
            }
          />
        </div>
      )}
      <RoutineRunInputsDialog
        definition={routine?.definition}
        headVersion={routine?.head_version}
        draft={routine?.draft}
        inputs={pendingRun?.slug === slug && pendingRun.workspaceId === workspaceId ? pendingRun.inputs : null}
        routineName={routine?.name || slug}
        // R14 (v67 D-RTRUN): Run now · Test run (Manager and above, POST
        // …/dry_run on the live version) · Later (a one-time start, POST
        // …/run with fire_at and, when pinned, pinned_version).
        modes={["run", ...(canEdit && published && lifecycle === "active" ? (["test"] as const) : []), "later"]}
        initialMode={pendingRun?.mode ?? "run"}
        submitting={busyAction === "run" || busyAction === "test" || busyAction === "later"}
        onCancel={() => setPendingRun(null)}
        onRun={(inputs, options) => {
          // Refuse a submit whose form was built for a different routine.
          // Unreachable while the effect above closes the dialog on every
          // selection change — which is the point of having it.
          if (pendingRun?.slug !== slug || pendingRun.workspaceId !== workspaceId) {
            setPendingRun(null)
            return
          }
          const mode = options?.mode ?? pendingRun.mode ?? "run"
          setPendingRun(null)
          if (mode === "test") void runTest(inputs)
          else if (mode === "later" && options?.fireAt) void planStart(inputs, options.fireAt, options.pinnedVersion ?? null)
          else void triggerAction("run", inputs)
        }}
      />
      {testResult && (
        <CreateSurface open onOpenChange={(open) => !open && setTestResult(null)} size="md" ariaLabel="Test result">
          <CreateSurfaceHeader concept="routines" context={`Routines › ${routine?.name || slug}`} title="Test without side effects" onClose={() => setTestResult(null)} />
          <CreateSurfaceBody className="p-0">
            <RoutineDryRunReport result={testResult} onClose={() => setTestResult(null)} />
          </CreateSurfaceBody>
          <CreateSurfaceFooter hint={null} onCancel={() => setTestResult(null)} cancelLabel="Close" guardCancel={false} />
        </CreateSurface>
      )}
      <ConfirmDialog
        open={confirmGov !== null}
        onOpenChange={(open) => !open && setConfirmGov(null)}
        title={
          confirmGov === "reject"
            ? `Reject ${routine?.name || slug}?`
            : confirmGov === "disable"
              ? `Turn off ${routine?.name || slug}?`
              : `Turn on ${routine?.name || slug}?`
        }
        description={
          confirmGov === "reject"
            ? "The proposed routine is discarded."
            : confirmGov === "disable"
              ? "It cannot run until it is turned on again."
              : "It can run again: schedules, webhooks and the Run button start it."
        }
        consequences={
          confirmGov === "reject"
            ? [{ tone: "lost", text: "The proposal and what it would have done." }]
            : confirmGov === "disable"
              ? [
                  { tone: "lost", text: "Schedules, webhooks and the Run button stop starting it." },
                  { tone: "kept", text: "Runs in progress finish; its history stays." },
                ]
              : []
        }
        confirmLabel={confirmGov === "reject" ? "Reject routine" : confirmGov === "disable" ? "Turn off" : "Turn on"}
        destructive={confirmGov !== "enable"}
        onConfirm={async () => {
          if (confirmGov && !(await governanceAction(confirmGov))) throw new Error("refused")
        }}
      />
    </div>
  )
}

function actionLabel(_a: "run"): string {
  return "Run"
}

function governanceLabel(a: GovernanceAction): string {
  switch (a) {
    case "approve":
      return "Routine approved"
    case "reject":
      return "Routine rejected"
    case "disable":
      return "Routine turned off"
    case "enable":
      return "Routine turned on"
  }
}

function governanceFailedLabel(a: GovernanceAction): string {
  switch (a) {
    case "approve":
      return "Approve failed"
    case "reject":
      return "Reject failed"
    case "disable":
      return "Turn off failed"
    case "enable":
      return "Turn on failed"
  }
}

/**
 * A RoutineDetail synthesised from a draft that has no published routine
 * behind it, or null when there is no such draft either. Everything the page
 * reads comes from the draft document; version, runs and schedules do not
 * exist yet.
 */
async function routineFromDraftOnly(
  workspaceId: string,
  slug: string,
  signal: AbortSignal,
): Promise<RoutineDetail | null> {
  let draft
  try {
    draft = await loadRoutineDraft(workspaceId, slug, signal)
  } catch (error) {
    if (error instanceof RoutineDraftError && error.status === 404) return null
    throw error
  }
  if (!draft?.id) return null
  const doc = draft.document ?? {}
  const definition =
    typeof doc.definition === "object" && doc.definition !== null
      ? (doc.definition as Record<string, unknown>)
      : {}
  const text = (value: unknown) => (typeof value === "string" ? value : "")
  return {
    id: "",
    slug,
    name: text(doc.name) || text(definition.display_name) || slug,
    description: text(doc.description) || text(definition.description) || undefined,
    dsl_version: text(definition.dsl_version) || "1.0",
    definition,
    definition_hash: "",
    ephemeral: false,
    workspace_visible: true,
    invocation_count: 0,
    authored_via: "draft",
    created_at: draft.updated_at ?? "",
    updated_at: draft.updated_at ?? "",
    author_crew_id: text(doc.author_crew_id) || undefined,
    icon: text(doc.icon) || undefined,
    color: text(doc.color) || undefined,
    draft: { id: draft.id, revision: draft.revision, updated_at: draft.updated_at ?? "", updated_by: draft.updated_by, test_result: draft.test_result },
    draft_only: true,
  }
}
