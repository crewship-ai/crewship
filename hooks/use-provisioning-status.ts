"use client"

import { useCallback, useEffect, useMemo, useRef, useState } from "react"
import { useRealtime } from "@/hooks/use-realtime"
import { apiFetch } from "@/lib/api-fetch"

/**
 * Aggregate provisioning state for the toolbar badge — surfaces "an
 * image is being built / waiting / failed somewhere" without forcing
 * the user to remember which crew they were editing.
 *
 * Live updates arrive via the workspace WS channel; polling is the
 * fallback when WS is disconnected (every 30 s) and the bootstrap on
 * mount. While a build is in flight we still poll every 4 s as a
 * belt-and-braces guard against missed WS frames.
 *
 * Two visibility guarantees layered on top of the raw status feed:
 *
 *  1. *Lingering recent state* — a build can finish in well under a
 *     minute, so flipping straight back to "idle" means the user never
 *     sees that anything happened. After a build completes we keep a
 *     compact `recent` summary on the crew (counted in the badge total)
 *     for {@link RECENT_COMPLETED_TTL_MS} so even a sub-minute build
 *     stays visible after the fact. Failures linger indefinitely (red,
 *     with the failing step + log tail) until the user acknowledges.
 *
 *  2. *Feature-granular progress* — the backend emits structured
 *     `provision.event` frames (one per resolve / build / per-feature
 *     install / container-create / ready / failure step). We fold those
 *     into a deduped `eventSteps` list and an `activeFeature`, preferring
 *     that richer source over the coarse `provision.started`/`progress`
 *     plan when present, so the UI can say "installing **ansible**"
 *     instead of "8/9".
 */

/** How long a *completed* build's recent summary stays pinned to the badge
 *  before it's pruned back to idle. Failures never auto-prune. */
export const RECENT_COMPLETED_TTL_MS = 3 * 60 * 1000
/** How often we sweep for expired completed-recent summaries. */
export const RECENT_PRUNE_INTERVAL_MS = 5000
/** Bound on BuildKit log-tail lines retained for the in-card "build log". */
const BUILD_LOG_TAIL_CAP = 60

export interface ProvisioningSummary {
  needsProvision: number      // user changed config, no image yet
  building: number            // job currently running
  failed: number              // last build crashed
  pendingRestart: number      // build complete but agents still on old image (count of crews, not agents)
  recentlyCompleted: number   // build finished recently, summary still lingering
  total: number               // sum of the buckets above — the badge counter
  detail: ProvisioningCrewState[]
}

export interface ProvisioningStatus extends ProvisioningSummary {
  /** Dismiss a crew's lingering recent state (completed or failed) so it
   *  drops out of the badge. Used by the "Dismiss" affordance on the
   *  failure card and the recent-build summary. */
  acknowledge: (crewId: string) => void
}

/** One structured step derived from the `provision.event` feed. Keyed so the
 *  same logical step (e.g. installing `ansible`) is updated in place across its
 *  started → completed/failed transitions instead of rendering three rows. */
export interface ProvisionStepState {
  key: string
  label: string
  feature?: string
  status: "started" | "completed" | "failed"
  durationMs?: number
}

/** Compact summary of the last finished build, kept after the live feed has
 *  gone quiet so a fast build (or a failure) stays visible. */
export interface RecentBuild {
  outcome: "completed" | "failed"
  /** epoch ms when the build finished — drives "built 34s ago". */
  at: number
  steps?: ProvisionStepState[]
  stepCount: number
  /** feature IDs that installed successfully, for "ansible ✓ terraform ✓". */
  features: string[]
  error?: string
  failedStep?: string
  buildLogTail?: string[]
  durationMs?: number
}

export interface ProvisioningCrewState {
  id: string
  slug: string
  name: string
  status: "idle" | "needs_provision" | "running" | "failed" | "completed"
  error?: string
  /** Feature IDs active on this crew, derived from devcontainer_config.
   *  Used by the popover to render feature icons next to each crew. */
  featureIds: string[]
  /** Live progress fields — populated for `running` (and the most recent
   *  message also persists into `failed` / fresh `completed` for context). */
  step?: number
  total?: number
  message?: string
  /** Tail of recent progress messages — bounded server-side to ~50 entries.
   *  Lets the popover show a few lines of context without a chatty endpoint. */
  logTail?: string[]
  /** Number of agents in this crew running on a stale image. >0 means
   *  the user should hit Restart agents. Server returns 0 when the live
   *  container's image already matches cached_image. */
  agentsPendingRestart?: number
  /** Ordered checklist of step labels for the in-flight build. Populated
   *  via the `provision.started` WS event (or replayed from GET when a
   *  client joins mid-build). Matches each label verbatim against the
   *  per-step `message` so the UI can drive a done/active/pending row
   *  per step by string equality. This is the *coarse* source — preferred
   *  only when `eventSteps` is empty. */
  steps?: string[]

