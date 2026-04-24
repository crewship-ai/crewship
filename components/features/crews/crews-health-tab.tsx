"use client"

import { useEffect, useState } from "react"
import {
  Brain, CalendarClock, Crown, HeartPulse, Server,
  CheckCircle2, XCircle, Loader2, Activity as ActivityIcon,
} from "lucide-react"
import { Card, CardContent } from "@/components/ui/card"
import { StatusBadge } from "@/components/ui/status-badge"
import { cn } from "@/lib/utils"
import { HealthOverview } from "@/components/features/crews/crews-health-overview"
import { useTick } from "@/hooks/use-tick"

interface CrewData {
  id: string
  name: string
  slug: string
  color: string | null
}

interface AgentData {
  id: string
  name: string
  slug: string
  status: string
  agent_role: string
  crew_id: string | null
  crew?: { name: string } | null
}

export interface CrewsHealthTabProps {
  workspaceId: string
  crews: CrewData[]
  agents: AgentData[]
  selectedAgent: AgentData | null
  selectedCrew: CrewData | null
}

/**
 * Context-aware Health view. Agent scope pulls container debug + schedule
 * from the agent detail + memory health scoped to the agent's crew.
 * Crew scope shows memory health for that crew plus member status
 * roll-up. Workspace scope keeps the pre-existing HealthOverview grid.
 *
 * Memory health and debug are read through the already-shipped
 * `/api/v1/memory/health` and `/api/v1/agents/{id}/debug` endpoints —
 * no new Go handler is needed for Phase 6.
 */
export function CrewsHealthTab({
  workspaceId,
  crews,
  agents,
  selectedAgent,
  selectedCrew,
}: CrewsHealthTabProps) {
  if (selectedAgent) {
    return <AgentHealth agent={selectedAgent} workspaceId={workspaceId} />
  }
  if (selectedCrew) {
    const members = agents.filter((a) => a.crew_id === selectedCrew.id)
    return <CrewHealth crew={selectedCrew} members={members} workspaceId={workspaceId} />
  }
  return <HealthOverview crews={crews} agents={agents} />
}

// --- Agent health -----------------------------------------------------------

interface AgentDetailExtra {
  memory_enabled: boolean
  lead_mode: string | null
  schedule_enabled: boolean | null
  schedule_cron: string | null
  schedule_next_run: string | null
  schedule_last_run: string | null
}

interface DebugSnapshot {
  db_status: string
  crewshipd_reachable: boolean
  runtime?: { status?: string } | null
}

function AgentHealth({ agent, workspaceId }: { agent: AgentData; workspaceId: string }) {
  const [detail, setDetail] = useState<AgentDetailExtra | null>(null)
  const [debug, setDebug] = useState<DebugSnapshot | null>(null)
  const nowTick = useTick(60_000)

  useEffect(() => {
    if (!workspaceId) return
    const controller = new AbortController()
    fetch(`/api/v1/agents/${agent.id}?workspace_id=${workspaceId}`, { signal: controller.signal })
      .then((r) => (r.ok ? r.json() : null))
      .then((d: AgentDetailExtra | null) => {
        if (controller.signal.aborted || !d) return
        setDetail({
          memory_enabled: Boolean(d.memory_enabled),
          lead_mode: d.lead_mode ?? null,
          schedule_enabled: d.schedule_enabled ?? null,
          schedule_cron: d.schedule_cron ?? null,
          schedule_next_run: d.schedule_next_run ?? null,
          schedule_last_run: d.schedule_last_run ?? null,
        })
      })
      .catch((err) => {
        if ((err as { name?: string })?.name === "AbortError") return
      })
    return () => controller.abort()
  }, [agent.id, workspaceId])

  useEffect(() => {
    if (!workspaceId) return
    const controller = new AbortController()
    fetch(`/api/v1/agents/${agent.id}/debug?workspace_id=${workspaceId}`, { signal: controller.signal })
      .then((r) => (r.ok ? r.json() : null))
      .then((d: DebugSnapshot | null) => {
        if (controller.signal.aborted || !d) return
        setDebug(d)
      })
      .catch((err) => {
        if ((err as { name?: string })?.name === "AbortError") return
      })
    return () => controller.abort()
  }, [agent.id, workspaceId])

  const scheduleCountdown = detail?.schedule_enabled && detail.schedule_next_run
    ? formatCountdown(new Date(detail.schedule_next_run).getTime() - nowTick)
    : null
  const isLead = agent.agent_role === "LEAD"

  return (
    <div className="space-y-4">
      <SectionHeader icon={HeartPulse} title={`Health — ${agent.name}`} />

      {/* Container + runtime */}
      <Card>
        <CardContent className="p-4 space-y-3">
          <h3 className="text-label font-semibold uppercase tracking-wider text-muted-foreground flex items-center gap-2">
            <Server className="h-3.5 w-3.5" />
            Runtime
          </h3>
          <div className="grid grid-cols-1 sm:grid-cols-3 gap-3">
            <MetricRow
              label="Status"
              value={<StatusBadge status={mapAgentStatus(agent.status)} label={agent.status.toLowerCase()} />}
            />
            <MetricRow
              label="Sidecar"
              value={
                debug === null ? (
                  <span className="inline-flex items-center gap-1 text-micro text-muted-foreground">
                    <Loader2 className="h-3 w-3 animate-spin" />
                    checking
                  </span>
                ) : debug.crewshipd_reachable ? (
                  <span className="inline-flex items-center gap-1 text-micro text-emerald-400">
                    <CheckCircle2 className="h-3 w-3" />
                    reachable
                  </span>
                ) : (
                  <span className="inline-flex items-center gap-1 text-micro text-red-400">
                    <XCircle className="h-3 w-3" />
                    unreachable
                  </span>
                )
              }
            />
            <MetricRow
              label="Runtime"
              value={
                debug?.runtime?.status ? (
                  <span className="text-micro tabular-nums">{debug.runtime.status}</span>
                ) : (
                  <span className="text-micro text-muted-foreground">—</span>
                )
              }
            />
          </div>
        </CardContent>
      </Card>

      {/* Status chips */}
      <Card>
        <CardContent className="p-4 space-y-3">
          <h3 className="text-label font-semibold uppercase tracking-wider text-muted-foreground flex items-center gap-2">
            <ActivityIcon className="h-3.5 w-3.5" />
            Agent state
          </h3>
          <div className="space-y-1.5">
            {isLead && (
              <StatusChip
                icon={Crown}
                label="Lead mode"
                value={detail?.lead_mode === "passive" ? "passive" : "active"}
                tone={detail?.lead_mode === "passive" ? "muted" : "emerald"}
              />
            )}
            <StatusChip
              icon={Brain}
              label="Memory"
              value={detail?.memory_enabled ? "on" : "off"}
              tone={detail?.memory_enabled ? "emerald" : "muted"}
            />
            {scheduleCountdown && (
              <StatusChip
                icon={CalendarClock}
                label="Schedule"
                value={`next: ${scheduleCountdown}`}
                tone="primary"
              />
            )}
          </div>
        </CardContent>
      </Card>

      {/* Memory health for the agent's crew. Agents don't have their own
          memory health record — metrics are crew-scoped because memory
          is shared across crew members. */}
      {agent.crew_id && (
        <MemoryHealthCard crewId={agent.crew_id} workspaceId={workspaceId} />
      )}
    </div>
  )
}

