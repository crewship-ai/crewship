"use client"

import { useCallback, useEffect, useMemo, useState } from "react"
import Link from "next/link"
import { ArrowUpRight, Bell, BellOff, Mail, MessageSquare, Search, Siren, Smartphone, Webhook, type LucideIcon } from "lucide-react"
import { toast } from "sonner"

import { cn } from "@/lib/utils"
import { Switch } from "@/components/ui/switch"
import { Skeleton } from "@/components/ui/skeleton"
import { StatusDot } from "@/components/ui/status-badge"
import { SettingsCard, SettingsRow } from "@/components/features/settings/shared"
import { ProviderMark } from "@/components/features/integrations/provider-marks"
import { apiFetch } from "@/lib/api-fetch"
import { FilterChip, Kpi } from "./admin-kit"

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

type Filter = "all" | "on" | "off" | "used"
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
  const [query, setQuery] = useState("")
  const [filter, setFilter] = useState<Filter>("all")

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
      setCategories(Array.isArray(data?.categories) ? data.categories : [])
    } catch (e) {
      setError(e instanceof Error ? e.message : "Network error")
    } finally {
      setLoading(false)
    }
    // Usage and the email transport are context: a failure leaves them out.
    try {
      const res = await apiFetch(`/api/v1/notification-channels?scope=all&workspace_id=${workspaceId}`)
      const body = res?.ok ? await res.json() : null
      setChannels(Array.isArray(body?.channels) ? body.channels : null)
    } catch {
      setChannels(null)
    }
    try {
      const res = await apiFetch(`/api/v1/admin/security-posture?workspace_id=${workspaceId}`)
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

  const handleToggle = useCallback(async (provider: string, next: boolean) => {
    if (!workspaceId) return
    const inUse = usage[provider] ?? 0
    if (!next && inUse > 0 && !window.confirm(`${inUse} channel${inUse === 1 ? "" : "s"} in this workspace send through ${provider}. Switch it off and they stop delivering?`)) return
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
  }, [workspaceId, usage])

  const shown = useMemo(() => {
    const q = query.trim().toLowerCase()
    return providers.filter((p) => {
      if (q && !`${p.provider} ${p.label ?? ""} ${p.blurb ?? ""}`.toLowerCase().includes(q)) return false
      if (filter === "on") return p.enabled
      if (filter === "off") return !p.enabled
      if (filter === "used") return (usage[p.provider] ?? 0) > 0
      return true
    })
  }, [providers, query, filter, usage])

  // Categories as the server orders them; a provider with an unknown one
  // still shows, under "Other".
  const groups = useMemo(() => {
    const known = categories.length ? categories : [{ key: "", label: "Providers", hint: "" }]
    const out = known.map((c) => ({ ...c, items: shown.filter((p) => (categories.length ? p.category === c.key : true)) }))
    const rest = categories.length ? shown.filter((p) => !categories.some((c) => c.key === p.category)) : []
    if (rest.length) out.push({ key: "other", label: "Other", hint: "", items: rest })
    return out.filter((g) => g.items.length > 0)
  }, [categories, shown])

  if (loading && providers.length === 0) {
    return <Skeleton className="h-[160px] rounded-xl" />
  }

  const on = providers.filter((p) => p.enabled).length
  const usedCount = providers.filter((p) => (usage[p.provider] ?? 0) > 0).length

  return (
    <div className="space-y-4">
      <div className="grid grid-cols-2 gap-3 lg:grid-cols-4">
        <Kpi icon={Bell} tint="var(--primary)" label="Providers on" value={`${on} / ${providers.length}`} sub={on < providers.length ? `${providers.length - on} switched off` : "Every provider allowed"} />
        <Kpi icon={MessageSquare} tint="var(--purple)" label="Channels" value={channels === null ? "—" : channels.length} sub={channels === null ? "Could not be read" : `${channels.filter((c) => c.scope === "user").length} personal · this workspace`} />
        <Kpi icon={Mail} tint="var(--info)" label="Email" value={emailConfigured === null ? "—" : emailConfigured ? "Ready" : "Not set up"} sub="Resend transport" />
        <Kpi icon={Webhook} tint="var(--success)" label="Webhooks" value={channels === null ? "—" : usage.webhook ?? 0} sub="Signed, always available" />
      </div>

      <div className="flex flex-wrap items-center gap-2">
        <label className="flex h-8 min-w-[220px] items-center gap-2 rounded-md border border-border bg-card px-2.5 text-muted-foreground focus-within:border-primary/40">
          <Search className="h-3.5 w-3.5 shrink-0" />
          <input value={query} onChange={(e) => setQuery(e.target.value)} placeholder="Search providers" aria-label="Search providers"
            className="min-w-0 flex-1 bg-transparent text-xs text-foreground outline-none placeholder:text-muted-foreground-soft" />
        </label>
        <FilterChip pressed={filter === "all"} onClick={() => setFilter("all")} count={providers.length}>All</FilterChip>
        <FilterChip pressed={filter === "on"} onClick={() => setFilter("on")} count={on} dot="bg-success">On</FilterChip>
        <FilterChip pressed={filter === "off"} onClick={() => setFilter("off")} count={providers.length - on}>Off</FilterChip>
        {channels !== null && <FilterChip pressed={filter === "used"} onClick={() => setFilter("used")} count={usedCount}>In use</FilterChip>}
      </div>

      {error ? (
        <div className="rounded-card border border-border bg-card px-4 py-6 text-center text-[11px] text-muted-foreground">
          Failed to load providers ({error})
        </div>
      ) : groups.length === 0 ? (
        <div className="rounded-card border border-border bg-card px-4 py-6 text-center text-[12px] text-muted-foreground">No provider matches.</div>
      ) : (
        groups.map((g) => (
          <SettingsCard key={g.key} icon={CATEGORY_ICON[g.key] ?? Bell} tint={CATEGORY_TINT[g.key] ?? "var(--purple)"}
            title={g.label} description={g.hint || undefined}
            actions={<span className="font-mono text-[11px] text-muted-foreground">{g.items.filter((p) => p.enabled).length}/{g.items.length} on</span>}>
            {g.items.map((p) => {
              const n = usage[p.provider] ?? 0
              return (
                <div key={p.provider} data-provider={p.provider}
                  className={cn("flex items-center gap-3 border-b border-border px-4 py-2.5 last:border-b-0", !p.enabled && "opacity-70")}>
                  {/* The provider's own mark — the same component Integrations
                      draws one click away. */}
                  <ProviderMark provider={p.provider} label={p.label ?? p.provider} className="h-7 w-7" />
                  <span className="min-w-0 flex-1">
                    <span className="flex items-center gap-2 text-xs">
                      <span className="font-medium">{p.label ?? p.provider}</span>
                      <span className="font-mono text-[10.5px] text-muted-foreground">{p.scheme}://</span>
                      {n > 0 && <span className="rounded-full bg-primary/10 px-1.5 font-mono text-[10px] text-primary-hover">{n} channel{n === 1 ? "" : "s"}</span>}
                    </span>
                    {p.blurb && <span className="block truncate text-[11.5px] text-muted-foreground">{p.blurb}</span>}
                  </span>
                  {!p.enabled && <span className="hidden items-center gap-1 text-[11px] text-muted-foreground sm:inline-flex"><BellOff className="h-3 w-3" />Off</span>}
                  <Switch
                    checked={p.enabled}
                    disabled={togglingProvider === p.provider}
                    onCheckedChange={(next) => handleToggle(p.provider, next === true)}
                    aria-label={`${p.enabled ? "Disable" : "Enable"} ${p.provider}`}
                  />
                </div>
              )
            })}
          </SettingsCard>
        ))
      )}

      <SettingsCard icon={Mail} tint="var(--info)" title="Built-in transports" description="Not switched here: each has its own transport">
        <SettingsRow label="Email" description="Sent through Resend; configured on the server (RESEND_API_KEY and RESEND_FROM)">
          <span className="inline-flex items-center gap-1.5 text-[11px] text-muted-foreground">
            <StatusDot status={emailConfigured ? "COMPLETED" : emailConfigured === false ? "BLOCKED" : "PENDING"} />
            {emailConfigured === null ? "Unknown" : emailConfigured ? "Ready" : "Not configured"}
            {channels !== null && ` · ${usage.email ?? 0} channels`}
          </span>
        </SettingsRow>
        <SettingsRow label="Signed webhooks" description="An HTTPS POST with an HMAC signature header" border={false}>
          <span className="inline-flex items-center gap-1.5 text-[11px] text-muted-foreground">
            <StatusDot status="COMPLETED" />Always available{channels !== null && ` · ${usage.webhook ?? 0} channels`}
          </span>
        </SettingsRow>
      </SettingsCard>

      <p className="text-[11px] text-muted-foreground">
        This is the kill switch: switch a provider off and nothing more leaves through it, whatever
        channels already exist, and no new channel can use it. The channels themselves live in{" "}
        {/* This tab decides what MAY be connected; the channels live on
            /integrations. Instance policy is not workspace usage. */}
        <Link href="/integrations?tab=notifications&section=connections" className="inline-flex items-center gap-1 font-medium text-primary hover:underline">
          Integrations → Notifications
          <ArrowUpRight className="size-3" />
        </Link>
        .
      </p>
    </div>
  )
}
