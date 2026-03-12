"use client"

import type { ReactNode } from "react"
import { AgentDetailProvider } from "@/hooks/use-agent-detail"
import { AgentDesktopRail, AgentMobileTabsBar } from "@/components/layout/agent-tabs"
import { AgentHeader } from "@/components/layout/agent-header"

interface AgentLayoutShellProps {
  agentId: string
  children: ReactNode
}

export function AgentLayoutShell({ agentId, children }: AgentLayoutShellProps) {
  return (
    <AgentDetailProvider agentId={agentId}>
      {/* Mobile: stacked layout (header + mobile bar + content) */}
      <div className="flex flex-col h-full md:hidden">
        <div className="shrink-0">
          <AgentHeader agentId={agentId} />
          <AgentMobileTabsBar agentId={agentId} />
        </div>
        <div className="flex-1 min-h-0 relative">
          <div className="absolute inset-0 overflow-y-auto">
            {children}
          </div>
        </div>
      </div>
      {/* Desktop: rail + content side by side */}
      <div className="hidden md:flex h-full overflow-hidden">
        <AgentDesktopRail agentId={agentId} />
        <div className="flex-1 min-w-0 relative">
          <div className="absolute inset-0 overflow-y-auto">
            {children}
          </div>
        </div>
      </div>
    </AgentDetailProvider>
  )
}
