"use client"

import { useState, useEffect, useMemo } from "react"
import { useParams } from "next/navigation"
import { Loader2, Clock, Save, Calendar } from "lucide-react"
import { Button } from "@/components/ui/button"
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card"
import { Input } from "@/components/ui/input"
import { Textarea } from "@/components/ui/textarea"
import { Label } from "@/components/ui/label"
import { Switch } from "@/components/ui/switch"
import { Badge } from "@/components/ui/badge"
import { useWorkspace } from "@/hooks/use-workspace"
import { formatDateTime } from "@/lib/time"
import { toast } from "sonner"
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

export function SchedulePageClient() {
  const params = useParams<{ agentId: string }>()
  const agentId = params.agentId
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
    if (!workspaceId) {
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
    if (!workspaceId) {
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
    if (!workspaceId) return
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
    return (
      <div className="flex items-center justify-center p-12">
        <Loader2 className="h-6 w-6 animate-spin text-muted-foreground" />
      </div>
    )
  }

  return (
    <div className="p-4 sm:p-6 space-y-6 max-w-3xl">
      <Card>
        <CardHeader>
          <CardTitle className="text-base flex items-center gap-2">
            <Clock className="h-4 w-4" />
            Schedule
          </CardTitle>
        </CardHeader>
        <CardContent className="space-y-5">
          {/* Enable toggle */}
          <div className="flex items-center justify-between">
            <div>
              <Label className="text-sm font-medium">Enable Schedule</Label>
              <p className="text-xs text-muted-foreground mt-0.5">
                Automatically run this agent on a schedule
              </p>
            </div>
            <Switch checked={enabled} onCheckedChange={setEnabled} />
          </div>

          {/* Cron expression */}
          <div className="space-y-2">
            <Label htmlFor="cron">Cron Expression</Label>
            <Input
              id="cron"
              value={cronExpr}
              onChange={(e) => setCronExpr(e.target.value)}
              placeholder="0 8 * * *"
              className={`font-mono text-sm ${cronExpr && !cronValid ? "border-red-500" : ""}`}
            />
            {cronExpr && !cronValid && (
              <p className="text-xs text-red-500">Invalid cron expression</p>
            )}
            {nextRuns.length > 0 && (
              <div className="text-xs text-muted-foreground space-y-0.5">
                <span className="font-medium">Next runs:</span>
                {nextRuns.map((r, i) => (
                  <div key={i} className="ml-2 flex items-center gap-1">
                    <Calendar className="h-3 w-3" /> {r}
                  </div>
                ))}
              </div>
            )}
          </div>

          {/* Presets */}
          <div className="space-y-1.5">
            <Label className="text-xs text-muted-foreground">Quick presets</Label>
            <div className="flex flex-wrap gap-1.5">
              {presets.map((p) => (
                <button
                  key={p.cron}
                  onClick={() => setCronExpr(p.cron)}
                  className="text-xs px-2.5 py-1 rounded-md border border-border bg-background hover:bg-accent transition-colors"
                >
                  {p.label}
                </button>
              ))}
            </div>
          </div>

          {/* Schedule prompt */}
          <div className="space-y-2">
            <Label htmlFor="prompt">Schedule Prompt</Label>
            <Textarea
              id="prompt"
              value={prompt}
              onChange={(e) => setPrompt(e.target.value)}
              placeholder="What should the agent do on each scheduled run? e.g. 'Check for new support tickets and summarize them.'"
              rows={4}
              className="resize-none"
            />
            <p className="text-xs text-muted-foreground">
              This message is sent to the agent as the user message when the schedule triggers.
            </p>
          </div>

          {/* Save */}
          <div className="flex items-center gap-3">
            <Button onClick={handleSave} disabled={saving} className="gap-2">
              {saving ? <Loader2 className="h-4 w-4 animate-spin" /> : <Save className="h-4 w-4" />}
              Save Schedule
            </Button>
            {lastRun && (
              <span className="text-xs text-muted-foreground">
                Last run: {formatDateTime(lastRun)}
              </span>
            )}
            {nextRun && enabled && (
              <span className="text-xs text-muted-foreground">
                Next: {formatDateTime(nextRun)}
              </span>
            )}
          </div>
        </CardContent>
      </Card>

      {/* Recent scheduled runs */}
      <Card>
        <CardHeader>
          <CardTitle className="text-base flex items-center gap-2">
            <Calendar className="h-4 w-4" />
            Recent Scheduled Runs
          </CardTitle>
        </CardHeader>
        <CardContent>
          {runsLoading ? (
            <div className="flex items-center gap-2 text-sm text-muted-foreground">
              <Loader2 className="h-4 w-4 animate-spin" /> Loading...
            </div>
          ) : runs.length === 0 ? (
            <p className="text-sm text-muted-foreground">No scheduled runs yet.</p>
          ) : (
            <div className="space-y-2">
              {runs.map((run) => (
                <div
                  key={run.id}
                  className="flex items-center justify-between rounded-lg border border-border p-3"
                >
                  <div className="flex items-center gap-3">
                    <Badge
                      variant={
                        run.status === "COMPLETED"
                          ? "default"
                          : run.status === "FAILED"
                          ? "destructive"
                          : "secondary"
                      }
                      className="text-xs"
                    >
                      {run.status}
                    </Badge>
                    <span className="text-sm text-muted-foreground">
                      {formatDateTime(run.started_at)}
                    </span>
                  </div>
                  <div className="text-xs text-muted-foreground">
                    {run.metadata && typeof run.metadata === "object" && "duration_ms" in run.metadata
                      ? formatDuration(run.metadata.duration_ms as number)
                      : ""}
                  </div>
                </div>
              ))}
            </div>
          )}
        </CardContent>
      </Card>
    </div>
  )
}
