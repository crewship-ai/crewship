"use client"

import { useEffect, useState } from "react"
import { Play } from "lucide-react"
import { cn } from "@/lib/utils"
import { apiFetch } from "@/lib/api-fetch"

import type { BottomPanelContext } from "./types"
import { EmptyState, formatRelative, statusColor } from "./shared"

// describeCron renders a 5-field cron as a human sentence ("Every day at
// 03:00") for the common shapes operators actually use; falls back to the
// raw expression for anything exotic so we never lie about the schedule.
const DOW = ["Sunday", "Monday", "Tuesday", "Wednesday", "Thursday", "Friday", "Saturday"]
function describeCron(expr: string): string {
  const p = expr.trim().split(/\s+/)
  if (p.length !== 5) return expr
  const [min, hr, dom, mon, dow] = p
  const at = (h: string, m: string) => `${h.padStart(2, "0")}:${m.padStart(2, "0")}`
  const everyN = (f: string) => (f.startsWith("*/") ? Number(f.slice(2)) : null)

  // */N minutes
  const nMin = everyN(min)
  if (nMin && hr === "*" && dom === "*" && mon === "*" && dow === "*") {
    return nMin === 1 ? "Every minute" : `Every ${nMin} minutes`
  }
  // every N hours (on the minute)
  const nHr = everyN(hr)
  if (/^\d+$/.test(min) && nHr && dom === "*" && mon === "*" && dow === "*") {
    return `Every ${nHr} hour${nHr === 1 ? "" : "s"}`
  }
  // fixed minute + hour
  if (/^\d+$/.test(min) && /^\d+$/.test(hr)) {
    const time = at(hr, min)
    if (dom === "*" && mon === "*" && dow === "*") return `Every day at ${time}`
    if (dom === "*" && mon === "*" && /^\d+$/.test(dow)) {
      const d = DOW[Number(dow) % 7] ?? `day ${dow}`
      return `Every ${d} at ${time}`
    }
    if (/^\d+$/.test(dom) && mon === "*" && dow === "*") return `Monthly on the ${dom}. at ${time}`
  }
  return expr
}

// Subset of internal/api scheduleResponse we render.
interface Schedule {
  id: string
  name: string
  target_pipeline_slug?: string
  cron_expr: string
  timezone: string
  enabled: boolean
  last_run_at?: string
  last_status?: string
  last_run_id?: string
  next_run_at?: string
}

/**
 * Schedule — the recurring trigger for the selected routine: cron, timezone,
 * enabled state, next + last run. Includes a "Run now" action wired to
 * POST /api/v1/workspaces/{ws}/pipeline-schedules/{id}/run.
 * Reads the existing GET .../pipeline-schedules list and matches the row.
 */
export function ScheduleTab({ workspaceId, context }: { workspaceId: string; context: BottomPanelContext }) {
  const [schedule, setSchedule] = useState<Schedule | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [loading, setLoading] = useState(true)
  const [running, setRunning] = useState(false)
  const [ran, setRan] = useState(false)

  const isRoutine = context?.kind === "routine"
  const slug = isRoutine ? context.slug : null
  const scheduleId = isRoutine ? context.scheduleId : null

  useEffect(() => {
    if (!isRoutine) return
    let cancelled = false
    setLoading(true)
    setSchedule(null)
    setError(null)
    // Clear the "Triggered ✓" state so a freshly selected routine doesn't
    // inherit the previous one's success button.
    setRan(false)
    apiFetch(`/api/v1/workspaces/${workspaceId}/pipeline-schedules`)
      .then((r) => (r.ok ? r.json() : Promise.reject(new Error(`HTTP ${r.status}`))))
      .then((data: Schedule[]) => {
        if (cancelled) return
        const list = Array.isArray(data) ? data : []
        const match = list.find((s) =>
          (scheduleId && s.id === scheduleId) || (slug && s.target_pipeline_slug === slug),
        )
        setSchedule(match ?? null)
      })
      .catch((err) => { if (!cancelled) setError(err instanceof Error ? err.message : String(err)) })
      .finally(() => { if (!cancelled) setLoading(false) })
    return () => { cancelled = true }
  }, [isRoutine, slug, scheduleId, workspaceId])

  const runNow = async () => {
    if (!schedule || running) return
    setRunning(true)
    setRan(false)
    try {
      const r = await apiFetch(`/api/v1/workspaces/${workspaceId}/pipeline-schedules/${schedule.id}/run`, { method: "POST" })
      if (!r.ok) throw new Error(`HTTP ${r.status}`)
      setRan(true)
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err))
    } finally {
      setRunning(false)
    }
  }

  if (!context) return <EmptyState>Select a routine to see its schedule.</EmptyState>
  if (context.kind !== "routine") return <EmptyState>Schedule is shown per routine.</EmptyState>
  if (error) return <EmptyState><span className="text-red-300">Failed to load: {error}</span></EmptyState>
  if (loading) return <EmptyState>Loading…</EmptyState>
  if (!schedule) return <EmptyState>This routine has no schedule — it runs only when triggered manually.</EmptyState>

  const cells: Array<{ k: string; v: React.ReactNode }> = [
    { k: "Runs", v: (
      <span>
        {describeCron(schedule.cron_expr)}
        <span className="ml-2 font-mono text-[10px] text-muted-foreground-soft">{schedule.cron_expr}</span>
      </span>
    ) },
    { k: "Timezone", v: schedule.timezone || "UTC" },
    { k: "Status", v: <span className={schedule.enabled ? "text-emerald-300" : "text-muted-foreground"}>{schedule.enabled ? "Enabled" : "Paused"}</span> },
    { k: "Next run", v: schedule.next_run_at ? formatRelative(schedule.next_run_at) : "—" },
    { k: "Last run", v: schedule.last_run_at ? formatRelative(schedule.last_run_at) : "—" },
    { k: "Last status", v: schedule.last_status ? <span className={statusColor(schedule.last_status)}>{schedule.last_status}</span> : "—" },
  ]

  return (
    <div className="h-full overflow-y-auto p-4 text-xs">
      <div className="flex items-center justify-between mb-4">
        <div className="text-foreground font-medium">{schedule.name}</div>
        <button
          type="button"
          onClick={runNow}
          disabled={running}
          className={cn(
            "px-3 py-1.5 rounded-md text-xs flex items-center gap-1.5 transition-colors",
            ran ? "bg-emerald-600 text-white" : "bg-blue-600 text-white hover:bg-blue-500",
            running && "opacity-60",
          )}
        >
          <Play className="h-3 w-3" /> {running ? "Starting…" : ran ? "Triggered ✓" : "Run now"}
        </button>
      </div>
      <div className="grid grid-cols-2 gap-2.5 max-w-xl">
        {cells.map((c) => (
          <div key={c.k} className="bg-background/40 border border-white/8 rounded-lg px-3 py-2.5">
            <div className="text-[10px] uppercase tracking-wide text-muted-foreground-soft mb-1.5">{c.k}</div>
            <div className="text-foreground">{c.v}</div>
          </div>
        ))}
      </div>
    </div>
  )
}
