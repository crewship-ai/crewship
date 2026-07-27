"use client"

import { useCallback, useEffect, useMemo, useState } from "react"
import { RotateCcw } from "lucide-react"
import { toast } from "sonner"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Badge } from "@/components/ui/badge"
import { Skeleton } from "@/components/ui/skeleton"
import { SettingsCard } from "@/components/features/settings/shared"
import { apiFetch } from "@/lib/api-fetch"

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
      <SettingsCard
        title="Rate limiters"
        description="Tune every configurable rate limit for this instance"
      >
        <div className="px-4 py-6 text-center text-[11px] text-muted-foreground">
          Failed to load rate limiters ({error})
        </div>
      </SettingsCard>
    )
  }

  return (
    <div className="space-y-4">
      {groups.map(([group, rows]) => (
        <SettingsCard
          key={group}
          title={group}
          description="Tune the request-rate ceilings — Save applies an override, Reset restores the default"
        >
          {rows.map((l, i) => {
            const draft = drafts[l.key] ?? String(l.value)
            const trimmedDraft = draft.trim()
            const parsed = Number(trimmedDraft)
            const inRange = trimmedDraft !== "" && Number.isInteger(parsed) && parsed >= l.min && parsed <= l.max
            const changed = String(l.value) !== trimmedDraft
            const busy = busyKey === l.key
            const inputId = `ratelimit-${l.key}`
            return (
              <div
                key={l.key}
                className={
                  "flex flex-wrap items-center justify-between gap-x-4 gap-y-2 px-4 py-3" +
                  (i < rows.length - 1 ? " border-b border-border/40" : "")
                }
              >
                <div className="min-w-0 flex-1 basis-48">
                  <div className="flex items-center gap-2">
                    <label htmlFor={inputId} className="text-xs font-medium text-foreground">
                      {l.display_name}
                    </label>
                    {l.overridden ? (
                      <Badge variant="secondary" className="text-[10px] px-1.5 py-0">Overridden</Badge>
                    ) : (
                      <Badge variant="outline" className="text-[10px] px-1.5 py-0">Default</Badge>
                    )}
                  </div>
                  <p className="text-[11px] text-muted-foreground/80 mt-0.5 leading-snug" title={l.description}>
                    {l.description}
                  </p>
                </div>

                <div className="flex items-center gap-3 shrink-0">
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
                        className="h-8 w-24 text-right"
                      />
                      <span className="text-[11px] text-muted-foreground w-14 shrink-0">{l.unit}</span>
                    </div>
                    <span className="text-[10px] text-muted-foreground/70 mt-0.5">
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
      ))}

      {groups.length === 0 && (
        <SettingsCard title="Rate limiters" description="Tune every configurable rate limit for this instance">
          <div className="px-4 py-6 text-center text-[11px] text-muted-foreground">
            No rate limiters configured.
          </div>
        </SettingsCard>
      )}
    </div>
  )
}
