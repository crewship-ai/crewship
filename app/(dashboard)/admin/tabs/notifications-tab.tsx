"use client"

import { useCallback, useEffect, useState } from "react"
import { MessageSquare } from "lucide-react"
import { toast } from "sonner"
import { Switch } from "@/components/ui/switch"
import { Skeleton } from "@/components/ui/skeleton"
import { SettingsCard } from "@/components/features/settings/shared"
import { apiFetch } from "@/lib/api-fetch"

interface ProviderInfo {
  provider: string
  scheme: string
  enabled: boolean
}

/**
 * Admin → Notifications: instance-wide enable/disable for each shoutrrr
 * provider (issue #1412). A disabled provider is rejected at
 * CHANNEL-CREATE time (fail-closed) — it does not retroactively break
 * channels that already exist, matching the mailer-transport-removed
 * degrade elsewhere in this system.
 */
export function NotificationsTab({ workspaceId }: { workspaceId: string | null }) {
  const [providers, setProviders] = useState<ProviderInfo[]>([])
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)
  const [togglingProvider, setTogglingProvider] = useState<string | null>(null)

  const refresh = useCallback(async () => {
    if (!workspaceId) return
    setLoading(true)
    setError(null)
    try {
      const res = await apiFetch(`/api/v1/notification-providers?workspace_id=${workspaceId}`)
      if (!res.ok) {
        setError(`HTTP ${res.status}`)
        return
      }
      const data = await res.json()
      setProviders(Array.isArray(data?.providers) ? data.providers : [])
    } catch (e) {
      setError(e instanceof Error ? e.message : "Network error")
    } finally {
      setLoading(false)
    }
  }, [workspaceId])

  useEffect(() => { refresh() }, [refresh])

  const handleToggle = useCallback(async (provider: string, next: boolean) => {
    if (!workspaceId) return
    setTogglingProvider(provider)
    // Optimistic flip.
    setProviders((prev) => prev.map((p) => (p.provider === provider ? { ...p, enabled: next } : p)))
    try {
      const res = await apiFetch(
        `/api/v1/notification-providers/${encodeURIComponent(provider)}?workspace_id=${workspaceId}`,
        {
          method: "PATCH",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify({ enabled: next }),
        },
      )
      if (!res.ok) {
        const errBody = await res.json().catch(() => null)
        throw new Error(errBody?.error ?? errBody?.detail ?? `HTTP ${res.status}`)
      }
      toast.success(`${provider} ${next ? "enabled" : "disabled"}`)
    } catch (e) {
      // Roll back on failure.
      setProviders((prev) => prev.map((p) => (p.provider === provider ? { ...p, enabled: !next } : p)))
      toast.error(e instanceof Error ? e.message : "Failed to update provider")
    } finally {
      setTogglingProvider(null)
    }
  }, [workspaceId])

  if (loading && providers.length === 0) {
    return <Skeleton className="h-[160px] rounded-xl" />
  }

  return (
    <div className="space-y-4">
      <SettingsCard
        title="Notification providers"
        description="Instance-wide enable/disable for each shoutrrr delivery provider — a disabled provider is rejected at channel-create time"
      >
        {error ? (
          <div className="px-4 py-6 text-center text-[11px] text-muted-foreground">
            Failed to load providers ({error})
          </div>
        ) : (
          providers.map((p, i) => (
            <div
              key={p.provider}
              className={
                "flex items-center justify-between gap-4 px-4 py-2.5" +
                (i < providers.length - 1 ? " border-b border-border/40" : "")
              }
            >
              <div className="flex items-center gap-2 text-xs text-foreground">
                <MessageSquare className="size-3 text-muted-foreground" />
                <span className="capitalize font-medium">{p.provider}</span>
                <span className="text-[10px] text-muted-foreground font-mono">{p.scheme}://</span>
              </div>
              <Switch
                checked={p.enabled}
                disabled={togglingProvider === p.provider}
                onCheckedChange={(next) => handleToggle(p.provider, next === true)}
                aria-label={`${p.enabled ? "Disable" : "Enable"} ${p.provider}`}
              />
            </div>
          ))
        )}
      </SettingsCard>

      <p className="text-[11px] text-muted-foreground">
        Email and signed-webhook channels are always available — this toggle only governs Slack, Discord,
        and Telegram (shoutrrr) channel creation.
      </p>
    </div>
  )
}
