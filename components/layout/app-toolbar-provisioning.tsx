"use client"

import Link from "next/link"
import { useCallback, useState } from "react"
import { toast } from "sonner"
import {
  AlertTriangle, Check, Circle, Loader2, Package, Play, RotateCcw,
} from "lucide-react"
import { Spinner } from "@/components/ui/spinner"
import { Popover, PopoverContent, PopoverTrigger } from "@/components/ui/popover"
import { useProvisioningStatus } from "@/hooks/use-provisioning-status"
import { apiFetch } from "@/lib/api-fetch"

// Provisioning UI extracted from app-toolbar.tsx — the badge popover
// driving Build now / Retry / Restart agents from the global toolbar,
// plus its row, checklist, status dot, and feature-chip helpers.

function ProvisioningBadge({
  provisioning,
  workspaceId,
}: {
  provisioning: ReturnType<typeof useProvisioningStatus>
  workspaceId: string | null
}) {
  const [open, setOpen] = useState(false)
  if (provisioning.total === 0) return null

  const tone = provisioning.failed > 0 ? "red" : "amber"
  const colors = tone === "red"
    ? { bg: "bg-red-50 dark:bg-red-950/30 border-red-200 dark:border-red-800", text: "text-red-700 dark:text-red-400", icon: "text-red-600" }
    : { bg: "bg-amber-50 dark:bg-amber-950/30 border-amber-200 dark:border-amber-800", text: "text-amber-700 dark:text-amber-400", icon: "text-amber-600" }

  const verbalize = () => {
    if (provisioning.failed > 0) return `${provisioning.failed} build${provisioning.failed > 1 ? "s" : ""} failed`
    if (provisioning.building > 0) return `Building ${provisioning.building}…`
    if (provisioning.needsProvision > 0) return `${provisioning.needsProvision} need${provisioning.needsProvision > 1 ? "" : "s"} rebuild`
    if (provisioning.pendingRestart > 0) return `${provisioning.pendingRestart} need${provisioning.pendingRestart > 1 ? "" : "s"} restart`
    return ""
  }
  const Icon = provisioning.building > 0 ? Loader2 : provisioning.failed > 0 ? AlertTriangle : Package
  // Show every crew that needs the user's attention: build pending, building,
  // failed, or build complete but agents still on the old image (waiting for
  // explicit Restart). Idle / clean-completed crews are filtered out so the
  // popover always reflects the badge count.
  const unhealthy = provisioning.detail.filter((d) => {
    if (d.status === "needs_provision" || d.status === "running" || d.status === "failed") return true
    if (d.status === "completed" && (d.agentsPendingRestart ?? 0) > 0) return true
    return false
  })

  return (
    <Popover open={open} onOpenChange={setOpen}>
      <PopoverTrigger asChild>
        <button
          type="button"
          aria-label={`Crew images: ${verbalize()}`}
          className={`flex items-center gap-1.5 px-2.5 py-1 rounded-full border transition-all hover:brightness-95 ${colors.bg}`}
        >
          <Icon className={`h-3 w-3 ${colors.icon} ${provisioning.building > 0 ? "animate-spin" : ""}`} />
          <span className={`text-micro font-medium ${colors.text}`}>{verbalize()}</span>
        </button>
      </PopoverTrigger>
      <PopoverContent align="start" sideOffset={8} className="w-[420px] p-0 overflow-hidden">
        <div className="px-3 py-2 border-b text-xs font-semibold flex items-center gap-2">
          <Package className="h-3.5 w-3.5 text-muted-foreground" />
          Container builds
        </div>
        <ul className="divide-y max-h-[480px] overflow-y-auto">
          {unhealthy.map((d) => (
            <ProvisioningRow
              key={d.id}
              crew={d}
              workspaceId={workspaceId}
              onNavigate={() => setOpen(false)}
            />
          ))}
        </ul>
        <div className="px-3 py-2 border-t bg-muted/30 text-[10px] text-muted-foreground">
          Build button kicks off provisioning here — no need to open the crew.
        </div>
      </PopoverContent>
    </Popover>
  )
}

/**
 * One row in the provisioning popover. Renders state-specific content
 * (Build / progress / Retry) inline so the user can act without navigating.
 *
 * The crew name remains a Link to the canvas because the canvas still
 * shows the full ProvisioningBanner with raw error logs and the
 * Settings tab — the popover is the *primary* surface for the action,
 * not the only place to see context.
 */

