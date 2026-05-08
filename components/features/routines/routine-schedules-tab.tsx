"use client"

import { useMemo, useState } from "react"
import { Plus, Trash2, Calendar, Power, PowerOff } from "lucide-react"
import { usePipelineSchedules, type PipelineSchedule } from "@/hooks/use-pipeline-schedules"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Badge } from "@/components/ui/badge"
import { toast } from "sonner"
import { cn } from "@/lib/utils"
import { RoutineListSkeleton } from "./routine-skeletons"

// RoutineSchedulesTab — cron-trigger CRUD for one routine. Wires the
// previously-dead usePipelineSchedules hook to actual UI. List shows
// existing schedules for this routine; the form below creates new
// ones with cron expression + timezone + optional inputs JSON.

interface Props {
  workspaceId: string
  pipelineId: string
  slug: string
}

export function RoutineSchedulesTab({ workspaceId, pipelineId, slug }: Props) {
  const { schedules, loading, error, create, update, remove } = usePipelineSchedules(workspaceId)
  const ours = useMemo(
    () => schedules.filter((s) => s.target_pipeline_id === pipelineId || s.target_pipeline_slug === slug),
    [schedules, pipelineId, slug],
  )

  const [formOpen, setFormOpen] = useState(false)
  const [name, setName] = useState("")
  const [cronExpr, setCronExpr] = useState("0 9 * * *")
  const [timezone, setTimezone] = useState(Intl.DateTimeFormat().resolvedOptions().timeZone || "UTC")
  const [inputsJson, setInputsJson] = useState("{}")
  const [busy, setBusy] = useState(false)

  const submit = async () => {
    setBusy(true)
    try {
      let inputs: Record<string, unknown> = {}
      try {
        inputs = inputsJson.trim() ? JSON.parse(inputsJson) : {}
      } catch {
        toast.error("Inputs must be valid JSON")
        setBusy(false)
        return
      }
      await create({
        name: name || `${slug} schedule`,
        target_pipeline_slug: slug,
        cron_expr: cronExpr,
        timezone,
        inputs,
        enabled: true,
      })
      toast.success("Schedule created")
      setFormOpen(false)
      setName("")
      setCronExpr("0 9 * * *")
      setInputsJson("{}")
    } catch (e) {
      toast.error("Create failed", { description: e instanceof Error ? e.message : String(e) })
    } finally {
      setBusy(false)
    }
  }

  const toggle = async (s: PipelineSchedule) => {
    try {
      await update(s.id, { cron_expr: s.cron_expr, enabled: !s.enabled })
      toast.success(s.enabled ? "Disabled" : "Enabled")
    } catch (e) {
      toast.error("Toggle failed", { description: e instanceof Error ? e.message : String(e) })
    }
  }

  const del = async (s: PipelineSchedule) => {
    if (!confirm(`Delete schedule "${s.name}"?`)) return
    try {
      await remove(s.id)
      toast.success("Schedule deleted")
    } catch (e) {
      toast.error("Delete failed", { description: e instanceof Error ? e.message : String(e) })
    }
  }

  if (loading && ours.length === 0) return <RoutineListSkeleton rows={2} />

  return (
    <div className="space-y-3">
      {error && (
        <div className="rounded-md border border-amber-500/30 bg-amber-500/5 px-3 py-2 text-xs text-amber-400">
          {error}
        </div>
      )}

      {/* List */}
      {ours.length === 0 && !formOpen ? (
        <div className="rounded-md border border-dashed border-border/60 p-6 text-center">
          <Calendar className="mx-auto mb-2 h-6 w-6 text-muted-foreground/50" />
          <p className="text-xs text-muted-foreground">No schedules yet for this routine.</p>
          <Button size="sm" variant="outline" onClick={() => setFormOpen(true)} className="mt-2 h-7 gap-1.5 text-xs">
            <Plus className="h-3 w-3" />
            Add schedule
          </Button>
        </div>
      ) : (
        <ol className="space-y-1.5">
          {ours.map((s) => (
            <li key={s.id} className="rounded-md border border-white/[0.06] bg-card/40 p-2.5 text-[11px]">
              <div className="flex items-start justify-between gap-2">
                <div className="min-w-0 flex-1">
                  <div className="flex items-center gap-2">
                    <span className="truncate font-medium">{s.name}</span>
                    <Badge variant="outline" className={cn("text-[9px]", !s.enabled && "opacity-50")}>
                      {s.enabled ? "enabled" : "disabled"}
                    </Badge>
                  </div>
                  <div className="mt-0.5 font-mono text-[10px] text-muted-foreground">
                    {s.cron_expr} · {s.timezone}
                  </div>
                  {s.next_run_at && (
                    <div className="mt-0.5 text-[10px] text-muted-foreground">
                      Next: {new Date(s.next_run_at).toLocaleString()}
                    </div>
                  )}
                  {s.last_run_at && (
                    <div className="text-[10px] text-muted-foreground">
                      Last: {new Date(s.last_run_at).toLocaleString()} ({s.last_status ?? "?"})
                    </div>
                  )}
                </div>
                <div className="flex items-center gap-1">
                  <Button
                    size="sm"
                    variant="ghost"
                    onClick={() => toggle(s)}
                    className="h-6 w-6 p-0"
                    title={s.enabled ? "Disable" : "Enable"}
                    aria-label={`${s.enabled ? "Disable" : "Enable"} schedule ${s.name}`}
                  >
                    {s.enabled ? <PowerOff className="h-3 w-3" aria-hidden="true" /> : <Power className="h-3 w-3" aria-hidden="true" />}
                  </Button>
                  <Button
                    size="sm"
                    variant="ghost"
                    onClick={() => del(s)}
                    className="h-6 w-6 p-0 text-muted-foreground hover:text-red-400"
                    title="Delete"
                    aria-label={`Delete schedule ${s.name}`}
                  >
                    <Trash2 className="h-3 w-3" aria-hidden="true" />
                  </Button>
                </div>
              </div>
            </li>
          ))}
          {!formOpen && (
            <li>
              <Button size="sm" variant="outline" onClick={() => setFormOpen(true)} className="w-full h-7 gap-1.5 text-xs">
                <Plus className="h-3 w-3" />
                Add another schedule
              </Button>
            </li>
          )}
        </ol>
      )}

      {/* Inline form */}
      {formOpen && (
        <div className="space-y-2 rounded-md border border-white/[0.1] bg-card/60 p-3">
          <h4 className="text-xs font-medium">New schedule</h4>
          <div>
            <label className="text-[10px] uppercase tracking-wider text-muted-foreground">Name</label>
            <Input value={name} onChange={(e) => setName(e.target.value)} placeholder={`${slug} schedule`} className="h-7 text-xs" />
          </div>
          <div className="grid grid-cols-2 gap-2">
            <div>
              <label className="text-[10px] uppercase tracking-wider text-muted-foreground">Cron expression</label>
              <Input value={cronExpr} onChange={(e) => setCronExpr(e.target.value)} className="h-7 font-mono text-xs" />
              <p className="mt-1 text-[10px] text-muted-foreground">{describeCron(cronExpr)}</p>
            </div>
            <div>
              <label className="text-[10px] uppercase tracking-wider text-muted-foreground">Timezone</label>
              <Input value={timezone} onChange={(e) => setTimezone(e.target.value)} className="h-7 text-xs" />
            </div>
          </div>
          <div>
            <label className="text-[10px] uppercase tracking-wider text-muted-foreground">Inputs (JSON)</label>
            <textarea
              value={inputsJson}
              onChange={(e) => setInputsJson(e.target.value)}
              className="h-20 w-full resize-none rounded-md border border-white/10 bg-background p-2 font-mono text-[10px]"
            />
          </div>
          <div className="flex justify-end gap-2">
            <Button size="sm" variant="ghost" onClick={() => setFormOpen(false)} disabled={busy}>
              Cancel
            </Button>
            <Button size="sm" onClick={submit} disabled={busy}>
              {busy ? "Creating…" : "Create"}
            </Button>
          </div>
        </div>
      )}
    </div>
  )
}

// describeCron is a tiny humanizer for the most common patterns. We
// intentionally don't ship a full cron parser — just a hint label so
// users can sanity-check their expression. Misses fall through to a
// generic "custom cron" label.
function describeCron(expr: string): string {
  const t = expr.trim()
  const parts = t.split(/\s+/)
  if (parts.length !== 5) return `${parts.length}-field cron (need 5)`
  const [m, h, dom, mon, dow] = parts
  if (m === "*" && h === "*" && dom === "*" && mon === "*" && dow === "*") return "every minute"
  if (dom === "*" && mon === "*" && dow === "*") return `daily at ${h.padStart(2, "0")}:${m.padStart(2, "0")}`
  if (dom === "*" && mon === "*" && dow !== "*") return `weekly (${dow}) at ${h.padStart(2, "0")}:${m.padStart(2, "0")}`
  return "custom cron"
}
