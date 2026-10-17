"use client"

import { useCallback, useEffect, useMemo, useState } from "react"
import Link from "next/link"
import { Bell, Check, ChevronRight, Info, Mail, MessageSquare, Send, Siren, Smartphone, Webhook, type LucideIcon } from "lucide-react"
import { toast } from "@/lib/toast"

import { cn } from "@/lib/utils"
import { Switch } from "@/components/ui/switch"
import { Button } from "@/components/ui/button"
import { ConfirmDialog } from "@/components/ui/confirm-dialog"
import { PageError, PageLoading } from "@/components/layout/page-state"
import { SettingsCard, SettingsRow } from "@/components/features/settings/shared"
import { AdminStrip } from "./admin-kit"
import { ProviderMark } from "@/components/features/integrations/provider-marks"
import { apiFetch } from "@/lib/api-fetch"
import { withWs, wsParam } from "@/lib/admin-workspace-query"
import { StatusPill } from "@/components/ui/status-pill"

interface ProviderInfo {
  provider: string
  scheme: string
  enabled: boolean
  label?: string
  blurb?: string
  category?: string
}
interface Category { key: string; label: string; hint: string }
interface Channel { id: string; type: string; provider?: string; scope?: string; enabled?: boolean }

/** A category shows this many providers before the rest fold behind one row. */
const FOLD_AT = 6
const CATEGORY_ICON: Record<string, LucideIcon> = { chat: MessageSquare, push: Smartphone, incident: Siren }
const CATEGORY_TINT: Record<string, string> = { chat: "var(--primary)", push: "var(--purple)", incident: "var(--destructive)" }

/**
 * Admin → Notifications: the instance-wide switch for each shoutrrr provider
 * (#1412), grouped the way people choose them — chat rooms, a push to one
 * person, on-call incidents — with how many of this workspace's channels use
 * each one. Switching a provider off is a kill switch: new channels are
 * refused AND dispatch refuses to send through it (internal/notify/dispatch.go),
 * so existing channels stop too. Email and signed webhooks have their own
 * transports and are shown for completeness.
 */