  // --- client-accumulated fields (never returned by GET; carried across
  //     refetches by mergeDetail) ---

  /** Richer, feature-granular step list folded from `provision.event`.
   *  Preferred over `steps` for rendering when non-empty. */
  eventSteps?: ProvisionStepState[]
  /** Feature currently installing, e.g. "ansible" — drives the granular
   *  "installing ansible" label in the card/badge. */
  activeFeature?: string
  /** Bounded BuildKit log tail captured on a build failure, surfaced behind
   *  an expandable "build log" in the card. */
  buildLogTail?: string[]
  /** Human label of the step a failed build died on. */
  failedStep?: string
  /** Lingering summary of the last finished build (see RecentBuild). */
  recent?: RecentBuild
  /** User dismissed this crew's lingering recent/failed state — drops it from
   *  the badge until the next build starts. */
  acknowledged?: boolean
  /** epoch ms the current build started — used to compute durationMs. */
  buildStartedAt?: number
}

interface CrewListEntry {
  id: string
  slug: string
  name: string
  devcontainer_config: string | null
  cached_image: string | null
}

interface ProvisionStatusResponse {
  status?: string
  error?: string
  cached_image?: string | null
  devcontainer_config?: string | null
  step?: number
  total?: number
  message?: string
  log_tail?: string[]
  agents_pending_restart?: number
  steps?: string[]
}

/** Wire shape of a `provision.event` frame (see internal/api/crew_provisioning_jobs.go). */
interface ProvisionEventPayload {
  crew_id?: string
  phase?: string
  step?: string
  feature?: string
  status?: string
  detail?: string
  error?: string
  tag?: string
  duration_ms?: number
}

const EMPTY: ProvisioningSummary = {
  needsProvision: 0, building: 0, failed: 0, pendingRestart: 0,
  recentlyCompleted: 0, total: 0, detail: [],
}

/** Friendly labels for the non-feature pipeline steps. */
const STEP_LABELS: Record<string, string> = {
  "provision.start": "Starting",
  resolve_features: "Resolving features",
  image_build_start: "Building image",
  image_build_done: "Image built",
  container_create: "Creating container",
  containerEnv_apply: "Applying environment",
  ready: "Ready",
  "provision.cache_hit": "Cache hit",
  "provision.failed": "Failed",
}

