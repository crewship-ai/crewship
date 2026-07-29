"use client"

import { useCallback, useEffect, useState } from "react"
import { RefreshCw, Save } from "lucide-react"
import { toast } from "sonner"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import { SettingsCard } from "@/components/features/settings/shared"
import { apiFetch } from "@/lib/api-fetch"
import { useAbilities } from "@/hooks/use-abilities"
import { cn } from "@/lib/utils"

// #1379 — the editable half. GET/PATCH /api/v1/admin/memory/config has existed
// since the memory-hardening series (Iter 6) specifically so retention could be
// changed without editing SQLite by hand, but nothing rendered it — so the
// hand-edit stayed the only route, which is what the endpoint was built to
// avoid.

/** Bounds mirror internal/api's validateMemoryConfig (1..MaxRetentionDays).
 *  Duplicated here only to give immediate feedback; the server stays the
 *  authority and its message is what gets surfaced on rejection. */
const MIN_DAYS = 1
const MAX_DAYS = 3650

interface MemoryConfig {
  workspace_id: string
  /** Resolved value: the stored setting when present, else the built-in
   *  default. `is_default` says which — and that decides whether editing is
   *  routine or is overriding somebody's deliberate policy. */
  versions_retention_days: number
  is_default: boolean
  raw_config?: string | null
}

export function MemoryConfigCard({ workspaceId }: {
  /** The workspace this card reads within. The admin API is workspace-scoped
 *  by middleware: an unscoped request is refused with 400 before the handler
 *  runs, which is what rendered these cards as "Could not load (HTTP 400)".
 *  Null while it resolves — asking anyway just produces that error. */
  workspaceId: string | null
}) {
  const { role } = useAbilities()
  const canEdit = role === "OWNER" || role === "ADMIN"

  const [config, setConfig] = useState<MemoryConfig | null>(null)
  const [days, setDays] = useState("")
  const [loading, setLoading] = useState(true)
  const [saving, setSaving] = useState(false)
  const [error, setError] = useState<string | null>(null)

  const load = useCallback(async () => {
    if (!workspaceId) return
    setLoading(true)
    setError(null)
    try {
      const res = await apiFetch(
        `/api/v1/admin/memory/config?workspace_id=${encodeURIComponent(workspaceId)}`,
      )
      if (!res.ok) {
        setError(
          res.status === 403
            ? "Requires an admin role in this workspace."
            : `Could not load the memory configuration (HTTP ${res.status}).`,
        )
        return
      }
      const body = (await res.json()) as MemoryConfig
      setConfig(body)
      setDays(String(body.versions_retention_days))
    } catch {
      setError("Network error loading the memory configuration.")
    } finally {
      setLoading(false)
    }
  }, [workspaceId])

  useEffect(() => { void load() }, [load])

  const parsed = Number(days)
  const valid = Number.isInteger(parsed) && parsed >= MIN_DAYS && parsed <= MAX_DAYS
  const dirty = config != null && (parsed !== config.versions_retention_days || config.is_default)

  const save = useCallback(async () => {
    if (!valid) return
    setSaving(true)
    try {
      // PATCH with only the one key: the server merges into the stored document
      // and preserves settings this UI doesn't model, so a newer knob can't be
      // clobbered by an older client saving a whole document back.
      const res = await apiFetch(
        `/api/v1/admin/memory/config?workspace_id=${encodeURIComponent(workspaceId ?? "")}`, {
        method: "PATCH",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ versions_retention_days: parsed }),
      })
      if (!res.ok) {
        const body = (await res.json().catch(() => ({}))) as { error?: string }
        // Relay the server's message rather than a generic failure — it names
        // the actual bound that was violated.
        toast.error(body.error || `Could not save (HTTP ${res.status}).`)
        return
      }
      const body = (await res.json()) as MemoryConfig
      setConfig(body)
      setDays(String(body.versions_retention_days))
      toast.success(`Memory retention set to ${body.versions_retention_days} days`, {
        description: "The retention sweep uses the new window on its next pass.",
      })
    } catch {
      toast.error("Network error saving the memory configuration.")
    } finally {
      setSaving(false)
    }
  }, [parsed, valid])

  return (
    <SettingsCard
      title="Memory version history"
      description="How long this instance keeps the edit history of agent memory. It does not affect what an agent remembers."
      actions={
        <Button
          variant="outline"
          size="sm"
          className="h-7 px-2.5 text-xs"
          onClick={() => void load()}
          disabled={loading}
        >
          <RefreshCw className={cn("mr-1.5 h-3 w-3", loading && "animate-spin")} />
          Refresh
        </Button>
      }
      padded
    >
      {error ? (
        <p className="text-xs text-muted-foreground">{error}</p>
      ) : !config ? (
        <p className="text-xs text-muted-foreground">Loading…</p>
      ) : (
        <div className="space-y-3">
          <div className="flex items-end gap-2 flex-wrap">
            <div className="space-y-1">
              <Label htmlFor="mem-retention-days" className="text-xs">
                Keep history for (days)
              </Label>
              <Input
                id="mem-retention-days"
                type="number"
                min={MIN_DAYS}
                max={MAX_DAYS}
                value={days}
                disabled={!canEdit}
                onChange={(e) => setDays(e.target.value)}
                className={cn("h-8 w-32 font-mono text-xs", !valid && "border-destructive")}
              />
            </div>
            {canEdit && (
              <Button size="sm" className="h-8 text-xs" onClick={() => void save()} disabled={!valid || !dirty || saving}>
                <Save className="mr-1.5 h-3 w-3" />
                {saving ? "Saving…" : "Save"}
              </Button>
            )}
          </div>

          {!valid && (
            <p className="text-[11px] text-destructive">
              Must be a whole number between {MIN_DAYS} and {MAX_DAYS} (10 years).
            </p>
          )}

          {/* Whether the current value was chosen matters before changing it:
              overriding a default is routine, overriding a deliberate policy
              is not. */}
          <p className="text-[11px] text-muted-foreground">
            {config.is_default
              ? `Currently the built-in default (${config.versions_retention_days} days) — not set for this workspace. Saving makes it explicit.`
              : `Set explicitly for this workspace.`}
          </p>

          {/* The name "memory" made this read as "how long agents remember".
              It is not: what an agent remembers lives in its own files and is
              never touched here. This trims the VERSION TRAIL of those files —
              the record of what each write changed, which is what makes a
              memory edit recoverable and auditable. */}
          <p className="text-[11px] leading-snug text-muted-foreground">
            This is housekeeping for the instance, not a memory policy. An agent&apos;s memory lives in
            its own files and is kept for as long as the agent exists; what expires here is the trail
            of past versions of those files — what you would use to see what a write changed, or to
            roll one back.
          </p>
          <p className="text-[11px] leading-snug text-muted-foreground">
            The trim runs shortly after this instance starts and then daily at 03:00 UTC. The three
            most recent versions of every file always survive, whatever the window says. Changes to
            this setting are journalled as <code className="font-mono">memory.config_updated</code>,
            so an audit can trace when the policy changed and who changed it.
          </p>

          {!canEdit && (
            <p className="text-[11px] text-muted-foreground">Requires an admin to change.</p>
          )}
        </div>
      )}
    </SettingsCard>
  )
}
