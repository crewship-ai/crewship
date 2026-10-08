"use client"

import { useCallback, useEffect, useMemo, useState } from "react"
import Link from "next/link"
import { Bell, ChevronRight, Mail, MessageSquare, Siren, Smartphone, type LucideIcon } from "lucide-react"
import { toast } from "sonner"

import { cn } from "@/lib/utils"
import { Switch } from "@/components/ui/switch"
import { Skeleton } from "@/components/ui/skeleton"
import { StatusDot } from "@/components/ui/status-badge"
import { Button } from "@/components/ui/button"
import { SettingsCard, SettingsRow, SettingsSummary, SummaryItem } from "@/components/features/settings/shared"
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

  const handleToggle = useCallback(async (provider: string, next: boolean) => {
    setPending(null)
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
    } catch (e) {
      // Roll back on failure.
      setProviders((prev) => prev.map((p) => (p.provider === provider ? { ...p, enabled: !next } : p)))
      toast.error(e instanceof Error ? e.message : "Failed to update provider")
    } finally {
      setTogglingProvider(null)
    }
  }, [workspaceId])

  // Switching off a provider channels send through silences them, so the row
  // asks first; an unused one flips straight away.
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
    return <Skeleton className="h-[160px] rounded-xl" />
  }

  const on = providers.filter((p) => p.enabled).length
  const usedCount = providers.filter((p) => (usage[p.provider] ?? 0) > 0).length

  return (
    <div className="space-y-4">
      <SettingsSummary>
        <SummaryItem n={on}>of {providers.length} providers allowed</SummaryItem>
        {channels !== null && <SummaryItem n={usedCount}>in use by channels</SummaryItem>}
        {emailConfigured === false && <SummaryItem tone="warn">Email not set up</SummaryItem>}
        {/* This page decides what MAY be connected; the channels live on
            /integrations. Instance policy is not workspace usage. */}
        <Link href="/integrations?tab=notifications&section=connections" className="ml-auto inline-flex items-center gap-1 font-medium text-primary-hover hover:underline">
          Channels live in Integrations
          <ChevronRight className="size-3" />
        </Link>
      </SettingsSummary>

      {error ? (
        <div className="rounded-card border border-border bg-card px-4 py-6 text-center text-[11px] text-muted-foreground">
          Failed to load providers ({error})
        </div>
      ) : (
        groups.map((g) => {
          const folded = !expanded[g.key] && g.items.length > FOLD_AT + 1
          const items = folded ? g.items.slice(0, FOLD_AT) : g.items
          return (
            <SettingsCard key={g.key} icon={CATEGORY_ICON[g.key] ?? Bell} tint={CATEGORY_TINT[g.key] ?? "var(--purple)"}
              title={g.label} description={g.hint || undefined}
              actions={<span className="font-mono text-[11px] text-muted-foreground">{g.items.filter((p) => p.enabled).length}/{g.items.length} allowed</span>}>
              {items.map((p) => {
                const n = usage[p.provider] ?? 0
                const name = p.label ?? p.provider
                return (
                  <div key={p.provider} data-provider={p.provider} className="border-b border-border last:border-b-0">
                    <div className="flex items-center gap-3 px-4 py-2.5">
                      {/* The provider's own mark — the same component Integrations
                          draws one click away. */}
                      <ProviderMark provider={p.provider} label={name} className={cn("h-7 w-7", !p.enabled && "opacity-60")} />
                      <span className={cn("min-w-0 flex-1", !p.enabled && "opacity-60")}>
                        <span className="block text-[13px]">{name}</span>
                        {p.blurb && <span className="block truncate text-[11px] text-muted-foreground-soft" title={p.blurb}>{p.blurb}</span>}
                      </span>
                      <span className="flex shrink-0 items-center justify-end gap-2 sm:w-64">
                        {n > 0 && <span className="rounded-full bg-primary/10 px-1.5 font-mono text-[10.5px] text-primary-hover">{n} channel{n === 1 ? "" : "s"}</span>}
                        {!p.enabled && <StatusPill tone="muted" label="Off" />}
                        <Switch
                          checked={p.enabled}
                          disabled={togglingProvider === p.provider}
                          onCheckedChange={(next) => requestToggle(p.provider, next === true)}
                          aria-label={`${p.enabled ? "Disable" : "Enable"} ${p.provider}`}
                        />
                      </span>
                    </div>
                    {pending === p.provider && (
                      <div role="alert" className="flex flex-wrap items-center gap-2 px-4 pb-3 text-[11.5px]">
                        <span className="flex-1 text-warn">
                          {n} channel{n === 1 ? "" : "s"} stop delivering if you switch {name} off. Nothing more leaves through it.
                        </span>
                        <Button size="xs" variant="ghost" onClick={() => setPending(null)}>Keep on</Button>
                        <Button size="xs" variant="destructive" onClick={() => void handleToggle(p.provider, false)}>Switch off</Button>
                      </div>
                    )}
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

      <SettingsCard icon={Mail} tint="var(--info)" title="Built-in transports" description="Always available; configured on the server, not switched here">
        <SettingsRow label="Email" description="Resend · set RESEND_API_KEY and RESEND_FROM">
          <span className="inline-flex items-center gap-1.5 text-[11px] text-muted-foreground">
            <StatusDot status={emailConfigured ? "COMPLETED" : emailConfigured === false ? "BLOCKED" : "PENDING"} />
            {emailConfigured === null ? "Unknown" : emailConfigured ? "Ready" : "Not set up"}
            {channels !== null && (usage.email ?? 0) > 0 && ` · ${usage.email} channels`}
          </span>
        </SettingsRow>
        <SettingsRow label="Signed webhooks" description="HTTPS POST with an HMAC signature header">
          <span className="inline-flex items-center gap-1.5 text-[11px] text-muted-foreground">
            <StatusDot status="COMPLETED" />Ready{channels !== null && (usage.webhook ?? 0) > 0 && ` · ${usage.webhook} channels`}
          </span>
        </SettingsRow>
      </SettingsCard>
    </div>
  )
}
