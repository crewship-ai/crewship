"use client"

import { useCallback, useEffect, useRef, useState } from "react"
import { apiFetch } from "@/lib/api-fetch"

// NotificationChannel mirrors internal/notify.Channel as serialized by
// GET /api/v1/notification-channels. The webhook signing secret / shoutrrr
// service url is NEVER returned by list — it surfaces exactly once, on
// the create response (Stripe/GitHub-style one-time reveal).
export interface NotificationChannel {
  id: string
  workspace_id: string
  type: "email" | "webhook" | "shoutrrr" | string
  provider?: string // slack | discord | telegram — set when type is "shoutrrr"
  url?: string
  to?: string
  events: string[]
  enabled: boolean
  created_by?: string
  created_at?: string
  // #1412 — two-layer preference system.
  scope?: "workspace" | "user"
  owner_user_id?: string
  categories?: string[] // admin allowlist; empty/omitted = every category
  min_priority?: "low" | "medium" | "high" | "urgent"
}

export interface ChannelCreateBody {
  type: "email" | "webhook" | "shoutrrr"
  url?: string // webhook
  to?: string // email
  secret?: string // webhook, optional — auto-generated when blank
  events?: string[] // completed | failed | all (server default: failed) — legacy #850 path
  provider?: string // slack | discord | telegram — required for type "shoutrrr"
  shoutrrr_url?: string // the Apprise-style service url — required for type "shoutrrr"
  personal?: boolean // true = your own channel (self-service, any role)
  categories?: string[] // admin allowlist for a workspace channel; omit = every category
  min_priority?: "low" | "medium" | "high" | "urgent"
}

export interface ChannelPatchBody {
  enabled?: boolean
  categories?: string[]
  min_priority?: "low" | "medium" | "high" | "urgent"
  events?: string[]
}

/** Create response: the channel plus, for webhooks, the one-time secret. */
export interface CreatedChannel extends NotificationChannel {
  secret?: string
}

/**
 * CRUD + test over the workspace's outbound notification channels
 * (email / signed webhook run-terminal delivery, issue #850). Writes are
 * MANAGER+ server-side; failed writes surface as thrown errors with the
 * server's message so the section can toast them verbatim.
 */
export function useNotificationChannels(workspaceId: string | null | undefined) {
  const [channels, setChannels] = useState<NotificationChannel[]>([])
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const abortRef = useRef<AbortController | null>(null)

  const refresh = useCallback(async () => {
    if (!workspaceId) {
      setChannels([])
      return
    }
    abortRef.current?.abort()
    const ctrl = new AbortController()
    abortRef.current = ctrl
    setLoading(true)
    setError(null)
    try {
      const res = await apiFetch(
        `/api/v1/notification-channels?workspace_id=${encodeURIComponent(workspaceId)}`,
        { signal: ctrl.signal },
      )
      if (ctrl.signal.aborted) return
      if (!res.ok) {
        setError(`notification channels: ${res.status}`)
        return
      }
      const data = await res.json()
      if (ctrl.signal.aborted) return
      setChannels(Array.isArray(data?.channels) ? data.channels : [])
    } catch (e) {
      if (ctrl.signal.aborted) return
      setError(e instanceof Error ? e.message : String(e))
    } finally {
      if (!ctrl.signal.aborted) setLoading(false)
    }
  }, [workspaceId])

  useEffect(() => {
    refresh()
    return () => abortRef.current?.abort()
  }, [refresh])

  const create = useCallback(
    async (body: ChannelCreateBody): Promise<CreatedChannel | null> => {
      if (!workspaceId) return null
      const res = await apiFetch(
        `/api/v1/notification-channels?workspace_id=${encodeURIComponent(workspaceId)}`,
        {
          method: "POST",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify(body),
        },
      )
      if (!res.ok) {
        const errBody = await res.json().catch(() => null)
        throw new Error(errBody?.error ?? errBody?.detail ?? `create channel: ${res.status}`)
      }
      const out: CreatedChannel = await res.json()
      await refresh()
      return out
    },
    [workspaceId, refresh],
  )

  const remove = useCallback(
    async (id: string): Promise<void> => {
      if (!workspaceId) return
      const res = await apiFetch(
        `/api/v1/notification-channels/${encodeURIComponent(id)}?workspace_id=${encodeURIComponent(workspaceId)}`,
        { method: "DELETE" },
      )
      if (!res.ok && res.status !== 404) {
        const errBody = await res.json().catch(() => null)
        throw new Error(errBody?.error ?? errBody?.detail ?? `delete channel: ${res.status}`)
      }
      await refresh()
    },
    [workspaceId, refresh],
  )

  const sendTest = useCallback(
    async (id: string): Promise<void> => {
      if (!workspaceId) return
      const res = await apiFetch(
        `/api/v1/notification-channels/${encodeURIComponent(id)}/test?workspace_id=${encodeURIComponent(workspaceId)}`,
        { method: "POST" },
      )
      if (!res.ok) {
        const errBody = await res.json().catch(() => null)
        throw new Error(errBody?.error ?? errBody?.detail ?? `test send: ${res.status}`)
      }
    },
    [workspaceId],
  )

  const patch = useCallback(
    async (id: string, body: ChannelPatchBody): Promise<void> => {
      if (!workspaceId) return
      const res = await apiFetch(
        `/api/v1/notification-channels/${encodeURIComponent(id)}?workspace_id=${encodeURIComponent(workspaceId)}`,
        {
          method: "PATCH",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify(body),
        },
      )
      if (!res.ok) {
        const errBody = await res.json().catch(() => null)
        throw new Error(errBody?.error ?? errBody?.detail ?? `update channel: ${res.status}`)
      }
      await refresh()
    },
    [workspaceId, refresh],
  )

  return { channels, loading, error, refresh, create, remove, sendTest, patch }
}
