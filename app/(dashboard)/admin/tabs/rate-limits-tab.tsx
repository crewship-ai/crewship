"use client"

import { useCallback, useEffect, useMemo, useState } from "react"
import { Bell, Boxes, FileText, Gauge, Globe, KeyRound, RotateCcw, Shield, Webhook, type LucideIcon } from "lucide-react"
import { toast } from "@/lib/toast"
import { Button } from "@/components/ui/button"
import { PageEmpty, PageError, PageLoading } from "@/components/layout/page-state"
import { SettingsCard, SettingsSaveBar, SettingsSegmented, firstSentence } from "@/components/features/settings/shared"
import { apiFetch } from "@/lib/api-fetch"
import { withWs } from "@/lib/admin-workspace-query"
import { cn } from "@/lib/utils"

/** A single tunable rate limiter — GET /api/v1/admin/rate-limits. */
interface Limiter {
  key: string
  group: string
  display_name: string
  description: string
  unit: string
  default: number
  value: number
  min: number
  max: number
  overridden: boolean
}

/**
 * Admin → Limits: view + tune every configurable rate limit for the
 * instance. Overrides are INSTANCE-GLOBAL — they apply to the whole daemon,
 * not just the current workspace. Edits collect in one save bar that PUTs
 * each changed value (validated client-side against [min,max]); Reset
 * DELETEs an override so the limiter falls back to its compiled-in default.
 */
