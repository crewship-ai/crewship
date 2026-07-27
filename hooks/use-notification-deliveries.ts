"use client"

import { useCallback, useEffect, useRef, useState } from "react"
import { apiFetch } from "@/lib/api-fetch"

/**
 * One row of the delivery log — the answer to "why didn't my notification
 * arrive?".
 *
 * Keys are snake_case because that is what the server sends; see
 * internal/notifyroute/deliveries.go, where the json tags are pinned by
 * api.TestDeliveriesWireIsSnakeCase. They were absent once, and the columns
 * that needed them silently rendered blank.
 */
export interface NotificationDelivery {
  id: string
  workspace_id: string
  channel_id: string
  user_id: string
  category: string
  dedup_key: string
  source_kind: string
  source_id: string
  title: string
  /** pending | sent | failed | dropped_pref | dropped_rate */
  status: string
  error?: string
  attempts: number
  created_at: string
  updated_at: string
  sent_at?: string
}

export interface DeliveryFilter {
  status?: string
  channelId?: string
  category?: string
  limit?: number
}

/**
 * The workspace delivery log. ADMIN/OWNER only — the server answers 403 for
 * anyone else, and `forbidden` is reported separately from `error` so the view
 * can say "this view is for admins" instead of "something went wrong": a
 * permission boundary is not a failure.
 */
export function useNotificationDeliveries(
  workspaceId: string | null | undefined,
  filter: DeliveryFilter = {},
) {
  const [deliveries, setDeliveries] = useState<NotificationDelivery[]>([])
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)
  const [forbidden, setForbidden] = useState(false)
  const abortRef = useRef<AbortController | null>(null)

  const { status, channelId, category, limit } = filter

  const refresh = useCallback(async () => {
    if (!workspaceId) {
      setDeliveries([])
      setLoading(false)
      return
    }
    abortRef.current?.abort()
    const ctrl = new AbortController()
    abortRef.current = ctrl
    setLoading(true)
    try {
      const q = new URLSearchParams({ workspace_id: workspaceId })
      if (status) q.set("status", status)
      if (channelId) q.set("channel_id", channelId)
      if (category) q.set("category", category)
      if (limit) q.set("limit", String(limit))

      const res = await apiFetch(`/api/v1/notification-deliveries?${q.toString()}`, {
        signal: ctrl.signal,
      })
      if (ctrl.signal.aborted) return
      if (res.status === 403) {
        setForbidden(true)
        setDeliveries([])
        setError(null)
        return
      }
      if (!res.ok) throw new Error(`load deliveries: ${res.status}`)
      const body = await res.json()
      setForbidden(false)
      setDeliveries(Array.isArray(body?.deliveries) ? body.deliveries : [])
      setError(null)
    } catch (e) {
      if (ctrl.signal.aborted) return
      setError(e instanceof Error ? e.message : "failed to load deliveries")
      setDeliveries([])
    } finally {
      if (!ctrl.signal.aborted) setLoading(false)
    }
  }, [workspaceId, status, channelId, category, limit])

  useEffect(() => {
    void refresh()
    return () => abortRef.current?.abort()
  }, [refresh])

  return { deliveries, loading, error, forbidden, refresh }
}
