"use client"

import { useAgentDetail } from "@/hooks/use-agent-detail"
import { Loader2, AlertTriangle } from "lucide-react"
import dynamic from "next/dynamic"

const WebTerminal = dynamic(
  () => import("@/components/features/terminal/web-terminal").then((m) => m.WebTerminal),
  { ssr: false }
)

export function TerminalPageClient() {
  const { agent, loading, error } = useAgentDetail()

  if (loading) {
    return (
      <div className="flex items-center justify-center h-[60vh]">
        <Loader2 className="h-6 w-6 animate-spin text-muted-foreground" />
      </div>
    )
  }

  if (error || !agent) {
    return (
      <div className="flex flex-col items-center justify-center h-[60vh] gap-2 text-muted-foreground">
        <AlertTriangle className="h-6 w-6" />
        <p className="text-sm">Failed to load agent</p>
      </div>
    )
  }

  if (!agent.crew_id || !agent.crew) {
    return (
      <div className="flex flex-col items-center justify-center h-[60vh] gap-2 text-muted-foreground">
        <AlertTriangle className="h-6 w-6" />
        <p className="text-sm">Terminal requires agent to be in a crew</p>
      </div>
    )
  }

  return (
    <div className="h-[calc(100vh-8rem)]">
      <WebTerminal
        crewId={agent.crew_id}
        crewSlug={agent.crew.slug}
        defaultAgentSlug={agent.slug}
      />
    </div>
  )
}
