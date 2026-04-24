"use client"

import { useState, useEffect, useMemo } from "react"
import { useAgentId } from "@/hooks/use-agent-id"
import { Loader2, Clock, Save, Calendar, MessageSquare, Power, Activity } from "lucide-react"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Textarea } from "@/components/ui/textarea"
import { Label } from "@/components/ui/label"
import { Switch } from "@/components/ui/switch"
import { Skeleton } from "@/components/ui/skeleton"
import { SectionCard } from "@/components/ui/section-card"
import { StatusBadge } from "@/components/ui/status-badge"
import { EmptyState } from "@/components/layout/empty-state"
import { PropertyRow } from "@/components/layout/property-row"
import { useWorkspace } from "@/hooks/use-workspace"
import { formatDateTime } from "@/lib/time"
import { toast } from "sonner"
import { cn } from "@/lib/utils"
import { CronExpressionParser } from "cron-parser"

interface ScheduleRun {
  id: string
  status: string
  started_at: string
  finished_at: string | null
  trigger_type: string
  metadata: Record<string, unknown> | null
}

const presets = [
  { label: "Every hour", cron: "0 * * * *" },
  { label: "Every day 8:00", cron: "0 8 * * *" },
  { label: "Every Monday 9:00", cron: "0 9 * * 1" },
  { label: "Every 1st of month", cron: "0 9 1 * *" },
  { label: "Every 6 hours", cron: "0 */6 * * *" },
  { label: "Weekdays 9:00", cron: "0 9 * * 1-5" },
]

function getNextRuns(cronExpr: string, count: number): string[] {
  try {
    const expr = CronExpressionParser.parse(cronExpr)
    const runs: string[] = []
    for (let i = 0; i < count; i++) {
      runs.push(expr.next().toDate().toLocaleString())
    }
    return runs
  } catch {
    return []
  }
}

function formatDuration(ms: number): string {
  if (ms < 1000) return `${ms}ms`
  const s = Math.round(ms / 1000)
  if (s < 60) return `${s}s`
  const m = Math.floor(s / 60)
  return `${m}m ${s % 60}s`
}

