"use client"

import { useCallback, useEffect, useState } from "react"
import { RefreshCw } from "lucide-react"

import { apiFetch } from "@/lib/api-fetch"
import { Button } from "@/components/ui/button"
import { Skeleton } from "@/components/ui/skeleton"
import { SettingsCard, SettingsRow, SettingsEmpty } from "@/components/features/settings/shared"

/**
 * Which model judges what — and whether it can actually run.
 *
 * Replaces the old Settings → Auxiliary Models card, which was a
 * configuration echo: it printed llm.AuxiliaryModels and labelled every row
 * "explicit" regardless of whether that provider could be built. Three
 * things were wrong with it and all three are why this exists:
 *
 *  1. It lived in workspace Settings, next to per-workspace cards, while
 *     the config it showed is process-wide. Creating a workspace did not
 *     change it. It belongs in Admin, beside the keeper governance panel
 *     that actually edits a judge.
 *
 *  2. It listed a `keeper` slot that nothing in the codebase consumes,
 *     while the real credential-access judge — built from cfg.Keeper on a
 *     separate path — was absent. So the row an operator read as "the
 *     keeper" named a model the keeper never used.
 *
 *  3. It could not report a problem. The backend now says whether each
 *     judge's provider is buildable and why not, and this renders that.
 *
 * Read-only on purpose: the editable knob is the per-workspace governance
 * model in the panel above, which overrides these at request time. A second
 * editable surface for the same thing is how the first inconsistency
 * started.
 */

interface Subsystem {
  id: string
  label: string
  provider: string
  model: string
  timeout_ms?: number
  source: string
  healthy: boolean
  detail?: string
}

export function JudgeModelsCard({ workspaceId }: { workspaceId: string | null }) {
  const [rows, setRows] = useState<Subsystem[] | null>(null)
  const [error, setError] = useState<string | null>(null)

  const load = useCallback(async () => {
    if (!workspaceId) return
    try {
      const res = await apiFetch(`/api/v1/system/aux-status?workspace_id=${encodeURIComponent(workspaceId)}`)
      if (!res.ok) throw new Error(String(res.status))
      const data = await res.json()
      setRows(Array.isArray(data?.subsystems) ? data.subsystems : [])
      setError(null)
    } catch {
      // Not an empty list: "no judges configured" would read as a clean
      // system, which is the opposite of what a failed read means.
      setError("Couldn't load judge status.")
    }
  }, [workspaceId])

  useEffect(() => { load() }, [load])

  const unhealthy = (rows ?? []).filter((r) => !r.healthy).length

  return (
    <SettingsCard
      title="Judge models"
      description="Which model decides what, and whether it can run. Instance-wide; a workspace governance model overrides it per request."
      actions={
        <Button variant="ghost" size="sm" className="h-7 px-2.5 gap-1.5 text-xs" onClick={() => load()}>
          <RefreshCw className="size-3" />Refresh
        </Button>
      }
    >
      {error ? (
        <SettingsEmpty>
          <div className="space-y-2">
            <div className="text-destructive">{error}</div>
            <Button variant="outline" size="sm" className="h-7 px-2.5 text-xs" onClick={() => { setError(null); load() }}>
              Retry
            </Button>
          </div>
        </SettingsEmpty>
      ) : rows === null ? (
        <div className="px-4 py-3 space-y-2">
          <Skeleton className="h-7 w-full" />
          <Skeleton className="h-7 w-full" />
        </div>
      ) : rows.length === 0 ? (
        <SettingsEmpty>No judge models are wired into this build.</SettingsEmpty>
      ) : (
        <>
          {unhealthy > 0 && (
            <div role="status" className="px-4 py-2 text-[11px] text-destructive border-b border-border/40 bg-destructive/[0.05]">
              {unhealthy} of {rows.length} judges cannot run right now — evaluations that need them will fail closed.
            </div>
          )}
          {rows.map((r) => (
            <SettingsRow
              key={r.id}
              label={
                <span className="flex items-center gap-2">
                  <span
                    aria-hidden="true"
                    className={`size-1.5 rounded-full shrink-0 ${r.healthy ? "bg-success" : "bg-destructive"}`}
                  />
                  <span>{r.label}</span>
                </span>
              }
              description={
                r.detail ? (
                  // The reason, verbatim from the server. A red dot that does
                  // not say what is wrong cannot be acted on.
                  <span role="status" className="block max-w-[28rem] whitespace-normal break-words text-destructive/90">
                    Not running — {r.detail}
                  </span>
                ) : undefined
              }
            >
              <span className="text-[11px] text-muted-foreground font-mono tabular-nums text-right">
                {r.provider || "—"}
                {r.model ? ` / ${r.model}` : ""}
                {r.timeout_ms ? ` · ${Math.round(r.timeout_ms / 1000)}s` : ""}
              </span>
            </SettingsRow>
          ))}
        </>
      )}
    </SettingsCard>
  )
}
