"use client"

import { useCallback, useEffect, useMemo, useRef, useState } from "react"
import {
  Play, Square, Clock, Coins, CheckCircle2, AlertTriangle,
  Loader2, ChevronRight, RotateCcw, Copy,
} from "lucide-react"
import { Spinner } from "@/components/ui/spinner"
import { Button } from "@/components/ui/button"
import { Progress } from "@/components/ui/progress"
import { Badge } from "@/components/ui/badge"
import { cn } from "@/lib/utils"
import { apiFetch } from "@/lib/api-fetch"
import { toast } from "sonner"
import type { Mission } from "@/lib/types/mission"

interface MissionControlBarProps {
  mission: Mission
  workspaceId: string
  onMissionChanged: () => void
}

const statusConfig: Record<string, { color: string; label: string; icon: React.ElementType }> = {
  PLANNING: { color: "text-purple bg-purple/10 border-purple/30", label: "Planning", icon: Clock },
  IN_PROGRESS: { color: "text-primary bg-primary/10 border-primary/30", label: "Running", icon: Loader2 },
  REVIEW: { color: "text-warn bg-warn/10 border-warn/30", label: "In Review", icon: ChevronRight },
  // B13 (#2370): the backend now writes DONE, not COMPLETED, for an
  // orchestration mission approved out of REVIEW (PRD-ISSUES-AND-ROUTINES-
  // 2026 §3.1). COMPLETED stays mapped too — a not-yet-backfilled row, or
  // an older server, can still report it.
  DONE: { color: "text-success bg-success/10 border-success/30", label: "Completed", icon: CheckCircle2 },
  COMPLETED: { color: "text-success bg-success/10 border-success/30", label: "Completed", icon: CheckCircle2 },
  FAILED: { color: "text-destructive bg-destructive/10 border-destructive/30", label: "Failed", icon: AlertTriangle },
  CANCELLED: { color: "text-muted-foreground bg-muted-foreground/10 border-border/30", label: "Cancelled", icon: Square },
}

function LiveDuration({ startedAt }: { startedAt: string }) {
  const [elapsed, setElapsed] = useState("")

  useEffect(() => {
    function update() {
      const start = new Date(startedAt).getTime()
      const diff = Math.floor((Date.now() - start) / 1000)
      if (diff < 60) setElapsed(`${diff}s`)
      else if (diff < 3600) setElapsed(`${Math.floor(diff / 60)}m ${diff % 60}s`)
      else setElapsed(`${Math.floor(diff / 3600)}h ${Math.floor((diff % 3600) / 60)}m`)
    }
    update()
    const interval = setInterval(update, 1000)
    return () => clearInterval(interval)
  }, [startedAt])

  return <span className="font-mono tabular-nums">{elapsed}</span>
}

