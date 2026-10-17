"use client"

import { useState } from "react"
import Link from "next/link"
import { useRouter } from "next/navigation"
import { AlertTriangle, Play, X as XIcon } from "lucide-react"
import { toast } from "@/lib/toast"
import { apiFetch } from "@/lib/api-fetch"
import type { Pipeline } from "@/hooks/use-pipelines"
import { Peek } from "@/components/ui/peek"
import { Button } from "@/components/ui/button"
import { ConfirmDialog } from "@/components/ui/confirm-dialog"
import { RoutineRunInputsDialog } from "@/components/features/routines/routine-run-inputs-dialog"
import { routineInputSpecs, type RoutineInputSpec } from "@/lib/routine-inputs"
import { routineTimeZone } from "@/lib/routine-time"
import { runDisabledReason } from "@/lib/routine-governance"
import { CALENDAR_LAYER_LABELS, dayLabel, routineHref, runHref, type CalendarItem } from "@/lib/calendar/model"

export interface CalendarPermissions {
  /** May start a routine (Run now, a one-time start). */
  run: boolean
  /** MANAGER+: cancel a one-time start, decide on a waiting run. */
  author: boolean
}

const clockIn = (d: Date, timeZone: string) =>
  d.toLocaleTimeString("en-GB", { hour: "2-digit", minute: "2-digit", timeZone })

/** Where the date lives, in words — never a table or column name. */
export function comesFrom(item: CalendarItem): string {
  if (item.kind === "planned") return `Repeating schedule of ${item.title}${item.timezone ? ` · ${item.timezone}` : ""}`
  if (item.kind === "pending") return `One-time start of ${item.title}`
  return `A run of ${item.title}`
}

const keepsNoCopy = (item: CalendarItem) =>
  item.kind === "planned"
    ? "This date lives on the routine's schedule. The calendar keeps no copy."
    : item.kind === "pending"
      ? "This date lives on the one-time start. The calendar keeps no copy."
      : "This is a recorded run. The calendar keeps no copy."

/**
 * The calendar item peek (CAL-PEEK): what it is and when, why the viewer sees
 * it, where it comes from, and the one action that fits — Open routine,
 * Run now, Cancel start, Open run or Decide. J / K walk the visible items.
 */
