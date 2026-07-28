"use client"

import { useCallback, useEffect, useState } from "react"
import { apiFetch } from "@/lib/api-fetch"
import type {
  NotificationProvider,
  NotificationProviderCategory,
} from "@/hooks/use-notification-channels"

/**
 * Section order used when the server does not send one (older instance).
 * Deliberately the same order the Go catalog declares, so the page does not
 * silently reorder itself against a server one version behind.
 */
const FALLBACK_CATEGORIES: NotificationProviderCategory[] = [
  { key: "chat", label: "Chat" },
  { key: "push", label: "Push" },
  { key: "incident", label: "Incident" },
]

/**
 * The chat/push provider registry: which destinations this instance supports,
 * the form each one asks for, and whether an admin has it enabled.
 *
 * The form definition is fetched rather than hard-coded on purpose. The UI and
 * the CLI both render from this, so neither carries its own copy of the
 * provider list — and adding a provider stays a backend-only change instead of
 * a change that silently only lands in one of the two surfaces.
 */
export function useNotificationProviders(workspaceId: string | null | undefined) {
  const [providers, setProviders] = useState<NotificationProvider[]>([])
  const [categories, setCategories] = useState<NotificationProviderCategory[]>(FALLBACK_CATEGORIES)
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)

  const refresh = useCallback(async () => {
    if (!workspaceId) {
      setProviders([])
      setLoading(false)
      return
    }
    setLoading(true)
    try {
      const res = await apiFetch(
        `/api/v1/notification-providers?workspace_id=${encodeURIComponent(workspaceId)}`,
      )
      if (!res.ok) throw new Error(`load providers: ${res.status}`)
      const body = await res.json()
      setProviders(Array.isArray(body?.providers) ? body.providers : [])
      // An older server sends no `categories`; keep the built-in order rather
      // than collapsing every provider into one unnamed section.
      setCategories(
        Array.isArray(body?.categories) && body.categories.length > 0
          ? body.categories
          : FALLBACK_CATEGORIES,
      )
      setError(null)
    } catch (e) {
      setError(e instanceof Error ? e.message : "failed to load providers")
      setProviders([])
    } finally {
      setLoading(false)
    }
  }, [workspaceId])

  useEffect(() => {
    void refresh()
  }, [refresh])

  return { providers, categories, loading, error, refresh }
}
