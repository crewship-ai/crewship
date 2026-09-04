"use client"

import { useCallback, useState } from "react"
import { AlertTriangle, Check, ChevronDown, X } from "lucide-react"
import { Spinner } from "@/components/ui/spinner"
import { toast } from "sonner"
import { apiFetch } from "@/lib/api-fetch"
import { useProvisioningStatus, type ProvisioningStatus } from "@/hooks/use-provisioning-status"
import {
  ProvisioningEventSteps,
  ProvisioningChecklist,
  ProvisioningFailure,
  RecentBuildSummary,
  formatProvisionAgo,
} from "@/components/layout/app-toolbar-provisioning"

// ProvisioningBanner is the canvas-level surface for container-image build
// state. It shares the workspace-scoped useProvisioningStatus hook with the
// toolbar badge and the chat card so all three move in lockstep — granular
// per-feature progress while building, a lingering "ready · built 34s ago"
// summary after a fast build (so it never looks dead), and a persistent red
// failure with the BuildKit log tail. Collapsible is a tiny details/summary
// wrapper used by the Settings tab. Extracted from crew-canvas.tsx.

function ProvisioningBanner({ crewId, crewSlug, workspaceId, needsOwnedByStrip = false, provisioning: provisioningProp }: {
  crewId: string
  crewSlug: string
  workspaceId: string
  /** The crew canvas's "Needs you" strip carries the rebuild and failed
   *  rows with their verbs (crew-needs-you.tsx); this banner then keeps only
   *  the live build progress and the recent-build summary. */
  needsOwnedByStrip?: boolean
  /** A parent's poller; when given, this banner does not start its own. */
  provisioning?: ProvisioningStatus
}) {
  const ownProvisioning = useProvisioningStatus(provisioningProp ? null : workspaceId)
  const provisioning = provisioningProp ?? ownProvisioning
  const { acknowledge } = provisioning
  const crew = provisioning.detail.find((d) => d.id === crewId)
  const [triggering, setTriggering] = useState(false)

  const trigger = useCallback(async () => {
    setTriggering(true)
    try {
      const r = await apiFetch(`/api/v1/crews/${crewId}/provision?workspace_id=${encodeURIComponent(workspaceId)}`, { method: "POST" })
      if (!r.ok) {
        const text = await r.text()
        toast.error(`Provision failed to start: ${text}`)
      } else {
        toast.success(`Provisioning started for ${crewSlug}`)
      }
    } catch (err) {
      toast.error(`Provision failed: ${err instanceof Error ? err.message : err}`)
    } finally {
      setTriggering(false)
    }
  }, [crewId, crewSlug, workspaceId])

  if (!crew) return null

  const recentCompleted = crew.status !== "failed" && crew.recent?.outcome === "completed"
  const isNeedsProvision = crew.status === "needs_provision"
  if (crew.status !== "running" && crew.status !== "failed" && !isNeedsProvision && !recentCompleted) {
    return null
  }
  if (needsOwnedByStrip && (crew.status === "failed" || isNeedsProvision)) {
    return null
  }

  if (crew.status === "running") {
    return (
      <div className="rounded-xl border border-primary/30 bg-primary/5 px-4 py-3 flex items-start gap-3">
        <Spinner className="h-4 w-4 text-primary shrink-0 mt-0.5" />
        <div className="flex-1 min-w-0">
          <div className="text-sm text-primary">
            {crew.activeFeature
              ? <>Building container image · installing <span className="font-medium">{crew.activeFeature}</span></>
              : "Building container image…"}
          </div>
          {crew.eventSteps && crew.eventSteps.length > 0 ? (
            <ProvisioningEventSteps steps={crew.eventSteps} />
          ) : crew.steps && crew.steps.length > 0 ? (
            <ProvisioningChecklist steps={crew.steps} active={crew.step ?? 0} message={crew.message} />
          ) : (
            <div className="text-xs text-muted-foreground">
              Devcontainer features are installing. Agents in this crew will become runnable as soon as the image is ready (usually 30-90 s).
            </div>
          )}
        </div>
      </div>
    )
  }

  if (crew.status === "failed") {
    return (
      <div className="rounded-xl border border-destructive/40 bg-destructive/5 px-4 py-3 flex items-start gap-3">
        <AlertTriangle className="h-4 w-4 text-destructive shrink-0 mt-0.5" />
        <div className="flex-1 min-w-0">
          <div className="text-sm text-destructive">Last provision failed</div>
          <ProvisioningFailure crew={crew} />
          <div className="text-xs text-muted-foreground mt-1.5">
            Fix the runtime config (Settings → Container image &amp; features) and try again.
          </div>
        </div>
        <div className="flex flex-col items-end gap-1.5 shrink-0">
          <button
            type="button"
            onClick={trigger}
            disabled={triggering}
            className="text-xs px-2.5 py-1.5 rounded bg-destructive/20 hover:bg-destructive/30 text-destructive border border-destructive/40"
          >
            {triggering ? "Starting…" : "Retry"}
          </button>
          <button
            type="button"
            onClick={() => acknowledge(crew.id)}
            className="text-[10px] text-muted-foreground hover:text-foreground inline-flex items-center gap-1"
          >
            <X className="h-3 w-3" /> Dismiss
          </button>
        </div>
      </div>
    )
  }

  if (recentCompleted && crew.recent) {
    return (
      <div className="rounded-xl border border-success/30 bg-success/5 px-4 py-3 flex items-start gap-3">
        <Check className="h-4 w-4 text-success shrink-0 mt-0.5" />
        <div className="flex-1 min-w-0">
          <div className="text-sm text-success">
            Container image built — {formatProvisionAgo(crew.recent.at)}
          </div>
          <RecentBuildSummary recent={crew.recent} />
        </div>
        <button
          type="button"
          onClick={() => acknowledge(crew.id)}
          aria-label="Dismiss"
          title="Dismiss"
          className="shrink-0 p-0.5 rounded text-muted-foreground hover:text-foreground hover:bg-muted/60 transition-colors"
        >
          <X className="h-3.5 w-3.5" />
        </button>
      </div>
    )
  }

  // needs_provision (idle, hasConfig, no cached_image)
  return (
    <div className="rounded-xl border border-warn/40 bg-warn/5 px-4 py-3 flex items-center gap-3">
      <AlertTriangle className="h-4 w-4 text-warn shrink-0" />
      <div className="flex-1 min-w-0">
        <div className="text-sm text-warn">Container image needs rebuild</div>
        <div className="text-xs text-muted-foreground">
          Runtime config changed — agents in this crew can&apos;t start until the image is rebuilt. Use the toolbar Build button or rebuild here.
        </div>
      </div>
      <button
        type="button"
        onClick={trigger}
        disabled={triggering}
        className="text-xs px-2.5 py-1.5 rounded bg-warn/25 hover:bg-warn/35 text-warn border border-warn/40 shrink-0"
      >
        {triggering ? "Starting…" : "Build now"}
      </button>
    </div>
  )
}


function Collapsible({ title, summary, children }: {
  title: string
  summary: string
  children: React.ReactNode
}) {
  return (
    <details className="rounded-xl border border-white/8 bg-card overflow-hidden group">
      <summary className="px-4 py-3 flex items-center gap-2 text-sm cursor-pointer hover:bg-white/[0.02] list-none">
        <ChevronDown className="h-3 w-3 text-muted-foreground transition-transform group-open:rotate-0 -rotate-90" />
        <span className="text-foreground font-medium">{title}</span>
        <span className="text-xs text-muted-foreground truncate">{summary}</span>
      </summary>
      <div className="px-4 py-3 border-t border-white/5">
        {children}
      </div>
    </details>
  )
}


export { ProvisioningBanner, Collapsible }