export function NotificationsTab({ workspaceId }: { workspaceId: string | null }) {
  const [providers, setProviders] = useState<ProviderInfo[]>([])
  const [categories, setCategories] = useState<Category[]>([])
  const [channels, setChannels] = useState<Channel[] | null>(null)
  const [emailConfigured, setEmailConfigured] = useState<boolean | null>(null)
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)
  const [togglingProvider, setTogglingProvider] = useState<string | null>(null)
  // The provider whose row is asking "switch off?", and the categories unfolded.
  const [pending, setPending] = useState<string | null>(null)
  const [expanded, setExpanded] = useState<Record<string, boolean>>({})

  const refresh = useCallback(async () => {
    setLoading(true)
    setError(null)
    try {
      const res = await apiFetch(withWs("/api/v1/notification-providers", workspaceId))
      if (!res.ok) {
        setError(`HTTP ${res.status}`)
        return
      }
      const data = await res.json()
      setProviders(Array.isArray(data?.providers) ? data.providers : [])
      setCategories(Array.isArray(data?.categories) ? data.categories : [])
    } catch (e) {
      setError(e instanceof Error ? e.message : "Network error")
    } finally {
      setLoading(false)
    }
    // Usage and the email transport are context: a failure leaves them out.
    // Channels belong to a workspace; with none there is no usage to show.
    if (workspaceId) {
      try {
        const res = await apiFetch(`/api/v1/notification-channels?scope=all&${wsParam(workspaceId)}`)
        const body = res?.ok ? await res.json() : null
        setChannels(Array.isArray(body?.channels) ? body.channels : null)
      } catch {
        setChannels(null)
      }
    } else {
      setChannels(null)
    }
    try {
      const res = await apiFetch(withWs("/api/v1/admin/security-posture", workspaceId))
      const body = res?.ok ? await res.json() : null
      setEmailConfigured(typeof body?.email_configured === "boolean" ? body.email_configured : null)
    } catch {
      setEmailConfigured(null)
    }
  }, [workspaceId])

  useEffect(() => { refresh() }, [refresh])

  const usage = useMemo(() => {
    const m: Record<string, number> = {}
    for (const c of channels ?? []) {
      const k = c.provider || c.type
      m[k] = (m[k] ?? 0) + 1
    }
    return m
  }, [channels])

  const handleToggle = useCallback(async (provider: string, next: boolean): Promise<boolean> => {
    setTogglingProvider(provider)
    // Optimistic flip.
    setProviders((prev) => prev.map((p) => (p.provider === provider ? { ...p, enabled: next } : p)))
    try {
      const res = await apiFetch(
        withWs(`/api/v1/notification-providers/${encodeURIComponent(provider)}`, workspaceId),
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
      toast.success(`${provider} ${next ? "allowed" : "switched off"}`)
      return true
    } catch (e) {
      // Roll back on failure.
      setProviders((prev) => prev.map((p) => (p.provider === provider ? { ...p, enabled: !next } : p)))
      toast.error(e instanceof Error ? e.message : "Failed to update provider")
      return false
    } finally {
      setTogglingProvider(null)
    }
  }, [workspaceId])

  // Switching off a provider channels send through silences them, so it asks
  // first (the shared confirmation); an unused one flips straight away.
  const requestToggle = useCallback((provider: string, next: boolean) => {
    if (!next && (usage[provider] ?? 0) > 0) {
      setPending(provider)
      return
    }
    void handleToggle(provider, next)
  }, [usage, handleToggle])

  // Categories as the server orders them; a provider with an unknown one
  // still shows, under "Other".
  const groups = useMemo(() => {
    const known = categories.length ? categories : [{ key: "", label: "Providers", hint: "" }]
    const out = known.map((c) => ({ ...c, items: providers.filter((p) => (categories.length ? p.category === c.key : true)) }))
    const rest = categories.length ? providers.filter((p) => !categories.some((c) => c.key === p.category)) : []
    if (rest.length) out.push({ key: "other", label: "Other", hint: "", items: rest })
    return out.filter((g) => g.items.length > 0)
  }, [categories, providers])

  if (loading && providers.length === 0) {
    return <PageLoading rows={4} className="px-0 py-0" />
  }

  const on = providers.filter((p) => p.enabled).length
  const channelTotal = channels?.length ?? 0
  const pendingInfo = pending ? providers.find((p) => p.provider === pending) : undefined
  const pendingName = pendingInfo ? pendingInfo.label ?? pendingInfo.provider : ""
  const pendingCount = pending ? usage[pending] ?? 0 : 0

  return (
    <div className="space-y-4">
      {/* What the switch does, before any switch (Harbor 2 banner). This page
          decides what MAY be connected; the channels live in Integrations. */}
      <div role="note" data-slot="notifications-banner" className="flex flex-wrap items-center gap-3 rounded-card bg-primary/10 px-3.5 py-2.5 text-primary-hover">
        <Info className="size-4 shrink-0" aria-hidden />
        <div className="min-w-0 flex-[1_1_15rem]">
          <b className="block font-semibold text-foreground">One switch for the whole Crewship</b>
          <span className="block text-label text-muted-foreground">
            A service switched off here disappears from Add channel in every workspace, and its existing channels stop sending.
            Workspaces then choose their own channels in Integrations › Channels.
          </span>
        </div>
        <Button asChild variant="outline" size="sm" className="h-7 px-2.5 text-xs">
          <Link href="/integrations?tab=notifications&section=connections">Open Channels<ChevronRight className="size-3" /></Link>
        </Button>
      </div>

      <AdminStrip label="Notifications at a glance" items={[
        { slot: "allowed", icon: <Check />, label: "Services allowed", value: `${on} of ${providers.length}` },
        ...(channels !== null ? [{ slot: "channels", icon: <Send />, label: "Channels using them", value: `${channelTotal} in this workspace` }] : []),
        { slot: "email", icon: <Mail />, label: "Email", value: emailConfigured === null ? "Unknown" : emailConfigured ? "Resend · configured" : "Not set up" },
      ]} />

      {error ? (
        <PageError className="px-0 py-0" title="Couldn’t load the notification services" detail={`${error} from GET /api/v1/notification-providers`} onRetry={() => void refresh()} />
      ) : (
        groups.map((g) => {
          const folded = !expanded[g.key] && g.items.length > FOLD_AT + 1
          const items = folded ? g.items.slice(0, FOLD_AT) : g.items
          return (
            <SettingsCard key={g.key} icon={CATEGORY_ICON[g.key] ?? Bell} tint={CATEGORY_TINT[g.key] ?? "var(--purple)"}
              title={g.label} description={g.hint || undefined}>
              {items.map((p) => {
                const n = usage[p.provider] ?? 0
                const name = p.label ?? p.provider
                return (
                  <div key={p.provider} data-provider={p.provider} className="flex items-center gap-3 border-b border-border px-4 py-3 last:border-b-0">
                    {/* The provider's own mark — the same component Integrations
                        draws one click away. */}
                    <ProviderMark provider={p.provider} label={name} className={cn("h-7 w-7", !p.enabled && "opacity-60")} />
                    <span className={cn("min-w-0 flex-1", !p.enabled && "opacity-60")}>
                      <span className="block text-control font-medium">{name}</span>
                      {p.blurb && <span className="block truncate text-label text-muted-foreground" title={p.blurb}>{p.blurb}</span>}
                    </span>
                    <span className="flex shrink-0 items-center justify-end gap-2.5">
                      {channels !== null && (
                        <span className="text-label text-muted-foreground">{n > 0 ? `${n} channel${n === 1 ? "" : "s"}` : "not used"}</span>
                      )}
                      {!p.enabled && <StatusPill tone="muted" label="Off" />}
                      <Switch
                        checked={p.enabled}
                        disabled={togglingProvider === p.provider}
                        onCheckedChange={(next) => requestToggle(p.provider, next === true)}
                        aria-label={`${p.enabled ? "Disable" : "Enable"} ${p.provider}`}
                      />
                    </span>
                  </div>
                )
              })}
              {folded && (
                <button type="button" onClick={() => setExpanded((prev) => ({ ...prev, [g.key]: true }))}
                  className="flex w-full items-center gap-1.5 px-4 py-2.5 text-left text-xs text-muted-foreground hover:bg-accent hover:text-foreground">
                  <ChevronRight className="size-3.5" />
                  Show {g.items.length - FOLD_AT} more
                </button>
              )}
            </SettingsCard>
          )
        })
      )}

      <SettingsCard icon={Mail} tint="var(--info)" title="Built-in transports" description="Always available; set in the server environment">
        <SettingsRow lead={Mail} label="Email" description="Resend · set RESEND_API_KEY and RESEND_FROM">
          <StatusPill tone={emailConfigured ? "success" : emailConfigured === false ? "warn" : "muted"}
            label={emailConfigured === null ? "Unknown" : emailConfigured ? "Configured" : "Not set up"} />
          {channels !== null && (usage.email ?? 0) > 0 && <span className="text-label text-muted-foreground">{usage.email} channels</span>}
        </SettingsRow>
        <SettingsRow lead={Webhook} label="Signed webhooks" description="HTTPS POST with an HMAC signature header">
          <StatusPill tone="success" label="Available" />
          {channels !== null && (usage.webhook ?? 0) > 0 && <span className="text-label text-muted-foreground">{usage.webhook} channels</span>}
        </SettingsRow>
      </SettingsCard>

      <ConfirmDialog open={pending !== null} onOpenChange={(o) => { if (!o) setPending(null) }} destructive
        title={`Switch ${pendingName} off?`}
        description="For the whole Crewship: it disappears from Add channel in every workspace."
        consequences={[
          { tone: "lost", text: `${pendingCount} channel${pendingCount === 1 ? "" : "s"} stop sending through ${pendingName}` },
          { tone: "kept", text: "The channels stay; switching it back on resumes them" },
        ]}
        confirmLabel="Switch off"
        onConfirm={async () => {
          if (!pending) return
          if (!(await handleToggle(pending, false))) throw new Error("not switched off")
        }} />
    </div>
  )
}
