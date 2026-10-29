"use client"
import { useEffect, useState } from "react"
import { AlertTriangle, CheckCircle2, Sparkles } from "lucide-react"
import { Button } from "@/components/ui/button"
import { Spinner } from "@/components/ui/spinner"
import { useArtifactViewer } from "./artifact-viewer-context"
import { useArtifactProjectMutations, useArtifactProjectSnapshot } from "./use-artifact-project-data"
import type { ArtifactToolActivity } from "./tool-result"

const CARD = "grid max-w-[460px] gap-2 rounded-xl border bg-card px-3 py-2.5"

function runningText(activity: ArtifactToolActivity): string {
  if (activity.tool === "create") return "Creating project…"
  if (activity.tool === "restore") return activity.restoreTarget !== undefined ? `Restoring v${activity.restoreTarget} as a new version…` : "Restoring as a new version…"
  return activity.baseVersion !== undefined ? `Creating version ${activity.baseVersion + 1}…` : "Creating a new version…"
}

function useElapsedSeconds(startedAt: Date | undefined): number | null {
  const [now, setNow] = useState(() => Date.now())
  useEffect(() => {
    if (!startedAt) return
    const id = setInterval(() => setNow(Date.now()), 1000)
    return () => clearInterval(id)
  }, [startedAt])
  if (!startedAt || Number.isNaN(startedAt.getTime())) return null
  return Math.max(0, Math.floor((now - startedAt.getTime()) / 1000))
}

function Running({ activity, startedAt }: { activity: ArtifactToolActivity; startedAt?: Date }) {
  const label = runningText(activity)
  const elapsed = useElapsedSeconds(startedAt)
  return <div className={CARD}>
    <div className="flex min-w-0 items-center gap-2 text-sm">
      <Sparkles className="size-3.5 shrink-0 text-primary" aria-hidden="true" />
      <p role="status" aria-live="polite" className="min-w-0 truncate font-semibold">{label}</p>
      {/* Steps are not streamed by the tool, so only the elapsed time is shown. */}
      {elapsed !== null && <span className="ml-auto shrink-0 text-xs text-muted-foreground">{elapsed}s</span>}
    </div>
    <div role="progressbar" aria-label={label.replace(/…$/, "")} className="relative h-1 overflow-hidden rounded-full bg-muted">
      <span className="absolute inset-y-0 left-0 w-1/3 rounded-full bg-primary motion-safe:animate-pulse" />
    </div>
  </div>
}

/** Restore is offered only while this step's version is still the latest one, so it never undoes a newer version. */
function DoneActions({ workspaceId, slug, version, source }: { workspaceId: string; slug: string; version: number; source?: string }) {
  const viewer = useArtifactViewer()
  const snapshot = useArtifactProjectSnapshot(workspaceId, slug)
  const { restore } = useArtifactProjectMutations(workspaceId, slug)
  const data = snapshot.data
  const message = data?.versions.find(v => v.version === version)?.message
    ?? (data?.manifest.version === version ? data.manifest.message : undefined)
  const conflict = restore.error?.status === 409 || restore.error?.code === "artifact_version_conflict"
  const canRestore = version > 1 && !snapshot.error && data?.can_write === true && data.can_restore === true && data.latest_version === version
  return <>
    {message && <span className="min-w-0 truncate text-muted-foreground">{message}</span>}
    <span className="flex-1" />
    {restore.isSuccess && restore.variables
      ? <span role="status" className="text-muted-foreground">Restored v{restore.variables.version} as a new version · nothing deleted</span>
      : conflict
        ? <span className="flex items-center gap-1.5 text-warn">
          Changed meanwhile ·
          <Button type="button" size="xs" variant="link" className="h-auto px-0"
            onClick={() => { restore.reset(); void snapshot.refetch() }}>Reload</Button>
        </span>
        : canRestore && <Button type="button" size="xs" variant="ghost" disabled={restore.isPending}
          onClick={() => restore.mutate({ base_version: data.latest_version, version: version - 1 })}>
          {restore.isPending && <Spinner />}Restore v{version - 1}
        </Button>}
    {restore.error && !conflict && <span className="text-destructive">{restore.error.message}</span>}
    {viewer && <Button type="button" size="xs" variant="secondary" onClick={() => viewer.open(source === undefined ? { slug } : { slug, source })}>Open</Button>}
  </>
}

/** The agent's artifact step: a progress card while it runs, then one row with what came out of it. */
export function ArtifactStepCard({ activity, startedAt, source }: {
  activity: ArtifactToolActivity
  startedAt?: Date
  /** Passed on to the viewer so the project card of the same step shows as open. */
  source?: string
}) {
  const viewer = useArtifactViewer()
  if (activity.state === "running") return <Running activity={activity} startedAt={startedAt} />
  if (activity.state === "failed") {
    // No retry here: the agent owns the retry and will publish again.
    return <div className={CARD}><p className="flex items-center gap-2 text-sm text-destructive"><AlertTriangle className="size-4 shrink-0" aria-hidden="true" />
      <span className="min-w-0 break-words">Couldn’t save version{activity.error ? ` · ${activity.error}` : ""}</span></p></div>
  }
  const { version, slug } = activity
  return <div className={CARD}>
    <div className="flex min-w-0 flex-wrap items-center gap-2 text-sm">
      <CheckCircle2 className="size-[15px] shrink-0 text-success" aria-hidden="true" />
      <b className="font-semibold">{version !== undefined ? `v${version} ready` : "Ready"}</b>
      {viewer && slug && version !== undefined && <DoneActions workspaceId={viewer.workspaceId} slug={slug} version={version} source={source} />}
    </div>
  </div>
}
