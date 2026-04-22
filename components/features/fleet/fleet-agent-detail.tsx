"use client"

import { useEffect, useState } from "react"
import {
  X, MessageSquare, ScrollText, Settings, Cpu, Key, Clock,
  ExternalLink,
} from "lucide-react"
import { motion } from "motion/react"
import { Button } from "@/components/ui/button"
import { Badge } from "@/components/ui/badge"
import { cn } from "@/lib/utils"
import { getAgentAvatarUrl } from "@/lib/agent-avatar"
import { timeAgo, formatDuration } from "@/lib/time"
import Link from "next/link"

interface AgentDetail {
  id: string
  name: string
  slug: string
  status: string
  role_title: string | null
  agent_role: string
  llm_provider: string
  llm_model: string
  cli_adapter: string
  description: string | null
  avatar_seed?: string | null
  avatar_style?: string | null
  crew?: { name: string; slug: string; color: string | null; avatar_style?: string | null } | null
  crew_id?: string | null
  _count?: { skills: number; credentials: number; chats: number }
  last_active_at?: string | null
}

interface RunData {
  id: string
  status: string
  created_at: string
  started_at: string | null
  ended_at: string | null
}

const STATUS_CONFIG: Record<string, { label: string; className: string; badgeClass: string; pulse?: boolean }> = {
  IDLE: {
    label: "Idle",
    className: "text-muted-foreground",
    badgeClass: "bg-muted text-muted-foreground",
  },
  RUNNING: {
    label: "Running",
    className: "text-emerald-400",
    badgeClass: "bg-emerald-50 text-emerald-700 dark:bg-emerald-950 dark:text-emerald-400",
    pulse: true,
  },
  ERROR: {
    label: "Error",
    className: "text-red-400",
    badgeClass: "bg-red-50 text-red-700 dark:bg-red-950 dark:text-red-400",
  },
  STOPPED: {
    label: "Stopped",
    className: "text-amber-400",
    badgeClass: "bg-amber-50 text-amber-700 dark:bg-amber-950 dark:text-amber-400",
  },
}

const RUN_STATUS_COLOR: Record<string, string> = {
  completed: "text-green-400",
  failed: "text-red-400",
  running: "text-blue-400",
  stopped: "text-amber-400",
}

export interface FleetAgentDetailProps {
  agent: AgentDetail
  workspaceId: string
  onClose: () => void
}