export function ScheduleSection() {
  const agentId = useAgentId()
  const { workspaceId, loading: wsLoading } = useWorkspace()

  const [loading, setLoading] = useState(true)
  const [saving, setSaving] = useState(false)
  const [cronExpr, setCronExpr] = useState("")
  const [prompt, setPrompt] = useState("")
  const [enabled, setEnabled] = useState(false)
  const [lastRun, setLastRun] = useState<string | null>(null)
  const [nextRun, setNextRun] = useState<string | null>(null)
  const [runs, setRuns] = useState<ScheduleRun[]>([])
  const [runsLoading, setRunsLoading] = useState(true)

  useEffect(() => {
    if (!workspaceId || !agentId) {
      setLoading(false)
      return
    }
    fetch(`/api/v1/agents/${agentId}?workspace_id=${workspaceId}`)
      .then((r) => r.ok ? r.json() : null)
      .then((data) => {
        if (data) {
          setCronExpr(data.schedule_cron || "")
          setPrompt(data.schedule_prompt || "")
          setEnabled(!!data.schedule_enabled)
          setLastRun(data.schedule_last_run || null)
          setNextRun(data.schedule_next_run || null)
        }
      })
      .catch(() => {})
      .finally(() => setLoading(false))
  }, [agentId, workspaceId])

  useEffect(() => {
    if (!workspaceId || !agentId) {
      setRunsLoading(false)
      return
    }
    fetch(`/api/v1/agents/${agentId}/runs?workspace_id=${workspaceId}&trigger_type=SCHEDULED&limit=10`)
      .then((r) => r.ok ? r.json() : null)
      .then((data) => {
        if (data?.items) setRuns(data.items)
        else if (Array.isArray(data)) setRuns(data)
      })
      .catch(() => {})
      .finally(() => setRunsLoading(false))
  }, [agentId, workspaceId])

  const nextRuns = useMemo(() => {
    if (!cronExpr) return []
    return getNextRuns(cronExpr, 3)
  }, [cronExpr])

  const cronValid = useMemo(() => {
    if (!cronExpr) return true
    try {
      CronExpressionParser.parse(cronExpr)
      return true
    } catch {
      return false
    }
  }, [cronExpr])

  const handleSave = async () => {
    if (!workspaceId || !agentId) return
    if (cronExpr && !cronValid) {
      toast.error("Invalid cron expression")
      return
    }
    setSaving(true)
    try {
      const res = await fetch(`/api/v1/agents/${agentId}?workspace_id=${workspaceId}`, {
        method: "PATCH",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({
          schedule_cron: cronExpr || null,
          schedule_prompt: prompt || null,
          schedule_enabled: enabled,
        }),
      })
      if (!res.ok) {
        const d = await res.json()
        toast.error(d.error || "Failed to save schedule")
        return
      }
      const data = await res.json()
      setNextRun(data.schedule_next_run || null)
      toast.success("Schedule saved")
    } catch {
      toast.error("Network error")
    } finally {
      setSaving(false)
    }
  }

  if (wsLoading || loading) {
    return <ScheduleSkeleton />
  }

  return (
    <div className="p-6 space-y-6 max-w-3xl">
      <div>
        <h2 className="text-title font-semibold">Schedule</h2>
        <p className="text-body text-muted-foreground mt-1">
          Run this agent automatically on a recurring cron schedule.
        </p>
      </div>

      <SectionCard
        title={
          <span className="flex items-center gap-2">
            <Clock className="h-4 w-4 text-muted-foreground" />
            Trigger
          </span>
        }
      >
        <div className="space-y-0">
          <PropertyRow label="Enabled" icon={Power}>
            <div className="flex items-center justify-between">
              <span className="text-body text-muted-foreground">
                Automatically run on schedule
              </span>
              <Switch checked={enabled} onCheckedChange={setEnabled} />
            </div>
          </PropertyRow>

          <PropertyRow label="Cron" icon={Clock}>
            <div className="space-y-2">
              <Input
                id="cron"
                value={cronExpr}
                onChange={(e) => setCronExpr(e.target.value)}
                placeholder="0 8 * * *"
                className={cn("font-mono text-label", cronExpr && !cronValid && "border-destructive")}
              />
              {cronExpr && !cronValid && (
                <p className="text-label text-destructive">Invalid cron expression</p>
              )}
              {nextRuns.length > 0 && (
                <div className="text-label text-muted-foreground space-y-0.5">
                  <span className="font-medium">Next runs:</span>
                  {nextRuns.map((r, i) => (
                    <div key={i} className="ml-2 flex items-center gap-1">
                      <Calendar className="h-3 w-3" /> {r}
                    </div>
                  ))}
                </div>
              )}
            </div>
          </PropertyRow>

          <PropertyRow label="Presets">
            <div className="flex flex-wrap gap-1.5">
              {presets.map((p) => (
                <button
                  key={p.cron}
                  onClick={() => setCronExpr(p.cron)}
                  className="text-label px-2.5 py-1 rounded-md border border-border bg-background hover:bg-accent transition-colors"
                >
                  {p.label}
                </button>
              ))}
            </div>
          </PropertyRow>

          <PropertyRow label="Prompt" icon={MessageSquare}>
            <div className="space-y-2">
              <Label htmlFor="prompt" className="sr-only">Schedule prompt</Label>
              <Textarea
                id="prompt"
                value={prompt}
                onChange={(e) => setPrompt(e.target.value)}
                placeholder="What should the agent do on each scheduled run? e.g. 'Check for new support tickets and summarize them.'"
                rows={4}
                className="resize-none"
              />
              <p className="text-label text-muted-foreground">
                This message is sent as the user message when the schedule triggers.
              </p>
            </div>
          </PropertyRow>

          {lastRun && (
            <PropertyRow label="Last run" icon={Activity}>
              <span className="text-body">{formatDateTime(lastRun)}</span>
            </PropertyRow>
          )}
          {nextRun && enabled && (
            <PropertyRow label="Next run" icon={Calendar}>
              <span className="text-body">{formatDateTime(nextRun)}</span>
            </PropertyRow>
          )}
        </div>

        <div className="flex items-center gap-3 pt-4 mt-2 border-t border-border">
          <Button onClick={handleSave} disabled={saving} className="gap-2">
            {saving ? <Loader2 className="h-4 w-4 animate-spin" /> : <Save className="h-4 w-4" />}
            Save Schedule
          </Button>
        </div>
      </SectionCard>

      {/* Recent scheduled runs */}
      <SectionCard
        title={
          <span className="flex items-center gap-2">
            <Calendar className="h-4 w-4 text-muted-foreground" />
            Recent Scheduled Runs
          </span>
        }
        bare
      >
        {runsLoading ? (
          <div className="flex items-center gap-2 p-6 text-body text-muted-foreground">
            <Loader2 className="h-4 w-4 animate-spin" /> Loading...
          </div>
        ) : runs.length === 0 ? (
          <div className="p-6">
            <EmptyState
              icon={Calendar}
              title="No scheduled runs yet"
              description="Once this schedule triggers, runs will appear here."
            />
          </div>
        ) : (
          <ul className="divide-y divide-border">
            {runs.map((run) => {
              // Normalize backend run status to canonical StatusBadge keys.
              // Unknown states fall through to PENDING so styling stays consistent.
              const badgeStatus =
                run.status === "FAILED" || run.status === "ERROR" || run.status === "TIMEOUT"
                  ? "FAILED"
                  : run.status === "COMPLETED" || run.status === "SUCCESS"
                    ? "COMPLETED"
                    : run.status === "RUNNING" || run.status === "IN_PROGRESS"
                      ? "IN_PROGRESS"
                      : "PENDING"
              return (
                <li
                  key={run.id}
                  className="flex items-center justify-between px-4 sm:px-6 py-3"
                >
                  <div className="flex items-center gap-3 min-w-0">
                    <StatusBadge status={badgeStatus} label={run.status} />
                    <span className="text-body text-muted-foreground truncate">
                      {formatDateTime(run.started_at)}
                    </span>
                  </div>
                  <div className="text-label text-muted-foreground shrink-0">
                    {run.metadata && typeof run.metadata === "object" && "duration_ms" in run.metadata
                      ? formatDuration(run.metadata.duration_ms as number)
                      : ""}
                  </div>
                </li>
              )
            })}
          </ul>
        )}
      </SectionCard>
    </div>
  )
}

function ScheduleSkeleton() {
  return (
    <div className="p-6 space-y-6 max-w-3xl">
      <div className="space-y-2">
        <Skeleton className="h-7 w-40" />
        <Skeleton className="h-4 w-72" />
      </div>
      <SectionCard title={<Skeleton className="h-5 w-24" />}>
        <div className="space-y-4">
          <Skeleton className="h-10 w-full" />
          <Skeleton className="h-10 w-full" />
          <Skeleton className="h-24 w-full" />
        </div>
      </SectionCard>
    </div>
  )
}