function ProvisioningRow({
  crew,
  workspaceId,
  onNavigate,
}: {
  crew: ReturnType<typeof useProvisioningStatus>["detail"][number]
  workspaceId: string | null
  onNavigate: () => void
}) {
  const [busy, setBusy] = useState(false)
  const pendingRestart = crew.agentsPendingRestart ?? 0
  const isPendingRestart = crew.status === "completed" && pendingRestart > 0

  const trigger = useCallback(async () => {
    if (!workspaceId) return
    setBusy(true)
    try {
      const r = await apiFetch(
        `/api/v1/crews/${crew.id}/provision?workspace_id=${encodeURIComponent(workspaceId)}`,
        { method: "POST" },
      )
      if (!r.ok) {
        const text = await r.text()
        toast.error(`Build failed to start: ${text.slice(0, 200)}`)
      } else {
        toast.success(`Building ${crew.name}…`)
      }
    } catch (err) {
      toast.error(`Build failed: ${err instanceof Error ? err.message : String(err)}`)
    } finally {
      setBusy(false)
    }
  }, [crew.id, crew.name, workspaceId])

  const restart = useCallback(async () => {
    if (!workspaceId) return
    setBusy(true)
    try {
      const r = await apiFetch(
        `/api/v1/crews/${crew.id}/restart-agents?workspace_id=${encodeURIComponent(workspaceId)}`,
        { method: "POST" },
      )
      if (!r.ok) {
        const text = await r.text()
        toast.error(`Restart failed: ${text.slice(0, 200)}`)
      } else {
        const data = (await r.json().catch(() => ({}))) as { restarted?: number }
        toast.success(`${data.restarted ?? 0} agent${data.restarted === 1 ? "" : "s"} restarted in ${crew.name}`)
      }
    } catch (err) {
      toast.error(`Restart failed: ${err instanceof Error ? err.message : String(err)}`)
    } finally {
      setBusy(false)
    }
  }, [crew.id, crew.name, workspaceId])

  const statusLabel = isPendingRestart
    ? "ready · restart agents"
    : crew.status === "needs_provision"
      ? "needs rebuild"
      : crew.status

  return (
    <li className="px-3 py-2.5">
      <div className="flex items-center gap-2 mb-1.5">
        <ProvisioningStatusDot status={crew.status} />
        <Link
          href={`/crews?crew=${encodeURIComponent(crew.slug)}`}
          onClick={onNavigate}
          className="text-sm font-medium truncate flex-1 hover:text-foreground transition-colors"
        >
          {crew.name}
        </Link>
        <span className="text-[10px] uppercase tracking-wide text-muted-foreground shrink-0">
          {statusLabel}
        </span>
      </div>

      {crew.status === "running" && crew.steps && crew.steps.length > 0 ? (
        <ProvisioningChecklist steps={crew.steps} active={crew.step ?? 0} message={crew.message} />
      ) : crew.status === "running" && crew.total ? (
        // Fallback for the brief window between provision.started and the
        // first progress event, or for older backends that don't emit a
        // plan: a single-line spinner with whatever message we have.
        <div className="ml-4 text-[11px] text-muted-foreground flex items-center gap-2">
          <Spinner className="h-3 w-3 shrink-0" />
          <span className="truncate">{crew.message ?? "Building image…"}</span>
          <span className="tabular-nums shrink-0 text-muted-foreground">
            {crew.step ?? 0}/{crew.total}
          </span>
        </div>
      ) : null}

      {crew.status === "failed" && crew.error && (
        <pre className="text-[10px] text-red-500 dark:text-red-400 font-mono whitespace-pre-wrap break-words ml-4 max-h-[60px] overflow-hidden">
          {crew.error.slice(0, 240)}
        </pre>
      )}

      {crew.featureIds.length > 0 && crew.status !== "running" && (
        <div className="flex items-center gap-1 ml-4 mt-1.5 flex-wrap">
          {crew.featureIds.map((fid) => (
            <FeatureChip key={fid} featureRef={fid} />
          ))}
        </div>
      )}

      {isPendingRestart && (
        <div className="ml-4 mt-1 text-[11px] text-muted-foreground">
          {pendingRestart} agent{pendingRestart === 1 ? "" : "s"} on old image
        </div>
      )}

      {(crew.status === "needs_provision" || crew.status === "failed" || isPendingRestart) && (
        <div className="flex justify-end mt-2">
          <button
            type="button"
            onClick={isPendingRestart ? restart : trigger}
            disabled={busy || !workspaceId}
            className={`text-xs px-2.5 py-1 rounded border flex items-center gap-1.5 transition-colors ${
              crew.status === "failed"
                ? "bg-red-500/15 hover:bg-red-500/25 text-red-300 border-red-500/40"
                : isPendingRestart
                  ? "bg-emerald-500/15 hover:bg-emerald-500/25 text-emerald-300 border-emerald-500/40"
                  : "bg-amber-500/20 hover:bg-amber-500/30 text-amber-200 border-amber-500/40"
            } disabled:opacity-50 disabled:cursor-not-allowed`}
          >
            {busy ? (
              <Spinner className="h-3 w-3" />
            ) : crew.status === "failed" ? (
              <RotateCcw className="h-3 w-3" />
            ) : isPendingRestart ? (
              <RotateCcw className="h-3 w-3" />
            ) : (
              <Play className="h-3 w-3" />
            )}
            {busy
              ? "Starting…"
              : crew.status === "failed"
                ? "Retry"
                : isPendingRestart
                  ? "Restart agents"
                  : "Build now"}
          </button>
        </div>
      )}
    </li>
  )
}

