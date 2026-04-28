"use client"

import { useCallback, useEffect, useRef, useState } from "react"

/**
 * Aggregate provisioning state for the toolbar badge — surfaces "an
 * image is being built / waiting / failed somewhere" without forcing
 * the user to remember which crew they were editing.
 *
 * Polls every 4 s while ANY crew is busy, every 30 s when idle. With
 * a typical 2-6 crew workspace this is 2-6 small fetches — well under
 * the rate limiter even when re-enabled in production.
 */
export interface ProvisioningSummary {
  needsProvision: number      // user changed config, no image yet
  building: number            // job currently running
  failed: number              // last build crashed
  total: number               // sum of the above
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
}

const EMPTY: ProvisioningSummary = { needsProvision: 0, building: 0, failed: 0, total: 0, detail: [] }

export function useProvisioningStatus(workspaceId: string | null): ProvisioningSummary {
  const [summary, setSummary] = useState<ProvisioningSummary>(EMPTY)
  const cancelRef = useRef(false)

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
          return { id: c.id, slug: c.slug, name: c.name, status, error: data.error, featureIds }
        }),
      )

      if (cancelRef.current) return
      const detail: ProvisioningCrewState[] = results
        .filter((r): r is PromiseFulfilledResult<ProvisioningCrewState> => r.status === "fulfilled")
        .map((r) => r.value)
      const needsProvision = detail.filter((d) => d.status === "needs_provision").length
      const building = detail.filter((d) => d.status === "running").length
      const failed = detail.filter((d) => d.status === "failed").length
      setSummary({ needsProvision, building, failed, total: needsProvision + building + failed, detail })
    } catch { /* toolbar must never crash */ }
  }, [workspaceId])

  useEffect(() => {
    cancelRef.current = false
    void refresh()
    return () => { cancelRef.current = true }
  }, [refresh])

  // Adaptive polling: tight while a build is in flight, relaxed otherwise.
  useEffect(() => {
    const isBusy = summary.building > 0
    const interval = isBusy ? 4000 : 30000
    const id = setInterval(() => { void refresh() }, interval)
    return () => clearInterval(id)
  }, [summary.building, refresh])

  return summary
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