export function useProvisioningStatus(workspaceId: string | null): ProvisioningStatus {
  const [summary, setSummary] = useState<ProvisioningSummary>(EMPTY)
  const cancelRef = useRef(false)
  const { subscribe } = useRealtime()

  const refresh = useCallback(async () => {
    if (!workspaceId) {
      setSummary(EMPTY)
      return
    }
    try {
      const listRes = await apiFetch(`/api/v1/crews?workspace_id=${workspaceId}`)
      if (!listRes.ok) return
      const crews: CrewListEntry[] = await listRes.json()
      if (!Array.isArray(crews)) return

      // Fan out provision status per crew. Use Promise.allSettled so a
      // single 5xx for one crew doesn't blank the whole badge.
      const results = await Promise.allSettled(
        crews.map(async (c): Promise<ProvisioningCrewState> => {
          const r = await apiFetch(`/api/v1/crews/${c.id}/provision?workspace_id=${workspaceId}`)
          const featureIds = extractFeatureIds(c.devcontainer_config)
          if (!r.ok) return { id: c.id, slug: c.slug, name: c.name, status: "idle", featureIds }
          const data: ProvisionStatusResponse = await r.json()
          const hasConfig = Boolean(data.devcontainer_config)
          const hasImage = Boolean(data.cached_image)
          let status: ProvisioningCrewState["status"] = "idle"
          if (data.status === "running") status = "running"
          else if (data.status === "failed") status = "failed"
          else if (hasConfig && !hasImage) status = "needs_provision"
          else if (hasImage) status = "completed"
          return {
            id: c.id,
            slug: c.slug,
            name: c.name,
            status,
            error: data.error,
            featureIds,
            step: data.step,
            total: data.total,
            message: data.message,
            logTail: data.log_tail,
            agentsPendingRestart: data.agents_pending_restart,
            steps: data.steps,
          }
        }),
      )

      if (cancelRef.current) return
      const serverDetail: ProvisioningCrewState[] = results
        .filter((r): r is PromiseFulfilledResult<ProvisioningCrewState> => r.status === "fulfilled")
        .map((r) => r.value)
      // Merge server-owned status fields over the client-accumulated lingering
      // state (eventSteps / recent / acknowledged) so a refetch never wipes the
      // granular progress or the "just built" summary we built from WS frames.
      setSummary((prev) => rollup(mergeDetail(prev.detail, serverDetail)))
    } catch { /* toolbar must never crash */ }
  }, [workspaceId])

  useEffect(() => {
    cancelRef.current = false
    void refresh()
    return () => { cancelRef.current = true }
  }, [refresh])

  // Adaptive polling: tight while a build is in flight, relaxed otherwise.
  // WS handles the in-flight case primarily; this is just a guard against
  // missed frames and the source of truth for status transitions on
  // workspaces that lack a working WS connection.
  useEffect(() => {
    const isBusy = summary.building > 0
    const interval = isBusy ? 4000 : 30000
    const id = setInterval(() => { void refresh() }, interval)
    return () => clearInterval(id)
  }, [summary.building, refresh])

  // Prune expired completed-recent summaries so the badge eventually clears.
  // Failures are never pruned here — they linger until acknowledged.
  useEffect(() => {
    const hasCompletedRecent = summary.detail.some((d) => d.recent?.outcome === "completed")
    if (!hasCompletedRecent) return
    const id = setInterval(() => {
      setSummary((prev) => {
        const now = Date.now()
        let changed = false
        const detail = prev.detail.map((d) => {
          if (d.recent?.outcome === "completed" && now - d.recent.at > RECENT_COMPLETED_TTL_MS) {
            changed = true
            return { ...d, recent: undefined }
          }
          return d
        })
        return changed ? rollup(detail) : prev
      })
    }, RECENT_PRUNE_INTERVAL_MS)
    return () => clearInterval(id)
  }, [summary.detail])

  // Live updates over WS — patch a single crew row in place. Avoids a full
  // refetch on every tick when only one crew is building.
  useEffect(() => {
    const unsubStarted = subscribe("provision.started", (ev) => {
      const p = ev.payload as { crew_id?: string; steps?: string[] }
      if (!p.crew_id || !Array.isArray(p.steps)) return
      setSummary((prev) => patchCrew(prev, p.crew_id!, (d) => ({
        ...d,
        status: "running",
        steps: p.steps,
        error: undefined,
        // Reset progress markers — a fresh build resets the checklist…
        step: 0,
        total: p.steps!.length,
        message: undefined,
        logTail: [],
        // …and the client-accumulated granular + lingering state.
        eventSteps: [],
        activeFeature: undefined,
        buildLogTail: undefined,
        failedStep: undefined,
        recent: undefined,
        acknowledged: false,
        buildStartedAt: Date.now(),
      })))
    })
    const unsubProgress = subscribe("provision.progress", (ev) => {
      const p = ev.payload as { crew_id?: string; step?: number; total?: number; message?: string }
      if (!p.crew_id) return
      setSummary((prev) => patchCrew(prev, p.crew_id!, (d) => ({
        ...d,
        status: d.status === "failed" ? "failed" : "running",
        step: p.step,
        total: p.total,
        message: p.message,
        logTail: p.message ? appendTail(d.logTail, p.message) : d.logTail,
      })))
    })
    // Richer, feature-granular feed. Folded into eventSteps + activeFeature and
    // preferred over the coarse provision.started plan when rendering.
    const unsubEvent = subscribe("provision.event", (ev) => {
      const p = ev.payload as ProvisionEventPayload
      if (!p.crew_id) return
      setSummary((prev) => patchCrew(prev, p.crew_id!, (d) => reduceProvisionEvent(d, p)))
    })
    const unsubCompleted = subscribe("provision.completed", (ev) => {
      const p = ev.payload as { crew_id?: string }
      if (!p.crew_id) return
      // Pin a lingering "recent" summary from the granular feed BEFORE the
      // refetch (which would otherwise reset the live progress fields), so a
      // sub-minute build stays visible after it finishes.
      setSummary((prev) => patchCrew(prev, p.crew_id!, (d) => ({
        ...d,
        status: "completed",
        activeFeature: undefined,
        message: undefined,
        recent: {
          outcome: "completed",
          at: Date.now(),
          steps: d.eventSteps,
          stepCount: d.eventSteps?.length ?? d.steps?.length ?? 0,
          features: completedFeatures(d.eventSteps),
          durationMs: d.buildStartedAt ? Date.now() - d.buildStartedAt : undefined,
        },
        acknowledged: false,
      })))
      // Refetch full state so cached_image and any post-build derived
      // counters (e.g. agents_pending_restart) come from the server.
      void refresh()
    })
    const unsubFailed = subscribe("provision.failed", (ev) => {
      const p = ev.payload as { crew_id?: string; error?: string }
      if (!p.crew_id) return
      setSummary((prev) => patchCrew(prev, p.crew_id!, (d) => {
        const error = p.error ?? d.error
        return {
          ...d,
          status: "failed",
          error,
          activeFeature: undefined,
          recent: {
            outcome: "failed",
            at: Date.now(),
            steps: d.eventSteps,
            stepCount: d.eventSteps?.length ?? d.steps?.length ?? 0,
            features: completedFeatures(d.eventSteps),
            error,
            failedStep: d.failedStep,
            buildLogTail: d.buildLogTail,
            durationMs: d.buildStartedAt ? Date.now() - d.buildStartedAt : undefined,
          },
          acknowledged: false,
        }
      }))
    })
    return () => {
      unsubStarted()
      unsubProgress()
      unsubEvent()
      unsubCompleted()
      unsubFailed()
    }
  }, [subscribe, refresh])

  const acknowledge = useCallback((crewId: string) => {
    setSummary((prev) => patchCrew(prev, crewId, (d) => ({
      ...d,
      acknowledged: true,
      recent: undefined,
    })))
  }, [])

  return useMemo(() => ({ ...summary, acknowledge }), [summary, acknowledge])
}

