"use client"

import { useCallback, useState } from "react"
import { Pause, Loader2 } from "lucide-react"
import { Button } from "@/components/ui/button"
import { Badge } from "@/components/ui/badge"
import { Skeleton } from "@/components/ui/skeleton"
import { useAgentDetail } from "@/hooks/use-agent-detail"
import { useWorkspace } from "@/hooks/use-workspace"
import { getAgentAvatarUrl } from "@/lib/agent-avatar"
import { useIsMobile } from "@/hooks/use-mobile"

const AGENT_ROLE_COLORS: Record<string, string> = {
  AGENT: "text-blue-600 border-blue-300 bg-blue-50 dark:bg-blue-950/30",
  LEAD: "text-amber-600 border-amber-300 bg-amber-50 dark:bg-amber-950/30",
  COORDINATOR: "text-purple-600 border-purple-300 bg-purple-50 dark:bg-purple-950/30",
}

const STATUS_STYLES: Record<string, { class: string; dot: string; pulse: boolean }> = {
  IDLE: { class: "bg-muted text-muted-foreground", dot: "bg-gray-400", pulse: false },
  RUNNING: { class: "bg-emerald-50 text-emerald-700 dark:bg-emerald-950 dark:text-emerald-400", dot: "bg-emerald-500", pulse: true },
  ERROR: { class: "bg-red-50 text-red-700 dark:bg-red-950 dark:text-red-400", dot: "bg-red-500", pulse: false },
  STOPPED: { class: "bg-neutral-100 text-neutral-600 dark:bg-neutral-900 dark:text-neutral-400", dot: "bg-neutral-400", pulse: false },
}

interface AgentHeaderProps {
  agentId: string
}

export function AgentHeader({ agentId }: AgentHeaderProps) {
  const { agent, loading, setAgent } = useAgentDetail()
  const { workspaceId } = useWorkspace()
  const [stopping, setStopping] = useState(false)
  const isMobile = useIsMobile()

  const handleStop = useCallback(async () => {
    if (!workspaceId || !agent || stopping) return
    setStopping(true)
    try {
      const res = await fetch(`/api/v1/agents/${agentId}/stop?workspace_id=${workspaceId}`, { method: "POST" })
      if (res.ok) {
        const data = await res.json()
        setAgent((prev) => prev ? { ...prev, status: data.status } : prev)
      }
    } catch {
      // silently fail
    } finally {
      setStopping(false)
    }
  }, [agentId, workspaceId, agent, stopping, setAgent])

  // Desktop: agent info is in the collapsible rail (AgentDesktopRail)
  if (!isMobile) return null

  if (loading || !agent) {
    return (
      <div className="flex items-center gap-4 px-5 pt-4 pb-3">
        <Skeleton className="h-8 w-8 md:h-10 md:w-10 rounded-xl" />
        <div className="space-y-2">
          <Skeleton className="h-4 w-32" />
          <Skeleton className="h-3 w-20" />
        </div>
      </div>
    )
  }

  const statusStyle = STATUS_STYLES[agent.status] ?? STATUS_STYLES.IDLE
  const isRunning = agent.status === "RUNNING"

  return (
    <div className="flex items-center gap-4 px-5 pt-4 pb-3">
      <img
        src={getAgentAvatarUrl(agent.avatar_seed || agent.name, agent.avatar_style || agent.crew?.avatar_style)}
        alt={agent.name}
        className="h-8 w-8 md:h-10 md:w-10 rounded-xl shrink-0"
      />
      <div className="min-w-0 flex-1">
        <div className="flex items-center gap-2 flex-wrap">
          <h1 className="text-default font-semibold">{agent.name}</h1>
          <Badge variant="secondary" className={`${statusStyle.class} text-micro gap-1.5`}>
            <span className={`h-1.5 w-1.5 rounded-full ${statusStyle.dot} ${statusStyle.pulse ? "animate-pulse" : ""}`} />
            {agent.status}
          </Badge>
          <Badge variant="outline" className={`${AGENT_ROLE_COLORS[agent.agent_role] ?? ""} text-micro`}>
            {agent.agent_role}
          </Badge>
        </div>
        {agent.crew && (
          <p className="text-label text-muted-foreground mt-0.5">{agent.crew.name}</p>
        )}
      </div>
      {isRunning && (
        <Button
          variant="outline"
          size="sm"
          className="text-destructive border-destructive/30 hover:bg-destructive/10 gap-1.5 shrink-0"
          onClick={handleStop}
          disabled={stopping}
        >
          {stopping ? <Loader2 className="h-3.5 w-3.5 animate-spin" /> : <Pause className="h-3.5 w-3.5" />}
          Stop
        </Button>
      )}
    </div>
  )
}
