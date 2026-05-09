"use client"

import { Calendar, Pause, Play, Plus, Webhook } from "lucide-react"
import { Button } from "@/components/ui/button"
import { Skeleton } from "@/components/ui/skeleton"
import { cn } from "@/lib/utils"
import { usePipelineSchedules } from "@/hooks/use-pipeline-schedules"
import type { Pipeline } from "@/hooks/use-pipelines"

// RoutinesSchedulesView — workspace-wide list of every cron schedule
// bound to a routine. Replaces the per-routine Schedules tab inside the
// detail panel as the primary entry point: when an operator wants to
// know "what runs unattended this week" they look here, not at each
// routine individually.
//
// Webhook triggers live on the routine itself (not as a separate row),
// so we surface a small badge per routine with a webhook count rather
// than mixing two trigger types into one table.

interface RoutinesSchedulesViewProps {
  workspaceId: string
  routines: Pipeline[]
  onSelect: (slug: string) => void
}

function statusColor(status: string | undefined): string {
  switch (status?.toLowerCase()) {
    case "succeeded":
    case "success":
      return "text-emerald-400"
    case "failed":
    case "error":
      return "text-rose-400"
    case "running":
      return "text-blue-400"
    default:
      return "text-muted-foreground"
  }
}

function relativeTime(iso?: string): string {
  if (!iso) return "—"
  const d = new Date(iso)
  const diff = Date.now() - d.getTime()
  if (Math.abs(diff) < 60_000) return "just now"
  const fwd = diff < 0
  const abs = Math.abs(diff)
  const mins = Math.round(abs / 60_000)
  if (mins < 60) return fwd ? `in ${mins}m` : `${mins}m ago`
  const hrs = Math.round(mins / 60)
  if (hrs < 24) return fwd ? `in ${hrs}h` : `${hrs}h ago`
  const days = Math.round(hrs / 24)
  return fwd ? `in ${days}d` : `${days}d ago`
}

export function RoutinesSchedulesView({
  workspaceId,
  routines,
  onSelect,
}: RoutinesSchedulesViewProps) {
  const { schedules, loading, error } = usePipelineSchedules(workspaceId)

  const slugByPipelineId = new Map(routines.map((r) => [r.id, r.slug]))

  if (loading) {
    return (
      <div className="p-6 space-y-2">
        {[0, 1, 2].map((i) => (
          <Skeleton key={i} className="h-12 w-full rounded-md" />
        ))}
      </div>
    )
  }

  if (error) {
    return (
      <div className="p-6 text-sm text-rose-400">
        Schedules unavailable: {error}
      </div>
    )
  }

  if (schedules.length === 0) {
    return (
      <div className="flex h-full flex-col items-center justify-center gap-3 p-6 text-center">
        <Calendar className="h-10 w-10 text-muted-foreground/40" />
        <div className="text-sm font-medium">No schedules yet</div>
        <p className="max-w-md text-xs text-muted-foreground">
          Schedules fire a saved routine on a cron expression — perfect for
          recurring jobs like &quot;every weekday at 8am, summarize new
          tickets.&quot; Open a routine and use the Schedules tab to add one.
        </p>
        <Button size="sm" variant="outline" className="mt-2 gap-1.5 text-xs">
          <Plus className="h-3 w-3" />
          New schedule
        </Button>
      </div>
    )
  }

  return (
    <div className="h-full overflow-auto p-6">
      <div className="mb-4 flex items-center justify-between">
        <div>
          <div className="text-base font-semibold">Schedules</div>
          <div className="text-xs text-muted-foreground">
            {schedules.length} schedule{schedules.length === 1 ? "" : "s"} across the workspace
          </div>
        </div>
        <Button size="sm" variant="default" className="gap-1.5 text-xs">
          <Plus className="h-3 w-3" />
          New schedule
        </Button>
      </div>

      <div className="overflow-hidden rounded-md border border-white/[0.06]">
        <table className="w-full text-xs">
          <thead className="bg-card/40 text-[11px] uppercase tracking-wider text-muted-foreground">
            <tr>
              <th className="px-3 py-2 text-left font-medium">Name</th>
              <th className="px-3 py-2 text-left font-medium">Routine</th>
              <th className="px-3 py-2 text-left font-medium">Cron</th>
              <th className="px-3 py-2 text-left font-medium">Last run</th>
              <th className="px-3 py-2 text-left font-medium">Next run</th>
              <th className="px-3 py-2 text-right font-medium">Status</th>
            </tr>
          </thead>
          <tbody className="divide-y divide-white/[0.04]">
            {schedules.map((s) => {
              const slug = s.target_pipeline_slug ?? slugByPipelineId.get(s.target_pipeline_id) ?? ""
              return (
                <tr
                  key={s.id}
                  className="cursor-pointer transition-colors hover:bg-card/40"
                  onClick={() => slug && onSelect(slug)}
                >
                  <td className="px-3 py-2.5 font-medium">
                    <div className="flex items-center gap-2">
                      {s.enabled ? (
                        <Play className="h-3 w-3 text-emerald-400" aria-label="enabled" />
                      ) : (
                        <Pause className="h-3 w-3 text-muted-foreground" aria-label="paused" />
                      )}
                      <span>{s.name}</span>
                    </div>
                  </td>
                  <td className="px-3 py-2.5 text-muted-foreground">{slug || "—"}</td>
                  <td className="px-3 py-2.5 font-mono text-[11px] text-muted-foreground">
                    {s.cron_expr}
                  </td>
                  <td className="px-3 py-2.5 text-muted-foreground">
                    {relativeTime(s.last_run_at)}
                  </td>
                  <td className="px-3 py-2.5 text-muted-foreground">
                    {relativeTime(s.next_run_at)}
                  </td>
                  <td
                    className={cn(
                      "px-3 py-2.5 text-right font-medium",
                      statusColor(s.last_status),
                    )}
                  >
                    {s.last_status ?? "—"}
                  </td>
                </tr>
              )
            })}
          </tbody>
        </table>
      </div>

      <div className="mt-4 flex items-center gap-1.5 text-[11px] text-muted-foreground">
        <Webhook className="h-3 w-3" />
        Webhook triggers are configured per-routine in the detail panel.
      </div>
    </div>
  )
}