/** Fold one `provision.event` frame into a crew's granular state. Pure. */
function reduceProvisionEvent(d: ProvisioningCrewState, p: ProvisionEventPayload): ProvisioningCrewState {
  const step = p.step ?? ""
  const status = p.status
  const isFailure = step === "provision.failed" || status === "failed"

  // The BuildKit log tail arrives as a *failed* image_build_start carrying the
  // multi-line tail in `detail`. Capture it as the build log without spawning a
  // noisy step row, and mark the crew failed.
  if (step === "image_build_start" && status === "failed" && p.detail) {
    const lines = p.detail.split("\n").filter((l) => l.trim() !== "")
    return {
      ...d,
      status: "failed",
      buildLogTail: lines.slice(-BUILD_LOG_TAIL_CAP),
      error: d.error ?? p.error,
      activeFeature: undefined,
    }
  }

  // Upsert a structured step keyed by feature (preferred) or step name. The key
  // is the dedup rule: the same logical step is updated in place across its
  // started → completed/failed transitions rather than rendered three times.
  const key = p.feature ? `feature:${p.feature}` : `step:${step}`
  const label = p.feature ?? STEP_LABELS[step] ?? step
  const nextStatus: ProvisionStepState["status"] =
    status === "completed" ? "completed" : status === "failed" ? "failed" : "started"
  const eventSteps = upsertStep(d.eventSteps, {
    key, label, feature: p.feature, status: nextStatus, durationMs: p.duration_ms,
  })

  let activeFeature = d.activeFeature
  if (p.feature) {
    if (nextStatus === "started") activeFeature = p.feature
    else if (activeFeature === p.feature) activeFeature = undefined
  }

  if (isFailure) {
    const failedStep = (p.feature ?? STEP_LABELS[step] ?? step) || d.failedStep
    return {
      ...d,
      status: "failed",
      error: p.error ?? d.error,
      eventSteps,
      activeFeature: undefined,
      failedStep,
    }
  }

  return {
    ...d,
    // A late per-feature completed event must never un-fail a crew that
    // already died on an earlier step.
    status: d.status === "failed" ? "failed" : "running",
    eventSteps,
    activeFeature,
  }
}

/** Insert or update a structured step, keyed by `key`. Never downgrades a
 *  terminal (completed/failed) step back to started on an out-of-order frame. */
