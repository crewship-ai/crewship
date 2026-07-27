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
  // "shoutrrr" is the stored value for every chat/push destination. It is the
  // delivery library's name and predates the provider catalogue; it stays on
  // the wire because existing rows carry it, but it is never shown to a user.
  type: "email" | "webhook" | "shoutrrr"
  url?: string // webhook
  to?: string // email
  secret?: string // webhook, optional — auto-generated when blank
  events?: string[] // completed | failed | all (server default: failed) — legacy #850 path
  provider?: string // see GET /api/v1/notification-providers
  /**
   * The provider form's values, keyed by field key. The normal path: the
   * server composes the delivery URL from these, so the user never sees or
   * types one.
   */
  fields?: Record<string, string>
  /** Pre-composed delivery URL — scripting / backup-restore only. */
  shoutrrr_url?: string
  personal?: boolean // true = your own channel (self-service, any role)
  categories?: string[] // admin allowlist for a workspace channel; omit = every category
  min_priority?: "low" | "medium" | "high" | "urgent"
}

/** Body for testing an unsaved draft (POST /notification-channels/test). */
export interface ChannelDraftTestBody {
  type: "email" | "webhook" | "shoutrrr"
  provider?: string
  fields?: Record<string, string>
  url?: string
  to?: string
  secret?: string
}

/** One input on a provider's form, as described by the providers registry. */
export interface ProviderField {
  key: string
  label: string
  type: "text" | "url" | "password"
  required: boolean
  secret?: boolean
  placeholder?: string
  help?: string
  help_url?: string
}

/**
 * A chat/push destination and the questions it asks.
 *
 * The form definition is served rather than hard-coded so the UI and the CLI
 * ask the same things and adding a provider stays a backend-only change.
 */
export interface NotificationProvider {
  provider: string
  label: string
  blurb: string
  /**
   * Catalog section — "chat" | "push" | "incident". Served by
   * GET /api/v1/notification-providers rather than mapped from the provider
   * name here, so a provider added on the backend lands in the right section
   * without a matching frontend change. Optional because an older server
   * omits it; the catalog falls back to an "Other" section rather than
   * dropping the provider.
   */
  category?: string
  fields: ProviderField[]
  enabled: boolean
}

/** One catalog section, as named by the server. */
export interface NotificationProviderCategory {
  key: string
  label: string
  hint?: string
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
export interface UseChannelsOptions {
  /**
   * true = ask for `?scope=all`: every connection in the workspace, including
   * other members' personal ones. ADMIN/OWNER only — the server silently
   * degrades to the caller's own scope for anyone else rather than 403ing, so
   * passing this from a member view is safe, not a bug waiting to happen.
   *
   * Other people's destinations come back redacted (a Telegram chat id is a
   * contact detail, not workspace configuration); see `isRedacted` below.
   */
  includeEveryone?: boolean
}

export function useNotificationChannels(
  workspaceId: string | null | undefined,
  options: UseChannelsOptions = {},
) {
  const { includeEveryone = false } = options
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
      const q = new URLSearchParams({ workspace_id: workspaceId })
      if (includeEveryone) q.set("scope", "all")
      const res = await apiFetch(`/api/v1/notification-channels?${q.toString()}`, {
        signal: ctrl.signal,
      })
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
  }, [workspaceId, includeEveryone])

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

  // Test a channel that has NOT been saved. The per-id test above needs a
  // persisted row, which means the first time anyone learns whether their
  // webhook URL is right is after they have already committed it — backwards
  // for a form whose whole difficulty is pasting an opaque token correctly.
  // Nothing is stored; the server composes, sends once, and drops it.
  const sendDraftTest = useCallback(
    async (body: ChannelDraftTestBody): Promise<void> => {
      if (!workspaceId) return
      const res = await apiFetch(
        `/api/v1/notification-channels/test?workspace_id=${encodeURIComponent(workspaceId)}`,
        {
          method: "POST",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify(body),
        },
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

  return { channels, loading, error, refresh, create, remove, sendTest, sendDraftTest, patch }
}
