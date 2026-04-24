"use client"

import { ScrollText } from "lucide-react"
import {
  Sheet,
  SheetContent,
  SheetHeader,
  SheetTitle,
} from "@/components/ui/sheet"
import { LogsPageClient } from "@/components/features/agents/logs/logs-page-client"
import { AgentDetailProvider } from "@/hooks/use-agent-detail"

interface AgentBrief {
  id: string
  name: string
}

export interface LogsDrawerProps {
  agent: AgentBrief | null
  open: boolean
  onOpenChange: (open: boolean) => void
}

/**
 * LogsPageClient reads agentId via useAgentId(), which falls back to
 * the AgentDetailProvider in scope. The wrapper provides that scope so
 * LogsViewer doesn't need the /crews/agents/[agentId]/logs route tree
 * to function.
 */
export function LogsDrawer({ agent, open, onOpenChange }: LogsDrawerProps) {
  return (
    <Sheet open={open} onOpenChange={onOpenChange}>
      <SheetContent
        side="right"
        className="w-full sm:max-w-3xl p-0 flex flex-col"
        showCloseButton={false}
      >
        <SheetHeader className="px-4 py-3 border-b border-border shrink-0">
          <SheetTitle className="flex items-center gap-2 text-label">
            <ScrollText className="h-4 w-4" />
            Logs {agent ? `— ${agent.name}` : ""}
          </SheetTitle>
        </SheetHeader>
        <div className="flex-1 min-h-0 overflow-hidden">
          {agent ? (
            <AgentDetailProvider agentId={agent.id}>
              <LogsPageClient />
            </AgentDetailProvider>
          ) : (
            <div className="flex-1 flex items-center justify-center p-6 text-micro text-muted-foreground">
              Select an agent to view logs.
            </div>
          )}
        </div>
      </SheetContent>
    </Sheet>
  )
}
