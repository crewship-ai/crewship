"use client"

import { useCallback, useState } from "react"
import { Pause } from "lucide-react"
import { Spinner } from "@/components/ui/spinner"
import { Button } from "@/components/ui/button"
import { Badge } from "@/components/ui/badge"
import { StatusDot } from "@/components/ui/status-badge"
import { Skeleton } from "@/components/ui/skeleton"
import { useAgentDetail } from "@/hooks/use-agent-detail"
import { useWorkspace } from "@/hooks/use-workspace"
import { AgentAvatar } from "@/components/ui/agent-avatar"
import { useIsMobile } from "@/hooks/use-mobile"
import { cn } from "@/lib/utils"
import { apiFetch } from "@/lib/api-fetch"

// Role label tone — neutral semantic tokens, no hardcoded palette shades.
const AGENT_ROLE_TONE = "text-muted-foreground border-border bg-muted/40"

// Map agent status to canonical status identifiers used by StatusDot/StatusBadge.
// RUNNING → IN_PROGRESS, ERROR → FAILED, STOPPED/IDLE → PENDING.
function mapAgentStatus(status: string): string {
  switch (status) {
    case "RUNNING": return "IN_PROGRESS"
    case "ERROR": return "FAILED"
    case "STOPPED": return "CANCELLED"
    case "IDLE": return "PENDING"
    default: return status
  }
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
      const res = await apiFetch(`/api/v1/agents/${agentId}/stop?workspace_id=${workspaceId}`, { method: "POST" })
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

  const canonicalStatus = mapAgentStatus(agent.status)
  const isRunning = agent.status === "RUNNING"

  return (
    <div className="flex items-center gap-4 px-5 pt-4 pb-3">
      <AgentAvatar
        seed={agent.avatar_seed || agent.name}
        style={agent.avatar_style || agent.crew?.avatar_style}
        alt={agent.name}
        className="h-8 w-8 md:h-10 md:w-10 rounded-xl shrink-0"
      />
      <div className="min-w-0 flex-1">
        <div className="flex items-center gap-2 flex-wrap">
          <h1 className="text-display font-semibold truncate">{agent.name}</h1>
          <Badge variant="outline" className="gap-1.5 text-micro border-border bg-muted/40 text-muted-foreground">
            <StatusDot status={canonicalStatus} live={isRunning} className="h-1.5 w-1.5" />
            {agent.status}
          </Badge>
          <Badge variant="outline" className={cn("text-micro", AGENT_ROLE_TONE)}>
            {agent.agent_role}
          </Badge>
        </div>
        {agent.crew && (
          <p className="text-label text-muted-foreground mt-0.5 truncate">{agent.crew.name}</p>
        )}
      </div>
      {isRunning && (
        <Button
          variant="outline"
          size="sm"
          className="gap-1.5 shrink-0 text-destructive border-destructive/30 hover:bg-destructive/10"
          onClick={handleStop}
          disabled={stopping}
        >
          {stopping ? <Spinner className="h-3.5 w-3.5" /> : <Pause className="h-3.5 w-3.5" />}
          Stop
        </Button>
      )}
    </div>
  )
}