export function CalendarPeek({
  item,
  items,
  workspaceId,
  routines,
  permissions,
  onSelect,
  onChanged,
}: {
  item: CalendarItem | null
  /** The visible items, in time order, for J / K. */
  items: CalendarItem[]
  workspaceId: string
  routines: Pipeline[]
  permissions: CalendarPermissions
  onSelect: (id: string | null) => void
  onChanged: () => void
}) {
  const router = useRouter()
  const base = `/api/v1/workspaces/${encodeURIComponent(workspaceId)}`
  const [confirmCancel, setConfirmCancel] = useState(false)
  const [run, setRun] = useState<{ specs: RoutineInputSpec[] | null; definition?: Record<string, unknown> | null; error?: string | null; busy?: boolean } | null>(null)

  const idx = item ? items.findIndex((i) => i.id === item.id) : -1
  const neighbour = (d: number) => (idx >= 0 && items.length > 1 ? () => onSelect(items[(idx + d + items.length) % items.length].id) : undefined)
  const routine = item ? routines.find((r) => r.slug === item.slug) : undefined
  const localZone = routineTimeZone()

  const openRun = async () => {
    if (!item) return
    setRun({ specs: null })
    try {
      const res = await apiFetch(`${base}/pipelines/${encodeURIComponent(item.slug)}`)
      if (!res.ok) throw new Error("Could not load this routine's inputs.")
      const recipe = await res.json()
      setRun({ specs: routineInputSpecs(recipe.definition), definition: recipe.definition })
    } catch (e) {
      setRun(null)
      toast.error(e instanceof Error ? e.message : "Could not load this routine's inputs.")
    }
  }
  const startRun = async (inputs: Record<string, unknown>) => {
    if (!item) return
    setRun((r) => r && { ...r, busy: true, error: null })
    try {
      const res = await apiFetch(`${base}/pipelines/${encodeURIComponent(item.slug)}/run`, {
        method: "POST",
        headers: { "Content-Type": "application/json", Prefer: "respond-async", "Idempotency-Key": crypto.randomUUID() },
        body: JSON.stringify({ inputs }),
      })
      const data = await res.json().catch(() => ({}))
      if (!res.ok || typeof data.run_id !== "string") throw new Error(data.error || data.detail || "Could not start the routine.")
      setRun(null)
      onSelect(null)
      toast.success(`${item.title} started`)
      router.push(runHref(item.slug, data.run_id))
    } catch (e) {
      setRun((r) => r && { ...r, busy: false, error: e instanceof Error ? e.message : String(e) })
    }
  }
  const cancelStart = async () => {
    if (!item) return
    const res = await apiFetch(`${base}/pipelines/pending/${encodeURIComponent(item.entry.id)}/cancel`, { method: "POST" })
    if (!res.ok) {
      toast.error("Could not cancel; the run may already have started.")
      return
    }
    toast.success("Start cancelled · the calendar follows")
    onSelect(null)
    onChanged()
  }

  let footer: React.ReactNode = null
  if (item) {
    const runBlocked = runDisabledReason(routine?.status)
    const primary =
      item.kind === "run" ? (
        <Button asChild size="sm">
          <Link href={runHref(item.slug, item.entry.id)}>{item.status === "wait" && permissions.author ? "Decide" : "Open run"}</Link>
        </Button>
      ) : (
        <Button asChild size="sm">
          <Link href={routineHref(item.slug)}>Open routine</Link>
        </Button>
      )
    footer = (
      <div className="flex flex-wrap items-center gap-2">
        {primary}
        {item.kind === "planned" && permissions.run && (
          <Button size="sm" variant="outline" onClick={openRun} disabled={!!runBlocked} title={runBlocked ?? undefined}>
            <Play className="size-3.5" aria-hidden />
            Run now
          </Button>
        )}
        {item.kind === "pending" && permissions.author && (
          <Button size="sm" variant="outline" onClick={() => setConfirmCancel(true)}>
            <XIcon className="size-3.5" aria-hidden />
            Cancel start
          </Button>
        )}
      </div>
    )
  }

  const when = item
    ? `${dayLabel(item.day)} · ${item.time ?? "all day"} · ${localZone}`
    : ""
  const otherZone = item?.timezone && item.timezone !== localZone && item.time ? `${clockIn(item.at, item.timezone)} ${item.timezone} · ${item.time} your time` : null

  return (
    <>
      <Peek
        open={!!item}
        onOpenChange={(open) => !open && onSelect(null)}
        title={item?.title ?? ""}
        subtitle={item ? `${CALENDAR_LAYER_LABELS[item.layer]} · ${when}` : undefined}
        onPrev={neighbour(-1)}
        onNext={neighbour(1)}
        footer={footer}
      >
        {item && (
          <div className="space-y-3 text-body" data-testid="calendar-peek">
            <p className="text-muted-foreground">{item.subtitle}</p>
            {otherZone && <p className="font-mono text-label text-muted-foreground">{otherZone}</p>}
            {item.status === "bad" && (
              <div className="flex items-start gap-2 rounded-md border border-destructive/30 bg-chip-danger-bg px-3 py-2 text-chip-danger-fg">
                <AlertTriangle className="mt-0.5 size-3.5 shrink-0" aria-hidden />
                <span>
                  <b className="block">At risk</b>
                  This run could not finish. Open it to see why.
                </span>
              </div>
            )}
            <dl className="grid grid-cols-[minmax(0,8rem)_minmax(0,1fr)] gap-x-3 gap-y-2">
              <dt className="text-muted-foreground">Why you see it</dt>
              <dd>{item.mine ? "You published this routine" : "Workspace view"}</dd>
              <dt className="text-muted-foreground">Comes from</dt>
              <dd>{comesFrom(item)}</dd>
            </dl>
            <p className="text-label text-muted-foreground">{keepsNoCopy(item)}</p>
          </div>
        )}
      </Peek>
      {item && (
        <ConfirmDialog
          open={confirmCancel}
          onOpenChange={setConfirmCancel}
          title="Cancel this start?"
          description={`${item.title} will not start ${dayLabel(item.day).toLowerCase() === "today" ? "today" : `on ${dayLabel(item.day)}`} at ${item.time}.`}
          consequences={[
            { tone: "lost", text: "The one-time start is removed from the calendar and the routine's Plan." },
            { tone: "kept", text: "The routine, its schedules and its runs stay as they are." },
          ]}
          confirmLabel="Cancel start"
          cancelLabel="Keep it"
          destructive
          onConfirm={cancelStart}
        />
      )}
      {item && run && run.specs !== null && (
        <RoutineRunInputsDialog
          inputs={run.specs}
          definition={run.definition}
          routineName={item.title}
          headVersion={routine?.head_version}
          submitting={run.busy}
          error={run.error}
          onCancel={() => setRun(null)}
          onRun={startRun}
        />
      )}
    </>
  )
}
