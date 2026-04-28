"use client"

import { useCallback, useEffect, useRef, useState } from "react"
import { useRealtime } from "@/hooks/use-realtime"

/**
 * Aggregate provisioning state for the toolbar badge — surfaces "an
 * image is being built / waiting / failed somewhere" without forcing
 * the user to remember which crew they were editing.
 *
 * Live updates arrive via the workspace WS channel; polling is the
 * fallback when WS is disconnected (every 30 s) and the bootstrap on
 * mount. While a build is in flight we still poll every 4 s as a
 * belt-and-braces guard against missed WS frames.
 */
export interface ProvisioningSummary {
  needsProvision: number      // user changed config, no image yet
  building: number            // job currently running
  failed: number              // last build crashed
  pendingRestart: number      // build complete but agents still on old image (count of crews, not agents)
  total: number               // sum of the four above — the badge counter
  detail: ProvisioningCrewState[]
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
}

const EMPTY: ProvisioningSummary = { needsProvision: 0, building: 0, failed: 0, pendingRestart: 0, total: 0, detail: [] }

export function useProvisioningStatus(workspaceId: string | null): ProvisioningSummary {
  const [summary, setSummary] = useState<ProvisioningSummary>(EMPTY)
  const cancelRef = useRef(false)
  const { subscribe } = useRealtime()

  const refresh = useCallback(async () => {
    if (!workspaceId) {
      setSummary(EMPTY)
      return
    }
    try {
      const listRes = await fetch(`/api/v1/crews?workspace_id=${workspaceId}`)
      if (!listRes.ok) return
      const crews: CrewListEntry[] = await listRes.json()
      if (!Array.isArray(crews)) return

      // Fan out provision status per crew. Use Promise.allSettled so a
      // single 5xx for one crew doesn't blank the whole badge.
      const results = await Promise.allSettled(
        crews.map(async (c): Promise<ProvisioningCrewState> => {
          const r = await fetch(`/api/v1/crews/${c.id}/provision?workspace_id=${workspaceId}`)
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
          }
        }),
      )

      if (cancelRef.current) return
      const detail: ProvisioningCrewState[] = results
        .filter((r): r is PromiseFulfilledResult<ProvisioningCrewState> => r.status === "fulfilled")
        .map((r) => r.value)
      setSummary(rollup(detail))
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

  // Live updates over WS — patch a single crew row in place. Avoids a full
  // refetch on every tick when only one crew is building.
  useEffect(() => {
    const unsubProgress = subscribe("provision.progress", (ev) => {
      const p = ev.payload as { crew_id?: string; step?: number; total?: number; message?: string }
      if (!p.crew_id) return
      setSummary((prev) => patchCrew(prev, p.crew_id!, (d) => ({
        ...d,
        status: "running",
        step: p.step,
        total: p.total,
        message: p.message,
        logTail: p.message ? appendTail(d.logTail, p.message) : d.logTail,
      })))
    })
    const unsubCompleted = subscribe("provision.completed", (ev) => {
      const p = ev.payload as { crew_id?: string }
      if (!p.crew_id) return
      // Refetch full state so cached_image and any post-build derived
      // counters (e.g. agents_pending_restart in PR2) come from the
      // server, not a stale optimistic update.
      void refresh()
    })
    const unsubFailed = subscribe("provision.failed", (ev) => {
      const p = ev.payload as { crew_id?: string; error?: string }
      if (!p.crew_id) return
      setSummary((prev) => patchCrew(prev, p.crew_id!, (d) => ({
        ...d,
        status: "failed",
        error: p.error,
      })))
    })
    return () => {
      unsubProgress()
      unsubCompleted()
      unsubFailed()
    }
  }, [subscribe, refresh])

  return summary
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

/** Recompute the badge counters from a list of per-crew states. The
 *  pendingRestart bucket only counts crews whose build already finished —
 *  while a build is in flight or failed, that signal would just confuse
 *  the user (they'd see two counters about the same crew). */
function rollup(detail: ProvisioningCrewState[]): ProvisioningSummary {
  const needsProvision = detail.filter((d) => d.status === "needs_provision").length
  const building = detail.filter((d) => d.status === "running").length
  const failed = detail.filter((d) => d.status === "failed").length
  const pendingRestart = detail.filter(
    (d) => d.status === "completed" && (d.agentsPendingRestart ?? 0) > 0,
  ).length
  return {
    needsProvision,
    building,
    failed,
    pendingRestart,
    total: needsProvision + building + failed + pendingRestart,
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
