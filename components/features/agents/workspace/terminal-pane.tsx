"use client"

import { useAgentDetail } from "@/hooks/use-agent-detail"
import { AlertTriangle, TerminalSquare } from "lucide-react"
import { Spinner } from "@/components/ui/spinner"
import dynamic from "next/dynamic"
import { EmptyState } from "@/components/layout/empty-state"

const WebTerminal = dynamic(
  () => import("@/components/features/terminal/web-terminal").then((m) => m.WebTerminal),
  { ssr: false }
)

export function TerminalPageClient() {
  const { agent, loading, error } = useAgentDetail()

  if (loading) {
    return (
      <div className="flex items-center justify-center h-[60vh]">
        <Spinner className="h-6 w-6 text-muted-foreground" />
      </div>
    )
  }

  if (error || !agent) {
    return (
      <div className="p-4 sm:p-6">
        <EmptyState
          icon={AlertTriangle}
          title="Failed to load agent"
          description="Refresh the page or check that the workspace is running."
        />
      </div>
    )
  }

  if (!agent.crew_id || !agent.crew) {
    return (
      <div className="p-4 sm:p-6">
        <EmptyState
          icon={TerminalSquare}
          title="Terminal unavailable"
          description="The terminal requires this agent to be assigned to a crew."
        />
      </div>
    )
  }

  return (
    <div className="flex flex-col h-[calc(100vh-8rem)] p-4 sm:p-6 gap-4">
      <h2 className="text-title font-semibold">Terminal</h2>
      <div className="flex-1 min-h-0 rounded-lg border border-border overflow-hidden bg-card">
        <WebTerminal
          crewId={agent.crew_id}
          crewSlug={agent.crew.slug}
          defaultAgentSlug={agent.slug}
        />
      </div>
    </div>
  )
}
