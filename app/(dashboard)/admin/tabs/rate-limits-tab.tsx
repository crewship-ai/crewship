"use client"

import { useCallback, useEffect, useMemo, useState } from "react"
import { Bell, Boxes, FileText, Gauge, Globe, KeyRound, RotateCcw, Shield, Webhook, type LucideIcon } from "lucide-react"
import { toast } from "sonner"
import { Button } from "@/components/ui/button"
import { Skeleton } from "@/components/ui/skeleton"
import { SettingsCard, SettingsEmpty, SettingsSaveBar, SettingsSegmented, SettingsSummary, SummaryItem, firstSentence } from "@/components/features/settings/shared"
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
    return <Skeleton className="h-[240px] rounded-card" />
  }

  if (error) {
    return (
      <SettingsCard icon={Gauge} tint="var(--purple)" title="Limits" description="Rate limits for the whole instance">
        <SettingsEmpty>Failed to load rate limiters ({error})</SettingsEmpty>
      </SettingsCard>
    )
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
      <SettingsSummary>
        <SummaryItem n={limiters.length}>limits in {groups.length} areas</SummaryItem>
        {changedCount
          ? <SummaryItem n={changedCount} tone="warn">changed from default</SummaryItem>
          : <SummaryItem>All at defaults</SummaryItem>}
        <SummaryItem>Instance-wide · no restart</SummaryItem>
        <span className="ml-auto">
          <SettingsSegmented label="Show" value={onlyChanged ? "changed" : "all"} onChange={(v) => setOnlyChanged(v === "changed")}
            options={[{ value: "all", label: "All" }, { value: "changed", label: "Changed" }]} />
        </span>
      </SettingsSummary>

      {visible.map(([group, rows]) => {
        const meta = GROUP_META[group] ?? { icon: Gauge, tint: "var(--purple)", about: "" }
        const groupChanged = rows.filter((l) => l.overridden).length
        return (
          <SettingsCard
            key={group}
            icon={meta.icon}
            tint={meta.tint}
            title={group}
            description={meta.about || undefined}
            actions={<span className="font-mono text-[11px] text-muted-foreground">{groupChanged ? `${groupChanged} changed` : "defaults"}</span>}
          >
            {rows.map((l) => {
              const draft = drafts[l.key] ?? String(l.value)
              const trimmed = draft.trim()
              const inRange = validDraft(l, trimmed)
              const dirty = trimmed !== String(l.value)
              const direction = l.value > l.default ? "looser" : l.value < l.default ? "tighter" : null
              const inputId = `ratelimit-${l.key}`
              return (
                <div
                  key={l.key}
                  data-limiter={l.key}
                  className={cn(
                    "flex flex-wrap items-center gap-x-4 gap-y-2 border-b border-border px-4 py-2.5 last:border-b-0 sm:flex-nowrap",
                    dirty ? "bg-primary/[0.05]" : l.overridden && "bg-warn/[0.04]",
                  )}
                >
                  <div className="min-w-0 flex-1">
                    <div className="flex flex-wrap items-center gap-2">
                      <label htmlFor={inputId} className="text-[13px] text-foreground">{l.display_name}</label>
                      {l.overridden && direction && (
                        <span className="rounded-full bg-warn/15 px-1.5 font-mono text-[10px] text-warn">{direction} · default {l.default}</span>
                      )}
                    </div>
                    {inRange ? (
                      <p className="mt-0.5 truncate text-[11px] text-muted-foreground-soft" title={l.description}>{firstSentence(l.description)}</p>
                    ) : (
                      <p className="mt-0.5 text-[11px] text-destructive">Must be between {l.min} and {l.max}</p>
                    )}
                  </div>
                  <div className="flex w-full shrink-0 items-center justify-end gap-2 sm:w-64">
                    <div className={cn(
                      "flex h-8 w-44 items-center overflow-hidden rounded-md border border-control-border bg-surface-subtle focus-within:border-ring",
                      !inRange && "border-destructive",
                    )}>
                      <input
                        id={inputId}
                        type="text"
                        inputMode="numeric"
                        value={draft}
                        aria-invalid={!inRange}
                        aria-label={`${l.display_name} value`}
                        disabled={saving || busyKey === l.key}
                        onChange={(e) => setDrafts((prev) => ({ ...prev, [l.key]: e.target.value }))}
                        className="h-full w-20 min-w-0 bg-transparent px-2 text-right font-mono text-control tabular-nums outline-none"
                      />
                      <span className="flex h-full flex-1 items-center truncate border-l border-border bg-muted px-2 text-[11px] text-muted-foreground" title={l.unit}>{l.unit}</span>
                    </div>
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

      {groups.length === 0 ? (
        <SettingsCard icon={Gauge} tint="var(--purple)" title="Limits" description="Rate limits for the whole instance">
          <SettingsEmpty>No rate limiters configured.</SettingsEmpty>
        </SettingsCard>
      ) : visible.length === 0 ? (
        <div className="rounded-card border border-border bg-card px-4 py-6 text-center text-xs text-muted-foreground">Every limit is at its default.</div>
      ) : null}

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
