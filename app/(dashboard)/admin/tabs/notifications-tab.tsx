"use client"

import { useCallback, useEffect, useState } from "react"
import Link from "next/link"
import { ArrowUpRight } from "lucide-react"
import { toast } from "sonner"
import { Switch } from "@/components/ui/switch"
import { Skeleton } from "@/components/ui/skeleton"
import { SettingsCard } from "@/components/features/settings/shared"
import { ProviderMark } from "@/components/features/integrations/provider-marks"
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
        description="The instance-wide switch for each delivery provider. Turning one off stops delivery through it immediately — existing channels included — and refuses new ones."
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
                {/* The provider's own mark — the same component Integrations
                    draws one click away. Eleven identical speech bubbles told
                    the reader nothing and made the two pages look like two
                    different products. */}
                <ProviderMark provider={p.provider} label={p.provider} className="h-6 w-6" />
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
        This is the kill switch: switch a provider off and nothing more leaves through it, whatever
        channels already exist. Email and signed-webhook channels are not governed here — they have
        their own transports.
      </p>

      {/* This tab decides what MAY be connected; the channels themselves live
          on /integrations. That split is deliberate — instance policy is not
          workspace usage — but an admin who lands here looking for "is Slack
          connected?" should not have to guess where the other half is. */}
      <p className="text-[11px] text-muted-foreground">
        Looking for the channels themselves?{" "}
        <Link
          href="/integrations?tab=notifications&section=connections"
          className="inline-flex items-center gap-1 font-medium text-primary hover:underline"
        >
          Integrations → Notifications
          <ArrowUpRight className="size-3" />
        </Link>{" "}
        lists every connection on this workspace, who owns it, and whether it is delivering.
      </p>
    </div>
  )
}
