"use client"

import * as React from "react"

import { apiFetch } from "@/lib/api-fetch"
import { withWs } from "@/lib/admin-workspace-query"
import type { KeeperStatus } from "@/app/(dashboard)/admin/types"
import { useAdminWebSocket } from "@/app/(dashboard)/admin/hooks/use-admin-websocket"
import type { Posture } from "./security-model"

/**
 * Admin › Security's instance-level reads: the Keeper engine's state
 * (GET /system/keeper — on or off, the judge and whether it answers, all of
 * it server-wide) and the server's posture (GET /admin/security-posture).
 * Each lands on its own; one failing leaves the other on screen. The decision
 * log and the per-workspace settings come from the instance routes
 * (use-instance-keeper.ts), which cover every workspace.
 *
 * liveTick counts live keeper events; the page reloads the log on each one
 * rather than splicing it in, so it never shows a row the server would not
 * return.
 */
export function useSecurity(workspaceId: string | null, workspaceLoading = false) {
  const [status, setStatus] = React.useState<KeeperStatus | null>(null)
  const [posture, setPosture] = React.useState<Posture | null>(null)
  const [postureError, setPostureError] = React.useState<string | null>(null)
  const [loading, setLoading] = React.useState(true)

  const reload = React.useCallback(async () => {
    // With no workspace (an instance admin need not belong to one) these
    // server-wide reads go without one; they only wait while it is loading.
    if (!workspaceId && workspaceLoading) return
    setLoading(true)
    await Promise.all([
      (async () => {
        try {
          const r = await apiFetch(withWs("/api/v1/system/keeper", workspaceId))
          setStatus(r.ok ? ((await r.json()) as KeeperStatus) : null)
        } catch {
          setStatus(null)
        }
      })(),
      (async () => {
        try {
          const r = await apiFetch(withWs("/api/v1/admin/security-posture", workspaceId))
          if (!r.ok) {
            setPostureError(r.status === 403 ? "Requires an instance administrator." : `Server setup could not be read (HTTP ${r.status}).`)
            return
          }
          setPosture((await r.json()) as Posture)
          setPostureError(null)
        } catch {
          setPostureError("Network error reading the server setup.")
        }
      })(),
    ])
    setLoading(false)
  }, [workspaceId, workspaceLoading])

  React.useEffect(() => { void reload() }, [reload])

  const { keeperLiveEvents, keeperWsStatus } = useAdminWebSocket({ enabled: !!workspaceId, workspaceId })

  return { status, posture, postureError, loading, reload, live: keeperWsStatus, liveTick: keeperLiveEvents.length }
}
