"use client"

import * as React from "react"

import { apiFetch } from "@/lib/api-fetch"
import type { KeeperLogEntry, KeeperStatus } from "@/app/(dashboard)/admin/types"
import { useAdminWebSocket } from "@/app/(dashboard)/admin/hooks/use-admin-websocket"
import type { Posture } from "./security-model"

/**
 * Admin › Security's reads: the Keeper's state (GET /system/keeper), every
 * keeper_requests row the workspace has — credential requests and the four
 * Phase-2 reviews alike (GET /admin/keeper/requests, the server caps at 200) —
 * and the server's posture (GET /admin/security-posture). Each lands on its
 * own; one failing leaves the others on screen.
 *
 * A live keeper event reloads the list rather than being spliced into it, so
 * the page never shows a row the server would not return.
 */
export function useSecurity(workspaceId: string | null) {
  const [status, setStatus] = React.useState<KeeperStatus | null>(null)
  const [entries, setEntries] = React.useState<KeeperLogEntry[]>([])
  const [posture, setPosture] = React.useState<Posture | null>(null)
  const [postureError, setPostureError] = React.useState<string | null>(null)
  const [activityError, setActivityError] = React.useState<string | null>(null)
  const [loading, setLoading] = React.useState(true)

  const q = workspaceId ? `workspace_id=${encodeURIComponent(workspaceId)}` : ""

  const loadActivity = React.useCallback(async () => {
    if (!workspaceId) return
    try {
      const r = await apiFetch(`/api/v1/admin/keeper/requests?${q}&limit=200`)
      if (!r.ok) { setActivityError(`Activity could not be read (HTTP ${r.status})`); return }
      const rows = (await r.json()) as KeeperLogEntry[]
      setEntries(Array.isArray(rows) ? rows : [])
      setActivityError(null)
    } catch {
      setActivityError("Activity could not be read")
    }
  }, [workspaceId, q])

  const reload = React.useCallback(async () => {
    if (!workspaceId) return
    setLoading(true)
    await Promise.all([
      (async () => {
        try {
          const r = await apiFetch(`/api/v1/system/keeper?${q}`)
          setStatus(r.ok ? ((await r.json()) as KeeperStatus) : null)
        } catch {
          setStatus(null)
        }
      })(),
      (async () => {
        try {
          const r = await apiFetch(`/api/v1/admin/security-posture?${q}`)
          if (!r.ok) {
            setPostureError(r.status === 403 ? "Requires an admin role in this workspace." : `Server setup could not be read (HTTP ${r.status}).`)
            return
          }
          setPosture((await r.json()) as Posture)
          setPostureError(null)
        } catch {
          setPostureError("Network error reading the server setup.")
        }
      })(),
      loadActivity(),
    ])
    setLoading(false)
  }, [workspaceId, q, loadActivity])

  React.useEffect(() => { void reload() }, [reload])

  const { keeperLiveEvents, keeperWsStatus } = useAdminWebSocket({ enabled: !!workspaceId, workspaceId })
  const seen = React.useRef(0)
  React.useEffect(() => {
    if (keeperLiveEvents.length === seen.current) return
    seen.current = keeperLiveEvents.length
    void loadActivity()
  }, [keeperLiveEvents.length, loadActivity])

  return { status, entries, posture, postureError, activityError, loading, reload, live: keeperWsStatus }
}