/**
 * Per-build checklist rendered inside a popover row. Three visual states
 * per step (matching how a user thinks about the build):
 *   - done    → faint, with checkmark
 *   - active  → bold, with spinner
 *   - pending → muted, with empty circle
 *
 * `active` is 1-based (the next step to run = current). When active equals
 * steps.length the build is on its last step. After completion the row
 * mounts as `completed` (no checklist), so we don't have to handle "all
 * done" inside the running view.
 */

function ProvisioningChecklist({
  steps,
  active,
  message,
}: {
  steps: string[]
  active: number
  message?: string
}) {
  // The emit message exactly matches a plan entry — we use that to find
  // the active row even if `active` (the index) lags a tick. Falls back
  // to `active - 1` when message is missing.
  let activeIdx = active > 0 ? active - 1 : -1
  if (message) {
    const messageIdx = steps.indexOf(message)
    if (messageIdx >= 0) activeIdx = messageIdx
  }

  // Render order: active first, completed steps stacking below it
  // (most recently completed nearest the active row), then pending
  // steps. Earlier the order was plan-sequence which pushed the
  // currently-installing line below a long list of green checkmarks
  // — by the time PHP was building, the user had to scroll past nine
  // completed rows to see what was actually happening.
  type Row = { label: string; planIdx: number; state: "active" | "done" | "pending" }
  const rows: Row[] = steps.map((label, i) => ({
    label,
    planIdx: i,
    state: i < activeIdx ? "done" : i === activeIdx ? "active" : "pending",
  }))
  const ordered: Row[] = [
    ...rows.filter((r) => r.state === "active"),
    // Most recently completed first so the eye lands on the freshest
    // result without scanning the whole green stack.
    ...rows.filter((r) => r.state === "done").slice().reverse(),
    ...rows.filter((r) => r.state === "pending"),
  ]

  return (
    <ol className="ml-1 mt-1 space-y-1 max-h-[180px] overflow-y-auto">
      {ordered.map((row) => (
        <li
          key={row.planIdx}
          className={`flex items-center gap-2 text-[11px] ${
            row.state === "done"
              ? "text-muted-foreground"
              : row.state === "active"
                ? "text-foreground font-medium"
                : "text-muted-foreground-soft"
          }`}
        >
          <span className="w-3 h-3 shrink-0 flex items-center justify-center">
            {row.state === "done" ? (
              <Check className="h-3 w-3 text-emerald-400" />
            ) : row.state === "active" ? (
              <Spinner className="h-3 w-3 text-blue-400" />
            ) : (
              <Circle className="h-2 w-2 text-muted-foreground-soft" />
            )}
          </span>
          <span className="truncate">{row.label}</span>
        </li>
      ))}
    </ol>
  )
}


function ProvisioningStatusDot({ status }: { status: string }) {
  if (status === "running") return <Spinner className="h-3 w-3 text-blue-500 shrink-0" />
  if (status === "failed") return <AlertTriangle className="h-3 w-3 text-red-500 shrink-0" />
  // needs_provision
  return <span className="h-2 w-2 rounded-full bg-amber-500 shrink-0" />
}

/**
 * Pill that renders a feature reference like
 * "ghcr.io/devcontainers/features/python:1" as just "python" with a
 * brand icon when we recognise the slug. Falls back to the bare slug
 * when we don't have a brand icon for it.
 */

function FeatureChip({ featureRef }: { featureRef: string }) {
  // Extract the leaf name: ghcr.io/.../features/<name>:<v> → <name>
  const m = featureRef.match(/\/features\/([^:]+)/)
  const slug = (m?.[1] ?? featureRef).toLowerCase()
  return (
    <span className="text-[10px] px-1.5 py-0.5 rounded bg-muted/60 text-muted-foreground border border-border/50">
      {slug}
    </span>
  )
}

export {
  ProvisioningBadge,
  ProvisioningRow,
  ProvisioningChecklist,
  ProvisioningStatusDot,
  FeatureChip,
}
