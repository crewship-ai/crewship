"use client"

import { useEffect, useState } from "react"
import Link from "next/link"
import { ExternalLink, MessageSquare } from "lucide-react"
import { nanoid } from "nanoid"
import {
  Sheet,
  SheetContent,
  SheetHeader,
  SheetTitle,
} from "@/components/ui/sheet"
import { Button } from "@/components/ui/button"
import { ChatPanel } from "@/components/features/chat/chat-panel"
import { AgentDetailProvider } from "@/hooks/use-agent-detail"

interface AgentBrief {
  id: string
  name: string
}

export interface ChatDrawerProps {
  agent: AgentBrief | null
  open: boolean
  onOpenChange: (open: boolean) => void
}

/**
 * Inline ChatPanel inside a Sheet. A fresh session id is allocated per
 * drawer-open so consecutive conversations are isolated; users who need
 * to resume a prior session go to the full chat page via the footer
 * link. Session switching is deliberately not ported here — the whole
 * point of the drawer is to chat fast without losing the Crews canvas.
 */
export function ChatDrawer({ agent, open, onOpenChange }: ChatDrawerProps) {
  const [sessionId, setSessionId] = useState<string>("")

  // Allocate a fresh session id each time the drawer opens for a given
  // agent. Without the reset, closing and re-opening for a different
  // agent would keep the previous session id — ChatPanel would show
  // empty history (wrong agent) or crash on the mismatch.
  useEffect(() => {
    if (open && agent?.id) {
      setSessionId(nanoid())
    }
  }, [open, agent?.id])

  return (
    <Sheet open={open} onOpenChange={onOpenChange}>
      <SheetContent
        side="right"
        className="w-full sm:max-w-2xl p-0 flex flex-col"
        showCloseButton={false}
      >
        <SheetHeader className="px-4 py-3 border-b border-border shrink-0">
          <SheetTitle className="flex items-center gap-2 text-label">
            <MessageSquare className="h-4 w-4" />
            Chat {agent ? `with ${agent.name}` : ""}
          </SheetTitle>
        </SheetHeader>
        <div className="flex-1 min-h-0 overflow-hidden">
          {agent && sessionId ? (
            <AgentDetailProvider agentId={agent.id}>
              <ChatPanel
                agentId={agent.id}
                sessionId={sessionId}
                agentName={agent.name}
              />
            </AgentDetailProvider>
          ) : (
            <div className="flex-1 flex items-center justify-center p-6 text-micro text-muted-foreground">
              Select an agent to chat.
            </div>
          )}
        </div>
        {agent && (
          <div className="border-t border-border px-4 py-2 shrink-0 flex items-center justify-end">
            <Button variant="ghost" size="sm" className="h-7 gap-1.5 text-micro" asChild>
              <Link href={`/crews/agents/${agent.id}/chat`}>
                Open full chat page
                <ExternalLink className="h-3 w-3" />
              </Link>
            </Button>
          </div>
        )}
      </SheetContent>
    </Sheet>
  )
}
