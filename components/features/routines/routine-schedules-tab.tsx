"use client"

import { useMemo, useState } from "react"
import { Plus, Trash2, Calendar, Power, PowerOff } from "lucide-react"
import { usePipelineSchedules, type PipelineSchedule } from "@/hooks/use-pipeline-schedules"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { toast } from "sonner"
import { cn } from "@/lib/utils"
import { RoutineListSkeleton } from "./routine-skeletons"
import { Card, EmptyState, Pill, FieldLabel } from "./_shared"

// RoutineSchedulesTab — cron-trigger CRUD restyled for the dashboard.
// Card-wrapped list + inline form, Pill states, readable typography,
// describeCron hint stays as a human sanity-check.

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

  if (loading && ours.length === 0) {
    return (
      <Card title="Schedules" subtitle="loading…">
        <div className="p-4">
          <RoutineListSkeleton rows={2} />
        </div>
      </Card>
    )
  }

  return (
    <div className="space-y-4">
      {error && (
        <Card tone="amber">
          <div className="px-4 py-3 text-sm text-amber-300">{error}</div>
        </Card>
      )}

      {/* List card */}
      {ours.length === 0 && !formOpen ? (
        <Card title="Schedules">
          <EmptyState
            icon={Calendar}
            title="No schedules yet"
            description="Add a cron trigger to run this routine on a cadence. Schedules are workspace-wide cron jobs that invoke this routine with optional preset inputs."
            action={
              <Button
                size="sm"
                variant="default"
                onClick={() => setFormOpen(true)}
                className="h-9 gap-1.5 px-4 text-sm"
              >
                <Plus className="h-3.5 w-3.5" />
                Add schedule
              </Button>
            }
          />
        </Card>
      ) : (
        <Card
          title="Active schedules"
          subtitle={`${ours.length} for this routine`}
          action={
            !formOpen && (
              <Button
                size="sm"
                variant="outline"
                onClick={() => setFormOpen(true)}
                className="h-8 gap-1.5 text-xs"
              >
                <Plus className="h-3 w-3" />
                Add schedule
              </Button>
            )
          }
        >
          <ol className="divide-y divide-border/40">
            {ours.map((s) => (
              <li key={s.id} className="grid grid-cols-[auto_1fr_auto] items-start gap-3 px-4 py-3">
                <div
                  className={cn(
                    "flex h-9 w-9 shrink-0 items-center justify-center rounded-lg",
                    s.enabled
                      ? "bg-violet-500/20 text-violet-400"
                      : "bg-muted text-muted-foreground",
                  )}
                >
                  <Calendar className="h-4 w-4" />
                </div>
                <div className="min-w-0 space-y-1">
                  <div className="flex flex-wrap items-center gap-2">
                    <span className="truncate text-sm font-semibold">{s.name}</span>
                    <Pill tone={s.enabled ? "violet" : "default"}>
                      {s.enabled ? "enabled" : "paused"}
                    </Pill>
                  </div>
                  <div className="flex flex-wrap items-baseline gap-x-3 font-mono text-[12px] text-muted-foreground">
                    <span>{s.cron_expr}</span>
                    <span className="opacity-60">·</span>
                    <span>{s.timezone}</span>
                    <span className="text-foreground/70">— {describeCron(s.cron_expr)}</span>
                  </div>
                  {(s.next_run_at || s.last_run_at) && (
                    <div className="flex flex-wrap items-center gap-x-3 text-[11px] text-muted-foreground">
                      {s.next_run_at && (
                        <span>
                          Next: <span className="text-foreground/85">{new Date(s.next_run_at).toLocaleString()}</span>
                        </span>
                      )}
                      {s.last_run_at && (
                        <span>
                          Last: <span className="text-foreground/85">{new Date(s.last_run_at).toLocaleString()}</span>
                          {s.last_status && (
                            <span className="ml-1 text-muted-foreground/70">({s.last_status})</span>
                          )}
                        </span>
                      )}
                    </div>
                  )}
                </div>
                <div className="flex shrink-0 items-center gap-1">
                  <Button
                    size="sm"
                    variant="ghost"
                    onClick={() => toggle(s)}
                    className="h-8 w-8 p-0"
                    title={s.enabled ? "Disable" : "Enable"}
                    aria-label={`${s.enabled ? "Disable" : "Enable"} schedule ${s.name}`}
                  >
                    {s.enabled ? <PowerOff className="h-3.5 w-3.5" /> : <Power className="h-3.5 w-3.5" />}
                  </Button>
                  <Button
                    size="sm"
                    variant="ghost"
                    onClick={() => del(s)}
                    className="h-8 w-8 p-0 text-muted-foreground hover:text-rose-400"
                    title="Delete"
                    aria-label={`Delete schedule ${s.name}`}
                  >
                    <Trash2 className="h-3.5 w-3.5" />
                  </Button>
                </div>
              </li>
            ))}
          </ol>
        </Card>
      )}

      {/* Inline form */}
      {formOpen && (
        <Card title="New schedule">
          <div className="space-y-4 p-4">
            <div>
              <FieldLabel>Name</FieldLabel>
              <Input
                value={name}
                onChange={(e) => setName(e.target.value)}
                placeholder={`${slug} schedule`}
                className="mt-1.5 h-9 text-sm"
              />
            </div>
            <div className="grid grid-cols-2 gap-3">
              <div>
                <FieldLabel>Cron expression</FieldLabel>
                <Input
                  aria-label="Cron expression"
                  value={cronExpr}
                  onChange={(e) => setCronExpr(e.target.value)}
                  className="mt-1.5 h-9 font-mono text-sm"
                />
                <p className="mt-1.5 text-[11px] text-muted-foreground">{describeCron(cronExpr)}</p>
              </div>
              <div>
                <FieldLabel>Timezone</FieldLabel>
                <Input
                  aria-label="Timezone"
                  value={timezone}
                  onChange={(e) => setTimezone(e.target.value)}
                  className="mt-1.5 h-9 text-sm"
                />
              </div>
            </div>
            <div>
              <FieldLabel>Inputs (JSON)</FieldLabel>
              <textarea
                aria-label="Inputs JSON"
                value={inputsJson}
                onChange={(e) => setInputsJson(e.target.value)}
                className="mt-1.5 h-24 w-full resize-none rounded-md border border-white/[0.1] bg-background p-2.5 font-mono text-[12px] leading-relaxed"
              />
              <p className="mt-1.5 text-[11px] text-muted-foreground">
                Passed as inputs to every invocation. Leave as <span className="font-mono">{`{}`}</span> for default inputs.
              </p>
            </div>
            <div className="flex justify-end gap-2">
              <Button size="sm" variant="ghost" onClick={() => setFormOpen(false)} disabled={busy} className="h-9 px-4">
                Cancel
              </Button>
              <Button size="sm" onClick={submit} disabled={busy} className="h-9 px-4">
                {busy ? "Creating…" : "Create schedule"}
              </Button>
            </div>
          </div>
        </Card>
      )}
    </div>
  )
}

// describeCron is a tiny humanizer for the most common patterns.
function describeCron(expr: string): string {
  const t = expr.trim()
  const parts = t.split(/\s+/)
  if (parts.length !== 5) return `${parts.length}-field cron (need 5)`
  const [m, h, dom, mon, dow] = parts
  if (m === "*" && h === "*" && dom === "*" && mon === "*" && dow === "*") return "every minute"
  if (dom === "*" && mon === "*" && dow === "*")
    return `daily at ${h.padStart(2, "0")}:${m.padStart(2, "0")}`
  if (dom === "*" && mon === "*" && dow !== "*")
    return `weekly (dow ${dow}) at ${h.padStart(2, "0")}:${m.padStart(2, "0")}`
  return "custom cron"
}
