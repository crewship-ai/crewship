"use client"

import { useCallback, useEffect, useMemo, useState } from "react"
import { Bell, Boxes, FileText, Gauge, Globe, KeyRound, Pencil, RotateCcw, Search, Shield, Webhook, Zap, type LucideIcon } from "lucide-react"
import { toast } from "sonner"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Skeleton } from "@/components/ui/skeleton"
import { SettingsCard } from "@/components/features/settings/shared"
import { apiFetch } from "@/lib/api-fetch"
import { cn } from "@/lib/utils"
import { FilterChip, Kpi } from "./admin-kit"

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
 * Admin → Rate Limiters: view + tune every configurable rate limit for the
 * instance. Overrides are INSTANCE-GLOBAL — they apply to the whole daemon,
 * not just the current workspace. Save PUTs the new value (validated
 * client-side against [min,max]), Reset DELETEs the override so the limiter
 * falls back to its compiled-in default.
 */
export function RateLimitsTab({ workspaceId }: { workspaceId: string | null }) {
  const [limiters, setLimiters] = useState<Limiter[]>([])
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)
  // Per-key in-flight guard so a row's buttons disable while its PUT/DELETE runs.
  const [busyKey, setBusyKey] = useState<string | null>(null)
  // Per-key draft values for the number inputs, keyed by limiter key.
  const [drafts, setDrafts] = useState<Record<string, string>>({})
  const [query, setQuery] = useState("")
  const [area, setArea] = useState<string>("all")
  const [onlyChanged, setOnlyChanged] = useState(false)

  const refresh = useCallback(async () => {
    if (!workspaceId) return
    setLoading(true)
    setError(null)
    try {
      const res = await apiFetch(`/api/v1/admin/rate-limits?workspace_id=${workspaceId}`)
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

  const handleSave = useCallback(async (limiter: Limiter, raw: string) => {
    if (!workspaceId) return
    // Number("") is 0, not NaN — guard the empty string explicitly so an
    // emptied field can never validate as 0 (harmless today since every
    // min is >= 1, but robust if a future limiter allows 0).
    const trimmed = raw.trim()
    const value = Number(trimmed)
    if (trimmed === "" || !Number.isInteger(value) || value < limiter.min || value > limiter.max) {
      toast.error(`${limiter.display_name}: must be between ${limiter.min} and ${limiter.max}`)
      return
    }
    setBusyKey(limiter.key)
    try {
      const res = await apiFetch(
        `/api/v1/admin/rate-limits/${encodeURIComponent(limiter.key)}?workspace_id=${workspaceId}`,
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
      const updated: Limiter = await res.json()
      applyUpdated(updated)
      toast.success(`${limiter.display_name} updated`)
    } catch (e) {
      toast.error(e instanceof Error ? e.message : "Failed to update limiter")
    } finally {
      setBusyKey(null)
    }
  }, [workspaceId, applyUpdated])

  const handleReset = useCallback(async (limiter: Limiter) => {
    if (!workspaceId) return
    setBusyKey(limiter.key)
    try {
      const res = await apiFetch(
        `/api/v1/admin/rate-limits/${encodeURIComponent(limiter.key)}?workspace_id=${workspaceId}`,
        { method: "DELETE" },
      )
      if (!res.ok) {
        const errBody = await res.json().catch(() => null)
        throw new Error(errBody?.error ?? errBody?.detail ?? `HTTP ${res.status}`)
      }
      const updated: Limiter = await res.json()
      applyUpdated(updated)
      toast.success(`${limiter.display_name} reset to default`)
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
    return <Skeleton className="h-[240px] rounded-xl" />
  }

  if (error) {
    return (
      <SettingsCard icon={Gauge} tint="var(--purple)"
        title="Rate limiters"
        description="Tune every configurable rate limit for this instance"
      >
        <div className="px-4 py-6 text-center text-[11px] text-muted-foreground">
          Failed to load rate limiters ({error})
        </div>
      </SettingsCard>
    )
  }

  const changedCount = limiters.filter((l) => l.overridden).length
  const q = query.trim().toLowerCase()
  const visible = groups
    .filter(([group]) => area === "all" || area === group)
    .map(([group, rows]) => [group, rows.filter((l) =>
      (!onlyChanged || l.overridden) &&
      (!q || `${l.display_name} ${l.description} ${l.key}`.toLowerCase().includes(q)))] as const)
    .filter(([, rows]) => rows.length > 0)

  return (
    <div className="space-y-4">
      <div className="grid grid-cols-2 gap-3 lg:grid-cols-4">
        <Kpi icon={Gauge} tint="var(--primary)" label="Limits" value={limiters.length} sub={`${groups.length} areas`} />
        <Kpi icon={Pencil} tint={changedCount ? "var(--warn)" : "var(--success)"} label="Changed" value={changedCount} sub={changedCount ? "Differ from the defaults" : "All at their defaults"} />
        <Kpi icon={Globe} tint="var(--purple)" label="Applies to" value="Instance" sub="Every workspace at once" />
        <Kpi icon={Zap} tint="var(--info)" label="Takes effect" value="Now" sub="No restart needed" />
      </div>

      <div className="flex flex-wrap items-center gap-2">
        <label className="flex h-8 min-w-[220px] items-center gap-2 rounded-md border border-border bg-card px-2.5 text-muted-foreground focus-within:border-primary/40">
          <Search className="h-3.5 w-3.5 shrink-0" />
          <input value={query} onChange={(e) => setQuery(e.target.value)} placeholder="Search limits" aria-label="Search limits"
            className="min-w-0 flex-1 bg-transparent text-xs text-foreground outline-none placeholder:text-muted-foreground-soft" />
        </label>
        <FilterChip pressed={onlyChanged} onClick={() => setOnlyChanged(!onlyChanged)} count={changedCount} dot="bg-warn">Changed only</FilterChip>
        <span className="mx-1 h-5 w-px bg-border" aria-hidden />
        <FilterChip pressed={area === "all"} onClick={() => setArea("all")}>All areas</FilterChip>
        {groups.map(([group, rows]) => (
          <FilterChip key={group} pressed={area === group} onClick={() => setArea(group)} count={rows.length}>{group}</FilterChip>
        ))}
      </div>

      {visible.map(([group, rows]) => {
        const meta = GROUP_META[group] ?? { icon: Gauge, tint: "var(--purple)", about: "" }
        return (
        <SettingsCard
          key={group}
          icon={meta.icon}
          tint={meta.tint}
          title={group}
          description={meta.about || "Save applies an override, Reset restores the default"}
          actions={<span className="font-mono text-[11px] text-muted-foreground">{rows.filter((l) => l.overridden).length ? `${rows.filter((l) => l.overridden).length} changed` : "defaults"}</span>}
        >
          {rows.map((l) => {
            const draft = drafts[l.key] ?? String(l.value)
            const trimmedDraft = draft.trim()
            const parsed = Number(trimmedDraft)
            const inRange = trimmedDraft !== "" && Number.isInteger(parsed) && parsed >= l.min && parsed <= l.max
            const changed = String(l.value) !== trimmedDraft
            const busy = busyKey === l.key
            const inputId = `ratelimit-${l.key}`
            const direction = l.value > l.default ? "looser" : l.value < l.default ? "tighter" : null
            return (
              <div
                key={l.key}
                data-limiter={l.key}
                className={cn(
                  "flex flex-wrap items-center justify-between gap-x-4 gap-y-2 border-b border-border px-4 py-3 last:border-b-0",
                  l.overridden && "bg-warn/[0.04]",
                )}
              >
                <div className="min-w-0 flex-1 basis-56">
                  <div className="flex flex-wrap items-center gap-2">
                    <label htmlFor={inputId} className="text-xs font-medium text-foreground">
                      {l.display_name}
                    </label>
                    {l.overridden ? (
                      <span className="rounded-full bg-warn/15 px-1.5 font-mono text-[10px] font-semibold text-warn">Overridden</span>
                    ) : (
                      <span className="rounded-full bg-muted px-1.5 font-mono text-[10px] text-muted-foreground">Default</span>
                    )}
                    {direction && <span className="text-[10.5px] text-muted-foreground">{direction} than the default {l.default}</span>}
                  </div>
                  <p className="mt-0.5 line-clamp-2 text-[11.5px] leading-snug text-muted-foreground" title={l.description}>
                    {l.description}
                  </p>
                </div>

                <div className="flex shrink-0 items-center gap-2">
                  <div className="flex flex-col items-end">
                    <div className="flex items-center gap-1.5">
                      <Input
                        id={inputId}
                        type="number"
                        inputMode="numeric"
                        min={l.min}
                        max={l.max}
                        value={draft}
                        aria-invalid={!inRange}
                        aria-label={`${l.display_name} value`}
                        disabled={busy}
                        onChange={(e) => setDrafts((prev) => ({ ...prev, [l.key]: e.target.value }))}
                        className={cn("h-8 w-24 text-right font-mono tabular-nums", changed && inRange && "border-primary/50")}
                      />
                      <span className="w-20 shrink-0 truncate text-[11px] text-muted-foreground" title={l.unit}>{l.unit}</span>
                    </div>
                    <span className={cn("mt-0.5 text-[10px]", inRange ? "text-muted-foreground-soft" : "text-destructive")}>
                      {inRange
                        ? `default ${l.default} · range ${l.min}–${l.max}`
                        : `must be between ${l.min} and ${l.max}`}
                    </span>
                  </div>

                  <Button
                    size="xs"
                    variant="soft"
                    disabled={busy || !changed || !inRange}
                    onClick={() => handleSave(l, draft)}
                  >
                    Save
                  </Button>
                  <Button
                    size="xs"
                    variant="ghost"
                    disabled={busy || !l.overridden}
                    onClick={() => handleReset(l)}
                    aria-label={`Reset ${l.display_name} to default`}
                  >
                    <RotateCcw className="size-3" />
                    Reset
                  </Button>
                </div>
              </div>
            )
          })}
        </SettingsCard>
        )
      })}

      {groups.length === 0 ? (
        <SettingsCard icon={Gauge} tint="var(--purple)" title="Rate limiters" description="Tune every configurable rate limit for this instance">
          <div className="px-4 py-6 text-center text-[11px] text-muted-foreground">
            No rate limiters configured.
          </div>
        </SettingsCard>
      ) : visible.length === 0 ? (
        <div className="rounded-card border border-border bg-card px-4 py-6 text-center text-[12px] text-muted-foreground">No limit matches.</div>
      ) : null}
    </div>
  )
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
