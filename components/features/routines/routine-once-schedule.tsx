"use client"

import { useAbilities } from "@/hooks/use-abilities"
import { routinePermissions } from "@/lib/routine-governance"

import { formatRoutineTime } from "@/lib/routine-time"

import { routinePresetSummary } from "@/lib/routine-preset-summary"

import { useMemo, useState } from "react"
import Link from "next/link"
import { Plus } from "lucide-react"
import { apiFetch } from "@/lib/api-fetch"
import { Button } from "@/components/ui/button"
import { StatusPill } from "@/components/ui/status-pill"
import { usePendingStarts } from "@/hooks/use-pending-starts"
import { pendingStartPresentation, pendingStartStatus, type PendingStart } from "@/lib/routine-pending-starts"
import { Card } from "./_shared"
import { RoutineDateTimePicker } from "./routine-date-time-picker"
import { RoutineRunInputsDialog } from "./routine-run-inputs-dialog"
import { routineInputSpecs, type RoutineInputSpec } from "@/lib/routine-inputs"

// Accepted starts that already left the queue, shown under the planned ones
// so a start never vanishes without saying what became of it.
const SETTLED_LIMIT = 5
export function RoutineOnceSchedule({
  workspaceId,
  slug,
  headVersion,
  draft,
}: {
  workspaceId: string
  slug: string
  /** The published head; a new one-time start pins it. */
  headVersion?: number | null
  draft?: { revision: number } | null
}) {
  const { role, capabilities } = useAbilities()
  const permissions = routinePermissions(role, capabilities)
  const [at, setAt] = useState("")
  const { starts, error: loadError, refresh } = usePendingStarts(workspaceId)
  const [error, setError] = useState<string | null>(null)
  const [busy, setBusy] = useState(false)
  const [picking, setPicking] = useState(false)
  const [specs, setSpecs] = useState<RoutineInputSpec[] | null>(null)
  const base = `/api/v1/workspaces/${encodeURIComponent(workspaceId)}`
  const mine = useMemo(() => starts.filter((p) => p.pipeline_slug === slug), [starts, slug])
  const pending = useMemo(
    () => mine.filter((p) => pendingStartStatus(p) === "pending").sort((a, b) => Date.parse(a.fire_at) - Date.parse(b.fire_at)),
    [mine],
  )
  // The server lists non-pending receipts newest accepted first.
  const settled = useMemo(() => mine.filter((p) => pendingStartStatus(p) !== "pending").slice(0, SETTLED_LIMIT), [mine])
  // A server that predates receipts sends no can_cancel; it lists only
  // pending starts, which a manager may remove.
  const canRemove = (p: PendingStart) => (p.can_cancel ?? permissions.author)
  const schedule = async (inputs: Record<string, unknown>) => {
    setBusy(true)
    setError(null)
    try {
      const res = await apiFetch(`${base}/pipelines/${encodeURIComponent(slug)}/run`, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ fire_at: new Date(at).toISOString(), inputs }),
      })
      if (!res.ok) {
        const data = await res.json()
        throw new Error(data.error || "Could not schedule run")
      }
      setSpecs(null)
      setAt("")
      setPicking(false)
      await refresh()
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e))
    } finally {
      setBusy(false)
    }
  }
  const [definition, setDefinition] = useState<Record<string, unknown> | null>(null)
  const prepare = async () => {
    setBusy(true)
    setError(null)
    try {
      const res = await apiFetch(`${base}/pipelines/${encodeURIComponent(slug)}`)
      if (!res.ok) throw new Error("Could not load routine inputs")
      const routine = await res.json()
      const inputs = routineInputSpecs(routine.definition)
      setDefinition(routine.definition ?? null)
      setSpecs(inputs)
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e))
    } finally {
      setBusy(false)
    }
  }
  const cancel = async (id: string) => {
    setBusy(true)
    setError(null)
    try {
      const res = await apiFetch(
        `${base}/pipelines/pending/${encodeURIComponent(id)}/cancel`,
        {
          method: "POST",
        },
      )
      if (!res.ok)
        throw new Error(
          "Could not cancel; the run may already have started. Refresh its history.",
        )
      await refresh()
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e))
    } finally {
      setBusy(false)
    }
  }
  return (
    <Card
      title="One-time starts"
      subtitle={pending.length ? `${pending.length} planned` : undefined}
      action={
        !picking && (
          <Button
            size="sm"
            variant="outline"
            className="h-8 gap-1.5 text-xs"
            disabled={busy || !permissions.run}
            title={!permissions.run ? "Scheduling a start requires permission to run routines" : undefined}
            onClick={() => setPicking(true)}
          >
            <Plus className="h-3 w-3" />
            Schedule a start
          </Button>
        )
      }
    >
      <section aria-label="One-time starts" className="divide-y divide-border/40">
        {(error || loadError) && (
          <p role="alert" className="px-4 py-3 text-sm text-destructive">
            {error || loadError}
          </p>
        )}
        {pending.map((p) => (
          <div
            key={p.id}
            className="grid grid-cols-[auto_minmax(0,1fr)] items-start gap-3 px-4 py-3 md:grid-cols-[auto_minmax(0,1fr)_auto]"
          >
            <span aria-hidden className="mt-2 h-2 w-2 rounded-full bg-primary" />
            <div className="min-w-0 space-y-1">
              <div className="flex flex-wrap items-center gap-2">
                <span className="text-sm font-semibold">{formatRoutineTime(p.fire_at)}</span>
                <StatusPill tone={pendingStartPresentation(p).tone} label={pendingStartPresentation(p).label} />
              </div>
              {pendingStartStatus(p) === "pending" && (p.dispatch_attempts ?? 0) > 0 && (
                <p className="text-xs text-muted-foreground" data-testid={`pending-state-${p.id}`}>
                  {pendingStartPresentation(p).detail}
                </p>
              )}
              <p className="text-xs text-muted-foreground" data-testid={`pending-uses-${p.id}`}>
                {p.pinned_version ? (
                  <>
                    Pinned to{" "}
                    <span className="font-medium text-foreground/85">v{p.pinned_version}</span> —
                    publishing a newer version does not change this start
                  </>
                ) : (
                  <>
                    Uses the{" "}
                    <span className="font-medium text-foreground/85">version live at dispatch</span>{" "}
                    (an older plan)
                  </>
                )}
                {" · with: "}
                {routinePresetSummary(p.inputs).replace(/^Inputs: /, "")}
              </p>
            </div>
            <div className="col-start-2 flex items-center gap-1 md:col-start-3">
              <Button
                size="sm"
                variant="ghost"
                className="h-8 text-xs"
                disabled={busy || !canRemove(p)}
                title={!canRemove(p) ? "Removing a planned start requires a manager role" : undefined}
                onClick={() => cancel(p.id)}
              >
                Remove
              </Button>
            </div>
          </div>
        ))}
        {settled.length > 0 && (
          <div aria-label="Recent accepted starts" role="group" className="space-y-1 px-4 py-3">
            <p className="text-xs font-medium text-muted-foreground">Recent accepted starts</p>
            {settled.map((p) => {
              const view = pendingStartPresentation(p)
              return (
                <div key={p.id} data-testid={`settled-start-${p.id}`} className="flex flex-wrap items-center gap-x-2 gap-y-1 py-1 text-xs">
                  <span className="font-mono tabular-nums text-muted-foreground">{formatRoutineTime(p.fire_at)}</span>
                  <StatusPill tone={view.tone} label={view.label} />
                  <span className="min-w-0 flex-1 basis-48 text-muted-foreground">{view.detail}</span>
                  {p.run_id && (
                    <Link className="text-primary hover:underline" href={`/routines?${new URLSearchParams({ slug, run: p.run_id })}`}>
                      Open run
                    </Link>
                  )}
                </div>
              )
            })}
          </div>
        )}
        {!pending.length && !picking && (
          <p className="px-4 py-3 text-sm text-muted-foreground">
            No one-time start planned. Starts added from the Calendar appear here too.
          </p>
        )}
        {picking && (
          <div className="space-y-3 px-4 py-3">
            <div className="flex flex-wrap items-end gap-2">
              <RoutineDateTimePicker
                label="One-time date and time"
                value={at}
                onChange={setAt}
              />
              <Button
                disabled={busy || !at || !(Date.parse(at) > Date.now())}
                onClick={prepare}
              >
                Choose the answers
              </Button>
              <Button variant="ghost" disabled={busy} onClick={() => setPicking(false)}>
                Cancel
              </Button>
            </div>
            <p className="text-xs text-muted-foreground">
              {Intl.DateTimeFormat().resolvedOptions().timeZone} · the start is pinned to the
              published version at the moment you schedule it
              {headVersion ? ` (now v${headVersion})` : ""}
              {draft ? ` — the unpublished draft r${draft.revision} is not used` : ""}.
            </p>
          </div>
        )}
      </section>
      <RoutineRunInputsDialog
        definition={definition}
        submitLabel="Schedule"
        inputs={specs}
        routineName={slug}
        headVersion={headVersion}
        draft={draft}
        submitting={busy}
        onCancel={() => setSpecs(null)}
        onRun={schedule}
      />
    </Card>
  )
}