function upsertStep(prev: ProvisionStepState[] | undefined, next: ProvisionStepState): ProvisionStepState[] {
  const list = prev ?? []
  const idx = list.findIndex((s) => s.key === next.key)
  if (idx === -1) return [...list, next]
  const cur = list[idx]
  const merged: ProvisionStepState = {
    ...cur,
    label: next.label || cur.label,
    feature: next.feature ?? cur.feature,
    status: rankStatus(next.status) >= rankStatus(cur.status) ? next.status : cur.status,
    durationMs: next.durationMs ?? cur.durationMs,
  }
  const copy = list.slice()
  copy[idx] = merged
  return copy
}

function rankStatus(s: ProvisionStepState["status"]): number {
  return s === "started" ? 0 : s === "completed" ? 1 : 2 // failed wins
}

function completedFeatures(steps: ProvisionStepState[] | undefined): string[] {
  return (steps ?? [])
    .filter((s) => s.feature && s.status === "completed")
    .map((s) => s.feature!)
}

/** Carry client-accumulated lingering fields across a server refetch. Server
 *  data owns status/step/error/etc; the granular + recent state is client-owned
 *  and must survive the rebuild. A crew that newly transitions into `running`
 *  (e.g. a retry whose provision.started frame we missed) gets its lingering
 *  state reset so a stale failure can't bleed into the new build. */
function mergeDetail(
  prev: ProvisioningCrewState[],
  server: ProvisioningCrewState[],
): ProvisioningCrewState[] {
  const byId = new Map(prev.map((d) => [d.id, d]))
  return server.map((s) => {
    const p = byId.get(s.id)
    if (!p) return s
    const newBuild = s.status === "running" && p.status !== "running"
    if (newBuild) {
      return {
        ...s,
        eventSteps: [],
        activeFeature: undefined,
        buildLogTail: undefined,
        failedStep: undefined,
        recent: undefined,
        acknowledged: false,
        buildStartedAt: p.buildStartedAt ?? Date.now(),
      }
    }
    return {
      ...s,
      eventSteps: p.eventSteps,
      activeFeature: p.activeFeature,
      buildLogTail: p.buildLogTail,
      failedStep: p.failedStep,
      recent: p.recent,
      acknowledged: p.acknowledged,
      buildStartedAt: p.buildStartedAt,
    }
  })
}

/** Patch a single crew row inside a summary, recomputing the rolled-up counters.
 *  Pure: returns a new summary; safe to use inside a setState callback. */
function patchCrew(
  prev: ProvisioningSummary,
  crewId: string,
  patch: (d: ProvisioningCrewState) => ProvisioningCrewState,
): ProvisioningSummary {
  let touched = false
  const detail = prev.detail.map((d) => {
    if (d.id !== crewId) return d
    touched = true
    return patch(d)
  })
  if (!touched) return prev
  return rollup(detail)
}

/** Recompute the badge counters from a list of per-crew states. Acknowledged
 *  crews are excluded entirely. `recentlyCompleted` keeps a just-finished build
 *  on the badge (so even a sub-minute build is seen) without double-counting a
 *  crew that's already flagged for a restart. */
function rollup(detail: ProvisioningCrewState[]): ProvisioningSummary {
  const live = detail.filter((d) => !d.acknowledged)
  const needsProvision = live.filter((d) => d.status === "needs_provision").length
  const building = live.filter((d) => d.status === "running").length
  const failed = live.filter((d) => d.status === "failed").length
  const pendingRestart = live.filter(
    (d) => d.status === "completed" && (d.agentsPendingRestart ?? 0) > 0,
  ).length
  const recentlyCompleted = live.filter(
    (d) =>
      d.recent?.outcome === "completed" &&
      d.status !== "running" &&
      d.status !== "failed" &&
      !(d.status === "completed" && (d.agentsPendingRestart ?? 0) > 0),
  ).length
  return {
    needsProvision,
    building,
    failed,
    pendingRestart,
    recentlyCompleted,
    total: needsProvision + building + failed + pendingRestart + recentlyCompleted,
    detail,
  }
}

/** Append a message to a bounded ring buffer mirroring the server's cap. */
function appendTail(prev: string[] | undefined, message: string): string[] {
  const next = [...(prev ?? []), message]
  return next.length > 50 ? next.slice(next.length - 50) : next
}

/**
 * Pull feature refs out of a stringified devcontainer.json. Tolerant of
 * NULL / malformed JSON — the badge should never crash the toolbar.
 */
function extractFeatureIds(raw: string | null): string[] {
  if (!raw) return []
  try {
    const parsed = JSON.parse(raw) as { features?: Record<string, unknown> }
    return Object.keys(parsed.features ?? {})
  } catch {
    return []
  }
}