// --- Crew health ------------------------------------------------------------

function CrewHealth({
  crew, members, workspaceId,
}: {
  crew: CrewData
  members: AgentData[]
  workspaceId: string
}) {
  const statusCounts = members.reduce<Record<string, number>>(
    (acc, a) => {
      const key = a.status || "IDLE"
      acc[key] = (acc[key] ?? 0) + 1
      return acc
    },
    {},
  )
  return (
    <div className="space-y-4">
      <SectionHeader icon={HeartPulse} title={`Health — ${crew.name}`} />

      <Card>
        <CardContent className="p-4 space-y-3">
          <h3 className="text-label font-semibold uppercase tracking-wider text-muted-foreground flex items-center gap-2">
            <ActivityIcon className="h-3.5 w-3.5" />
            Members ({members.length})
          </h3>
          <div className="grid grid-cols-2 sm:grid-cols-4 gap-2">
            {(["RUNNING", "IDLE", "ERROR", "STOPPED"] as const).map((status) => (
              <div
                key={status}
                className="rounded-md bg-card/50 border border-border px-3 py-2 text-center"
              >
                <p
                  className={cn(
                    "text-title font-bold tabular-nums",
                    status === "RUNNING" && "text-emerald-400",
                    status === "ERROR" && "text-red-400",
                    status === "STOPPED" && "text-amber-400",
                    status === "IDLE" && "text-muted-foreground",
                  )}
                >
                  {statusCounts[status] ?? 0}
                </p>
                <p className="text-micro text-muted-foreground mt-0.5">{status.toLowerCase()}</p>
              </div>
            ))}
          </div>
        </CardContent>
      </Card>

      <MemoryHealthCard crewId={crew.id} workspaceId={workspaceId} />
    </div>
  )
}

// --- Memory health card (reused by agent + crew variants) ------------------

interface MemoryHealth {
  overall: number
  metrics: {
    freshness: number
    coverage: number
    coherence: number
    efficiency: number
    reachability: number
  }
}