export function RateLimitsTab({ workspaceId }: { workspaceId: string | null }) {
  const [limiters, setLimiters] = useState<Limiter[]>([])
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)
  // Per-key in-flight guard so a row's buttons disable while its PUT/DELETE runs.
  const [busyKey, setBusyKey] = useState<string | null>(null)
  // Per-key draft values for the number inputs, keyed by limiter key.
  const [drafts, setDrafts] = useState<Record<string, string>>({})
  const [saving, setSaving] = useState(false)
  const [onlyChanged, setOnlyChanged] = useState(false)

  const refresh = useCallback(async () => {
    setLoading(true)
    setError(null)
    try {
      const res = await apiFetch(withWs("/api/v1/admin/rate-limits", workspaceId))
      if (!res.ok) {
        setError(`HTTP ${res.status}`)
        return
      }
      const data = await res.json()
      const list: Limiter[] = Array.isArray(data?.limiters) ? data.limiters : []
      setLimiters(list)
      setDrafts(Object.fromEntries(list.map((l) => [l.key, String(l.value)])))
    } catch (e) {
      setError(e instanceof Error ? e.message : "Network error")
    } finally {
      setLoading(false)
    }
  }, [workspaceId])

  useEffect(() => { refresh() }, [refresh])

  // Merge an updated limiter (returned by PUT/DELETE) back into local state so
  // the row reflects the new value + overridden flag without a full refetch.
  const applyUpdated = useCallback((updated: Limiter) => {
    setLimiters((prev) => prev.map((l) => (l.key === updated.key ? updated : l)))
    setDrafts((prev) => ({ ...prev, [updated.key]: String(updated.value) }))
  }, [])

  // One PUT per changed limiter. Each result is merged as it lands, so a
  // failure leaves only its own edit pending.
  const handleSave = useCallback(async (edits: { limiter: Limiter; value: number }[]) => {
    setSaving(true)
    let saved = 0
    for (const { limiter, value } of edits) {
      try {
        const res = await apiFetch(
          withWs(`/api/v1/admin/rate-limits/${encodeURIComponent(limiter.key)}`, workspaceId),
          {
            method: "PUT",
            headers: { "Content-Type": "application/json" },
            body: JSON.stringify({ value }),
          },
        )
        if (!res.ok) {
          const errBody = await res.json().catch(() => null)
          throw new Error(errBody?.error ?? errBody?.detail ?? `HTTP ${res.status}`)
        }
        applyUpdated(await res.json())
        saved++
      } catch (e) {
        toast.error(`${limiter.display_name}: ${e instanceof Error ? e.message : "Failed to update"}`)
      }
    }
    setSaving(false)
    if (saved) toast.success(saved === 1 ? `${edits[0].limiter.display_name} updated` : `${saved} limits updated`)
  }, [workspaceId, applyUpdated])

  const handleReset = useCallback(async (limiter: Limiter) => {
    setBusyKey(limiter.key)
    try {
      const res = await apiFetch(
        withWs(`/api/v1/admin/rate-limits/${encodeURIComponent(limiter.key)}`, workspaceId),
        { method: "DELETE" },
      )
      if (!res.ok) {
        const errBody = await res.json().catch(() => null)
        throw new Error(errBody?.error ?? errBody?.detail ?? `HTTP ${res.status}`)
      }
      applyUpdated(await res.json())
      toast.success(`${limiter.display_name} reset to ${limiter.default}`)
    } catch (e) {
      toast.error(e instanceof Error ? e.message : "Failed to reset limiter")
    } finally {
      setBusyKey(null)
    }
  }, [workspaceId, applyUpdated])

  // Group limiters by their `group` label, preserving first-seen order.
  const groups = useMemo(() => {
    const byGroup = new Map<string, Limiter[]>()
    for (const l of limiters) {
      const arr = byGroup.get(l.group) ?? []
      arr.push(l)
      byGroup.set(l.group, arr)
    }
    return Array.from(byGroup.entries())
  }, [limiters])

  if (loading && limiters.length === 0) {
    return <PageLoading rows={6} className="px-0 py-0" />
  }

  if (error) {
    return (
      <PageError className="px-0 py-0" title="Couldn’t load the rate limits" detail={`${error} from GET /api/v1/admin/rate-limits`}
        description="Nothing was changed. The limits the server holds still apply." onRetry={() => void refresh()} />
    )
  }

  if (groups.length === 0) {
    return <PageEmpty icon={Gauge} title="No rate limits on this server" description="This build exposes no tunable limiters." cli="crewship admin ratelimits list" className="px-0 py-0" />
  }

  const changedCount = limiters.filter((l) => l.overridden).length
  const edits = limiters.flatMap((l) => {
    const raw = (drafts[l.key] ?? String(l.value)).trim()
    return raw === String(l.value) ? [] : [{ limiter: l, raw, value: Number(raw), valid: validDraft(l, raw) }]
  })
  const visible = groups
    .map(([group, rows]) => [group, rows.filter((l) => !onlyChanged || l.overridden)] as const)
    .filter(([, rows]) => rows.length > 0)

  return (
    <div className="space-y-4">
      <div data-slot="limits-toolbar" className="flex flex-wrap items-center gap-2.5">
        <span className="text-label text-muted-foreground">Instance-wide · applies without a restart</span>
        <span className="flex-1" />
        <span className="text-label text-muted-foreground">Show</span>
        <SettingsSegmented label="Show" value={onlyChanged ? "changed" : "all"} onChange={(v) => setOnlyChanged(v === "changed")}
          options={[{ value: "all", label: "All" }, { value: "changed", label: `Changed ${changedCount}` }]} />
      </div>

      {visible.map(([group, rows]) => {
        const meta = GROUP_META[group] ?? { icon: Gauge, tint: "var(--purple)", about: "" }
        return (
          <SettingsCard key={group} icon={meta.icon} tint={meta.tint} title={group} description={meta.about || undefined}>
            {rows.map((l) => {
              const draft = drafts[l.key] ?? String(l.value)
              const trimmed = draft.trim()
              const inRange = validDraft(l, trimmed)
              const dirty = trimmed !== String(l.value)
              const inputId = `ratelimit-${l.key}`
              return (
                <div
                  key={l.key}
                  data-limiter={l.key}
                  data-changed={l.overridden || undefined}
                  className={cn(
                    "flex flex-wrap items-center gap-x-4 gap-y-2 border-b border-border px-4 py-3 last:border-b-0",
                    dirty && "bg-primary/[0.05]",
                  )}
                >
                  <div className="min-w-0 flex-[1_1_11rem]">
                    <label htmlFor={inputId} className="text-control font-medium text-foreground">{l.display_name}</label>
                    {!inRange ? (
                      <p className="mt-0.5 text-label text-destructive">Must be between {l.min} and {l.max}</p>
                    ) : l.overridden ? (
                      <p className="mt-0.5 text-label text-warn">Changed · default {l.default.toLocaleString("en")}</p>
                    ) : (
                      <p className="mt-0.5 truncate text-label text-muted-foreground" title={l.description}>{firstSentence(l.description)}</p>
                    )}
                  </div>
                  {/* The figure in mono, the unit beside it, and Reset as an
                      icon only where the value differs from the default. */}
                  <div className="ml-auto flex shrink-0 items-center gap-2">
                    <input
                      id={inputId}
                      type="text"
                      inputMode="numeric"
                      value={draft}
                      aria-invalid={!inRange}
                      aria-label={`${l.display_name} value`}
                      disabled={saving || busyKey === l.key}
                      onChange={(e) => setDrafts((prev) => ({ ...prev, [l.key]: e.target.value }))}
                      className={cn(
                        "h-[var(--control-md)] w-24 rounded-md border border-control-border bg-surface-subtle px-2.5 text-right font-mono text-control tabular-nums outline-none hover:border-line-strong focus-visible:border-ring focus-visible:ring-[3px] focus-visible:ring-ring/50 coarse:h-[2.75rem]",
                        !inRange && "border-destructive",
                      )}
                    />
                    <span className="w-24 truncate text-label text-muted-foreground" title={l.unit}>{l.unit}</span>
                    {l.overridden ? (
                      <Button size="icon-sm" variant="ghost" className="h-7 w-7" disabled={saving || busyKey === l.key}
                        onClick={() => handleReset(l)} aria-label={`Reset ${l.display_name} to ${l.default}`} title={`Reset to ${l.default}`}>
                        <RotateCcw className="size-3.5" />
                      </Button>
                    ) : <span className="w-7" aria-hidden />}
                  </div>
                </div>
              )
            })}
          </SettingsCard>
        )
      })}

      {visible.length === 0 && (
        <div className="rounded-card border border-border bg-card px-4 py-6 text-center text-label text-muted-foreground">Every limit is at its default.</div>
      )}

      <SettingsSaveBar
        label="Limits"
        count={edits.length}
        saving={saving}
        canSave={edits.every((e) => e.valid)}
        onDiscard={() => setDrafts(Object.fromEntries(limiters.map((l) => [l.key, String(l.value)])))}
        onSave={() => void handleSave(edits.map(({ limiter, value }) => ({ limiter, value })))}
      />
    </div>
  )
}

/** Number("") is 0, not NaN — an emptied field must never validate as 0. */
function validDraft(l: Limiter, raw: string): boolean {
  const v = Number(raw)
  return raw !== "" && Number.isInteger(v) && v >= l.min && v <= l.max
}

/** What each area protects, in one line, and its tile. Unknown groups fall
 *  back to the gauge. */
const GROUP_META: Record<string, { icon: LucideIcon; tint: string; about: string }> = {
  "HTTP (per-IP)": { icon: Globe, tint: "var(--primary)", about: "Requests per client address, before anything else runs" },
  Login: { icon: KeyRound, tint: "var(--destructive)", about: "When repeated failed sign-ins lock an account, and for how long" },
  Notifications: { icon: Bell, tint: "var(--purple)", about: "How many messages one recipient can get before throttling" },
  Provisioning: { icon: Boxes, tint: "var(--info)", about: "How many crew environments build at once" },
  Webhooks: { icon: Webhook, tint: "var(--success)", about: "How often an agent webhook may fire" },
  Keeper: { icon: Shield, tint: "var(--warn)", about: "How often the credential judge can be probed or re-run by hand" },
  Pages: { icon: FileText, tint: "var(--primary)", about: "Live panel pushes and public page views" },
}