export function MissionControlBar({ mission, workspaceId, onMissionChanged }: MissionControlBarProps) {
  const [loading, setLoading] = useState<string | null>(null)
  const scopeRef = useRef<{ workspaceId: string; crewId: string; missionId: string; controller: AbortController } | null>(null)
  const hasIdentity = Boolean(workspaceId && mission.crew_id && mission.id)
  useEffect(() => {
    const scope = { workspaceId, crewId: mission.crew_id, missionId: mission.id, controller: new AbortController() }
    scopeRef.current = scope
    setLoading(null)
    return () => { scope.controller.abort(); if (scopeRef.current === scope) scopeRef.current = null }
  }, [workspaceId, mission.crew_id, mission.id])
  const cfg = statusConfig[mission.status] || statusConfig.PLANNING
  const StatusIcon = cfg.icon

  const { completed, failed, inProgress, total, progress, totalTokens, totalCost, earliestStart } = useMemo(() => {
    const tasks = mission.tasks || []
    let comp = 0, fail = 0, prog = 0, tokens = 0, cost = 0
    let earliest: string | undefined
    for (const t of tasks) {
      if (t.status === "COMPLETED") comp++
      else if (t.status === "FAILED") fail++
      else if (t.status === "IN_PROGRESS") prog++
      tokens += t.token_count || 0
      cost += t.estimated_cost || 0
      if (t.started_at && (!earliest || t.started_at < earliest)) earliest = t.started_at
    }
    return {
      completed: comp, failed: fail, inProgress: prog,
      total: tasks.length, progress: tasks.length > 0 ? (comp / tasks.length) * 100 : 0,
      totalTokens: tokens, totalCost: cost, earliestStart: earliest,
    }
  }, [mission.tasks])

  const handleAction = useCallback(async (action: "start" | "cancel" | "complete" | "restart" | "clone") => {
    const scope = scopeRef.current
    if (!scope || !hasIdentity || loading !== null || scope.controller.signal.aborted) return
    const isCurrent = () => scopeRef.current === scope && !scope.controller.signal.aborted
    setLoading(action)
    try {
      const base = `/api/v1/crews/${encodeURIComponent(scope.crewId)}/missions/${encodeURIComponent(scope.missionId)}`
      const qs = `?workspace_id=${encodeURIComponent(scope.workspaceId)}`
      const changesStatus = action === "cancel" || action === "complete"
      const res = await apiFetch(`${base}${changesStatus ? "" : `/${action}`}${qs}`, {
        method: changesStatus ? "PATCH" : "POST",
        ...(changesStatus ? {
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify({ status: action === "cancel" ? "CANCELLED" : "DONE" }),
        } : {}),
        signal: scope.controller.signal,
      })
      if (!isCurrent()) return
      if (!res.ok) {
        const body = await res.json().catch(() => null)
        if (isCurrent()) toast.error(body?.detail ?? `Failed to ${action} mission`)
        return
      }
      const messages = {
        start: "Mission started", cancel: "Mission cancelled", complete: "Mission completed",
        restart: "Mission reset to Planning — incomplete tasks requeued",
        clone: "Mission cloned — select it from the dropdown",
      }
      toast.success(messages[action])
      onMissionChanged()
    } catch {
      if (isCurrent()) toast.error(`Failed to ${action} mission`)
    } finally {
      if (isCurrent()) setLoading(null)
    }
  }, [hasIdentity, loading, onMissionChanged])

  return (
    <div className="border-b border-border bg-card px-4 py-3">
      <div className="flex items-center justify-between gap-4">
        {/* Left: mission info */}
        <div className="flex items-center gap-3 min-w-0">
          <Badge variant="outline" className={cn("gap-1.5 shrink-0", cfg.color)}>
            <StatusIcon className={cn("h-3.5 w-3.5", mission.status === "IN_PROGRESS" && "animate-spin")} />
            {cfg.label}
          </Badge>
          <div className="min-w-0">
            <h3 className="text-sm font-semibold text-foreground truncate">{mission.title}</h3>
            <p className="text-xs text-muted-foreground">Lead: @{mission.lead_agent_slug}</p>
          </div>
        </div>

        {/* Center: progress + metrics */}
        <div className="flex items-center gap-6 shrink-0">
          {/* Task progress */}
          <div className="flex items-center gap-2">
            <div className="w-32">
              <Progress value={progress} className="h-2" />
            </div>
            <span className="text-xs text-muted-foreground font-mono whitespace-nowrap">
              {completed}/{total}
              {failed > 0 && <span className="text-destructive ml-1">({failed} failed)</span>}
              {inProgress > 0 && <span className="text-primary ml-1">({inProgress} running)</span>}
            </span>
          </div>

          {/* Duration */}
          {earliestStart && (mission.status === "IN_PROGRESS" || mission.status === "REVIEW") && (
            <div className="flex items-center gap-1.5 text-xs text-muted-foreground">
              <Clock className="h-3.5 w-3.5" />
              <LiveDuration startedAt={earliestStart} />
            </div>
          )}

          {/* Tokens/Cost */}
          {totalTokens > 0 && (
            <div className="flex items-center gap-1.5 text-xs text-muted-foreground">
              <Coins className="h-3.5 w-3.5" />
              <span className="font-mono">
                {totalTokens >= 1000 ? `${(totalTokens / 1000).toFixed(1)}k` : totalTokens} tok
                {totalCost > 0 && ` · $${totalCost.toFixed(4)}`}
              </span>
            </div>
          )}
        </div>

        {/* Right: actions */}
        <div className="flex items-center gap-2 shrink-0">
          {mission.status === "PLANNING" && (
            <Button
              size="sm"
              onClick={() => handleAction("start")}
              disabled={!hasIdentity || loading !== null || total === 0}
              className="gap-1.5 bg-primary hover:bg-primary/90"
            >
              {loading === "start" ? <Spinner className="h-3.5 w-3.5" /> : <Play className="h-3.5 w-3.5" />}
              Start Mission
            </Button>
          )}
          {mission.status === "REVIEW" && (
            <Button
              size="sm"
              variant="outline"
              onClick={() => handleAction("complete")}
              disabled={!hasIdentity || loading !== null}
              className="gap-1.5 border-success/30 text-success hover:bg-success/10"
            >
              {loading === "complete" ? <Spinner className="h-3.5 w-3.5" /> : <CheckCircle2 className="h-3.5 w-3.5" />}
              Complete
            </Button>
          )}
          {(mission.status === "PLANNING" || mission.status === "IN_PROGRESS") && (
            <Button
              size="sm"
              variant="outline"
              onClick={() => handleAction("cancel")}
              disabled={!hasIdentity || loading !== null}
              className="gap-1.5 border-destructive/30 text-destructive hover:bg-destructive/10"
            >
              {loading === "cancel" ? <Spinner className="h-3.5 w-3.5" /> : <Square className="h-3.5 w-3.5" />}
              Cancel
            </Button>
          )}
          {/* Restart — available for finished/failed missions */}
          {(mission.status === "DONE" || mission.status === "COMPLETED" || mission.status === "FAILED" || mission.status === "CANCELLED" || mission.status === "REVIEW") && (
            <Button
              size="sm"
              variant="outline"
              onClick={() => handleAction("restart")}
              disabled={!hasIdentity || loading !== null}
              className="gap-1.5 border-warn/30 text-warn hover:bg-warn/10"
            >
              {loading === "restart" ? <Spinner className="h-3.5 w-3.5" /> : <RotateCcw className="h-3.5 w-3.5" />}
              Restart
            </Button>
          )}
          {/* Clone — always available */}
          <Button
            size="sm"
            variant="outline"
            onClick={() => handleAction("clone")}
            disabled={!hasIdentity || loading !== null}
            className="gap-1.5 border-border text-muted-foreground hover:bg-accent/50"
          >
            {loading === "clone" ? <Spinner className="h-3.5 w-3.5" /> : <Copy className="h-3.5 w-3.5" />}
            Clone
          </Button>
        </div>
      </div>
    </div>
  )
}
