"use client"

import { useCallback, useState } from "react"
import { Pause, Loader2 } from "lucide-react"
import { Button } from "@/components/ui/button"
import { useAgentDetail } from "@/hooks/use-agent-detail"
import { useWorkspace } from "@/hooks/use-workspace"

interface AgentHeaderProps {
  agentId: string
}

export function AgentHeader({ agentId }: AgentHeaderProps) {
  const { agent, setAgent } = useAgentDetail()
  const { workspaceId } = useWorkspace()
  const [stopping, setStopping] = useState(false)

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

  const isRunning = agent?.status === "RUNNING"

  if (!isRunning) return null

  return (
    <div className="flex h-12 items-center px-4 sm:px-6">
      <Button
        variant="outline"
        size="sm"
        className="text-destructive border-destructive/30 hover:bg-destructive/10 gap-1.5"
        onClick={handleStop}
        disabled={stopping}
      >
        {stopping ? <Loader2 className="h-3.5 w-3.5 animate-spin" /> : <Pause className="h-3.5 w-3.5" />}
        Stop
      </Button>
    </div>
  )
}
