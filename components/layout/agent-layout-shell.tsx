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
      <div className="flex flex-col md:flex-row h-full min-h-full overflow-hidden">
        {/* Mobile: header + tabs bar */}
        <div className="shrink-0 md:hidden">
          <AgentHeader agentId={agentId} />
          <AgentMobileTabsBar agentId={agentId} />
        </div>
        {/* Desktop: side rail — h-full ensures border-r extends to bottom */}
        <div className="hidden md:flex shrink-0 h-full">
          <AgentDesktopRail agentId={agentId} />
        </div>
        {/* Content area (single render) */}
        <div className="flex-1 min-w-0 min-h-0 relative">
          <div className="absolute inset-0 overflow-y-auto">
            {children}
          </div>
        </div>
      </div>
    </AgentDetailProvider>
  )
}