export function FleetAgentDetail({ agent, workspaceId, onClose }: FleetAgentDetailProps) {
  const status = STATUS_CONFIG[agent.status] || STATUS_CONFIG.IDLE
  const [runs, setRuns] = useState<RunData[]>([])
  const skillCount = agent._count?.skills ?? 0
  const credCount = agent._count?.credentials ?? 0

  // Fetch recent runs (abort stale requests on agent switch)
  useEffect(() => {
    if (!workspaceId) return
    const controller = new AbortController()
    fetch(`/api/v1/agents/${agent.id}/chats?workspace_id=${workspaceId}&limit=5`, { signal: controller.signal })
      .then((r) => r.ok ? r.json() : [])
      .then((data: RunData[]) => setRuns(data))
      .catch(() => {})
    return () => controller.abort()
  }, [agent.id, workspaceId])

  return (
    <motion.div
      key={agent.id}
      initial={{ opacity: 0, x: 12 }}
      animate={{ opacity: 1, x: 0 }}
      exit={{ opacity: 0, x: 12 }}
      transition={{ duration: 0.15, ease: "easeOut" }}
      className="h-full border-l border-white/[0.1] bg-card flex flex-col"
    >
      {/* Header */}
      <div className="flex items-start gap-3 p-4 border-b border-border shrink-0">
        <img
          src={getAgentAvatarUrl(agent.avatar_seed || agent.name, agent.avatar_style || agent.crew?.avatar_style)}
          alt=""
          className="h-12 w-12 rounded-xl shrink-0"
        />
        <div className="flex-1 min-w-0">
          <h2 className="text-[15px] font-semibold truncate">{agent.name}</h2>
          <p className="text-[12px] text-muted-foreground">{agent.role_title || agent.agent_role}</p>
          <Badge variant="secondary" className={cn("text-[10px] mt-1 gap-1.5", status.badgeClass)}>
            {status.pulse && (
              <span className="relative flex h-1.5 w-1.5">
                <span className="animate-ping absolute inline-flex h-full w-full rounded-full bg-emerald-400 opacity-75" />
                <span className="relative inline-flex rounded-full h-1.5 w-1.5 bg-emerald-500" />
              </span>
            )}
            {status.label}
          </Badge>
        </div>
        <Button variant="ghost" size="icon-xs" className="text-muted-foreground" onClick={onClose} aria-label="Close agent detail">
          <X className="h-4 w-4" />
        </Button>
      </div>

      {/* Content — essentials only. Detailed view lives in the center pane
          (FleetAgentInline) when an agent is selected. */}
      <div className="flex-1 overflow-y-auto p-4 space-y-4">
        {/* Quick actions */}
        <div className="grid grid-cols-3 gap-2">
          <Button size="sm" className="h-8 text-[11px] gap-1" asChild>
            <Link href={`/fleet/agents/${agent.id}/chat`}>
              <MessageSquare className="h-3 w-3" />
              Chat
            </Link>
          </Button>
          <Button variant="outline" size="sm" className="h-8 text-[11px] gap-1" asChild>
            <Link href={`/fleet/agents/${agent.id}/logs`}>
              <ScrollText className="h-3 w-3" />
              Logs
            </Link>
          </Button>
          <Button variant="outline" size="sm" className="h-8 text-[11px] gap-1" asChild>
            <Link href={`/fleet/agents/${agent.id}/settings`}>
              <Settings className="h-3 w-3" />
              Settings
            </Link>
          </Button>
        </div>

        {/* Stats */}
        <div className="grid grid-cols-3 gap-2">
          <StatCard icon={Cpu} label="Skills" value={skillCount} />
          <StatCard icon={Key} label="Keys" value={credCount} />
          <StatCard icon={Clock} label="Activity" value={agent.last_active_at ? timeAgo(agent.last_active_at) : "—"} />
        </div>

        {/* Recent runs */}
        {runs.length > 0 && (
          <div>
            <h3 className="text-[11px] font-semibold text-muted-foreground uppercase tracking-wider mb-2">
              Recent Runs
            </h3>
            <div className="space-y-1">
              {runs.map((run) => (
                <Link
                  key={run.id}
                  href={`/fleet/agents/${agent.id}/runs`}
                  className="flex items-center gap-2 px-2 py-1.5 rounded-md hover:bg-white/[0.04] transition-colors"
                >
                  <span className={cn("text-[10px] font-mono", RUN_STATUS_COLOR[run.status] || "text-muted-foreground")}>
                    {run.status}
                  </span>
                  <span className="flex-1" />
                  {run.started_at && run.ended_at && (
                    <span className="text-[10px] text-muted-foreground/50 tabular-nums">
                      {formatDuration(new Date(run.ended_at).getTime() - new Date(run.started_at).getTime())}
                    </span>
                  )}
                  <span className="text-[10px] text-muted-foreground/50">
                    {timeAgo(run.created_at)}
                  </span>
                </Link>
              ))}
            </div>
          </div>
        )}

        {/* Open full page link */}
        <div className="pt-2">
          <Button variant="ghost" size="sm" className="h-7 text-[11px] text-muted-foreground w-full justify-center gap-1.5" asChild>
            <Link href={`/fleet/agents/${agent.id}`}>
              Open full agent page
              <ExternalLink className="h-3 w-3" />
            </Link>
          </Button>
        </div>
      </div>
    </motion.div>
  )
}

function StatCard({ icon: Icon, label, value }: { icon: React.ElementType; label: string; value: string | number }) {
  return (
    <div className="rounded-md bg-white/[0.03] border border-white/[0.06] px-2.5 py-2 text-center">
      <Icon className="h-3.5 w-3.5 text-muted-foreground/50 mx-auto mb-1" />
      <p className="text-[13px] font-semibold tabular-nums">{value}</p>
      <p className="text-[10px] text-muted-foreground/50">{label}</p>
    </div>
  )
}
