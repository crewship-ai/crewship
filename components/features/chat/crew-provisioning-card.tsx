"use client"

import { Loader2, Package, AlertTriangle, Check } from "lucide-react"
import { Spinner } from "@/components/ui/spinner"
import { useWorkspace } from "@/hooks/use-workspace"
import { useProvisioningStatus } from "@/hooks/use-provisioning-status"
import { ProvisioningChecklist } from "@/components/layout/app-toolbar-provisioning"

interface CrewProvisioningCardProps {
  crewId?: string
  crewSlug?: string
  /** Fallback message text shown while the live progress feed is still
   *  warming up (the chat event lands before the first provision.* WS
   *  event for the same crew, especially on a cold start). */
  message?: string
  /** Status reported by the chat event itself. When the bridge fails to
   *  enqueue (rate limit, unavailable provisioner) it sets `"failed"` so
   *  this card can render a real error state instead of a spinner that
   *  never resolves — no provision.* event will ever fire for a job that
   *  never started. */
  enqueueStatus?: string
  /** Error string from the bridge when `enqueueStatus === "failed"`. */
  enqueueError?: string
}

/** Inline build-progress card rendered inside the chat when a user's first
 *  message lands on a crew whose devcontainer image hasn't been built yet.
 *  Subscribes to the same workspace-level provisioning stream the toolbar
 *  popover uses, so updates appear in lockstep across both surfaces.
 */
export function CrewProvisioningCard({
  crewId,
  crewSlug,
  message,
  enqueueStatus,
  enqueueError,
}: CrewProvisioningCardProps) {
  const { workspaceId } = useWorkspace()
  const provisioning = useProvisioningStatus(workspaceId)

  const crew = crewId
    ? provisioning.detail.find((d) => d.id === crewId)
    : crewSlug
      ? provisioning.detail.find((d) => d.slug === crewSlug)
      : undefined

  // Pre-feed state.
  if (!crew) {
    // Enqueue failed — no job was created so the WS feed will never produce
    // updates for this crew. Render a hard failure instead of a perpetual
    // spinner. Common causes: rate-limit, Docker provisioner not wired up,
    // crew has no devcontainer config.
    if (enqueueStatus === "failed") {
      return (
        <div className="rounded-lg border border-red-500/30 bg-red-500/5 px-4 py-3 flex items-start gap-3">
          <AlertTriangle className="h-4 w-4 text-red-500 shrink-0 mt-0.5" />
          <div className="flex-1 min-w-0">
            <div className="text-sm font-medium text-foreground mb-0.5">
              {crewSlug ? `Could not start build for ${crewSlug}` : "Could not start build"}
            </div>
            <div className="text-xs text-muted-foreground">
              {message || "Provisioning was not enqueued."}
            </div>
            {enqueueError ? (
              <pre className="text-[11px] text-red-500/90 dark:text-red-400/90 font-mono whitespace-pre-wrap break-words mt-1 max-h-[80px] overflow-hidden">
                {enqueueError.slice(0, 320)}
              </pre>
            ) : null}
          </div>
        </div>
      )
    }

    // Otherwise: warm-up state — a build was kicked off but the WS hasn't
    // replayed the plan yet. Show a placeholder spinner.
    return (
      <div className="rounded-lg border border-amber-500/30 bg-amber-500/5 px-4 py-3 flex items-start gap-3">
        <Spinner className="h-4 w-4 text-amber-500 shrink-0 mt-0.5" />
        <div className="flex-1 min-w-0">
          <div className="text-sm font-medium text-foreground mb-0.5">
            {crewSlug ? `Building ${crewSlug}…` : "Building crew image…"}
          </div>
          <div className="text-xs text-muted-foreground">
            {message || "Provisioning kicked off — your message will run once the image is ready."}
          </div>
        </div>
      </div>
    )
  }

  const Icon = crew.status === "failed"
    ? AlertTriangle
    : crew.status === "completed"
      ? Check
      : crew.status === "running"
        ? Loader2
        : Package

  const tone = crew.status === "failed"
    ? "border-red-500/30 bg-red-500/5"
    : crew.status === "completed"
      ? "border-emerald-500/30 bg-emerald-500/5"
      : "border-amber-500/30 bg-amber-500/5"

  const iconTone = crew.status === "failed"
    ? "text-red-500"
    : crew.status === "completed"
      ? "text-emerald-500"
      : "text-amber-500"

  const label = crew.status === "failed"
    ? `Build failed for ${crew.name}`
    : crew.status === "completed"
      ? `${crew.name} ready — re-send your message`
      : `Building ${crew.name}…`

  return (
    <div className={`rounded-lg border ${tone} px-4 py-3 flex items-start gap-3`}>
      <Icon className={`h-4 w-4 shrink-0 mt-0.5 ${iconTone} ${crew.status === "running" ? "animate-spin" : ""}`} />
      <div className="flex-1 min-w-0">
        <div className="text-sm font-medium text-foreground mb-1">{label}</div>

        {crew.status === "running" && crew.steps && crew.steps.length > 0 ? (
          <ProvisioningChecklist
            steps={crew.steps}
            active={crew.step ?? 0}
            message={crew.message}
          />
        ) : crew.status === "running" ? (
          <div className="text-xs text-muted-foreground flex items-center gap-2">
            <span>{crew.message ?? "Pulling base image…"}</span>
            {crew.total ? (
              <span className="tabular-nums shrink-0 text-muted-foreground">
                {crew.step ?? 0}/{crew.total}
              </span>
            ) : null}
          </div>
        ) : null}

        {crew.status === "failed" && crew.error && (
          <pre className="text-[11px] text-red-500/90 dark:text-red-400/90 font-mono whitespace-pre-wrap break-words mt-1 max-h-[80px] overflow-hidden">
            {crew.error.slice(0, 320)}
          </pre>
        )}
      </div>
    </div>
  )
}
