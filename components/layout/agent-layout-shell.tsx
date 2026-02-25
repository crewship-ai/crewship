"use client"

import type { ReactNode } from "react"
import { AgentDetailProvider } from "@/hooks/use-agent-detail"
import { AgentTabs } from "@/components/layout/agent-tabs"
import { AgentHeader } from "@/components/layout/agent-header"

interface AgentLayoutShellProps {
  agentId: string
  children: ReactNode
}

export function AgentLayoutShell({ agentId, children }: AgentLayoutShellProps) {
  return (
    <AgentDetailProvider agentId={agentId}>
      <div className="flex flex-col h-full">
        <div className="bg-background border-b shrink-0">
          <AgentHeader agentId={agentId} />
          <AgentTabs agentId={agentId} />
        </div>
        <div className="flex-1 overflow-y-auto">
          {children}
        </div>
      </div>
    </AgentDetailProvider>
  )
}