function MemoryHealthCard({ crewId, workspaceId }: { crewId: string; workspaceId: string }) {
  const [health, setHealth] = useState<MemoryHealth | null>(null)
  const [loading, setLoading] = useState(true)

  useEffect(() => {
    if (!workspaceId) return
    setLoading(true)
    const controller = new AbortController()
    fetch(`/api/v1/memory/health?workspace_id=${workspaceId}&crew_id=${crewId}`, {
      signal: controller.signal,
    })
      .then((r) => (r.ok ? r.json() : null))
      .then((d: MemoryHealth | null) => {
        if (controller.signal.aborted) return
        setHealth(d)
        setLoading(false)
      })
      .catch((err) => {
        if ((err as { name?: string })?.name === "AbortError") return
        setLoading(false)
      })
    return () => controller.abort()
  }, [crewId, workspaceId])

  return (
    <Card>
      <CardContent className="p-4 space-y-3">
        <div className="flex items-center justify-between gap-2">
          <h3 className="text-label font-semibold uppercase tracking-wider text-muted-foreground flex items-center gap-2">
            <Brain className="h-3.5 w-3.5" />
            Memory health
          </h3>
          {health && (
            <span
              className={cn(
                "text-micro font-semibold tabular-nums px-2 py-0.5 rounded-full",
                health.overall >= 0.75 && "bg-emerald-500/15 text-emerald-400",
                health.overall >= 0.5 && health.overall < 0.75 && "bg-amber-500/15 text-amber-400",
                health.overall < 0.5 && "bg-red-500/15 text-red-400",
              )}
            >
              {Math.round(health.overall * 100)}%
            </span>
          )}
        </div>
        {loading ? (
          <div className="flex items-center gap-2 text-micro text-muted-foreground">
            <Loader2 className="h-3 w-3 animate-spin" />
            computing…
          </div>
        ) : health ? (
          <div className="grid grid-cols-1 sm:grid-cols-2 gap-2">
            <MemoryBar label="Freshness" value={health.metrics.freshness} />
            <MemoryBar label="Coverage" value={health.metrics.coverage} />
            <MemoryBar label="Coherence" value={health.metrics.coherence} />
            <MemoryBar label="Efficiency" value={health.metrics.efficiency} />
            <MemoryBar label="Reachability" value={health.metrics.reachability} />
          </div>
        ) : (
          <p className="text-micro text-muted-foreground">
            Memory health unavailable.
          </p>
        )}
      </CardContent>
    </Card>
  )
}

function MemoryBar({ label, value }: { label: string; value: number }) {
  const pct = Math.max(0, Math.min(1, value))
  return (
    <div className="flex items-center gap-2">
      <span className="text-micro text-muted-foreground uppercase tracking-wider w-24 shrink-0">
        {label}
      </span>
      <div
        role="progressbar"
        aria-valuenow={Math.round(pct * 100)}
        aria-valuemin={0}
        aria-valuemax={100}
        aria-label={`${label} ${Math.round(pct * 100)}%`}
        className="flex-1 h-1.5 bg-white/[0.06] rounded-full overflow-hidden"
      >
        <div
          className={cn(
            "h-full rounded-full transition-all",
            pct >= 0.75 && "bg-emerald-400",
            pct >= 0.5 && pct < 0.75 && "bg-amber-400",
            pct < 0.5 && "bg-red-400",
          )}
          // Dynamic width — Tailwind can't express runtime percentages.
          style={{ width: `${Math.round(pct * 100)}%` }}
        />
      </div>
      <span className="text-micro text-muted-foreground/70 tabular-nums w-10 text-right shrink-0">
        {Math.round(pct * 100)}%
      </span>
    </div>
  )
}

// --- small helpers ----------------------------------------------------------

function SectionHeader({
  icon: Icon, title,
}: {
  icon: React.ElementType
  title: string
}) {
  return (
    <div className="flex items-center gap-2">
      <Icon className="h-4 w-4 text-muted-foreground" />
      <h2 className="text-default font-semibold">{title}</h2>
    </div>
  )
}

function MetricRow({ label, value }: { label: string; value: React.ReactNode }) {
  return (
    <div className="space-y-0.5">
      <p className="text-micro text-muted-foreground uppercase tracking-wider">{label}</p>
      <div className="text-body">{value}</div>
    </div>
  )
}

function StatusChip({
  icon: Icon, label, value, tone,
}: {
  icon: React.ElementType
  label: string
  value: string
  tone: "emerald" | "muted" | "primary"
}) {
  const toneClass = tone === "emerald"
    ? "text-emerald-400 bg-emerald-500/10"
    : tone === "primary"
      ? "text-primary bg-primary/10"
      : "text-muted-foreground bg-muted/40"
  return (
    <div className="flex items-center gap-2 text-micro">
      <span className={cn("flex items-center justify-center h-5 w-5 rounded-full shrink-0", toneClass)}>
        <Icon className="h-3 w-3" aria-hidden="true" />
      </span>
      <span className="text-muted-foreground uppercase tracking-wider w-24 shrink-0">{label}</span>
      <span className="font-medium text-foreground/85 tabular-nums">{value}</span>
    </div>
  )
}

function mapAgentStatus(status: string | undefined): string {
  switch (status) {
    case "RUNNING": return "IN_PROGRESS"
    case "ERROR": return "FAILED"
    case "STOPPED": return "CANCELLED"
    default: return "PENDING"
  }
}

function formatCountdown(ms: number): string {
  if (ms <= 0) return "imminent"
  const mins = Math.floor(ms / 60_000)
  if (mins < 60) return `${mins}m`
  const hrs = Math.floor(mins / 60)
  if (hrs < 24) return `${hrs}h ${mins % 60}m`
  const days = Math.floor(hrs / 24)
  return `${days}d ${hrs % 24}h`
}
