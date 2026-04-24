"use client"

import Link from "next/link"
import { ExternalLink, MessageSquare } from "lucide-react"
import {
  Sheet,
  SheetContent,
  SheetDescription,
  SheetHeader,
  SheetTitle,
} from "@/components/ui/sheet"
import { Button } from "@/components/ui/button"

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
 * Phase 1 stub. Phase 3 will replace the body with the full ChatPanel
 * component inlined here so chatting no longer requires navigating away
 * from /crews. For now we link to the existing dedicated chat page so
 * deep-links via `?drawer=chat` are never dead.
 */
export function ChatDrawer({ agent, open, onOpenChange }: ChatDrawerProps) {
  return (
    <Sheet open={open} onOpenChange={onOpenChange}>
      <SheetContent side="right" className="w-full sm:max-w-md flex flex-col">
        <SheetHeader>
          <SheetTitle className="flex items-center gap-2">
            <MessageSquare className="h-4 w-4" />
            Chat {agent ? `with ${agent.name}` : ""}
          </SheetTitle>
          <SheetDescription>
            Chat drawer — the full conversation view will live here in the next phase.
          </SheetDescription>
        </SheetHeader>
        <div className="flex-1 flex items-center justify-center p-6 text-center text-micro text-muted-foreground">
          <div className="space-y-3">
            <p>Inline chat is landing in Phase 3.</p>
            {agent && (
              <Button variant="outline" size="sm" className="gap-1.5" asChild>
                <Link href={`/crews/agents/${agent.id}/chat`}>
                  Open full chat page
                  <ExternalLink className="h-3 w-3" />
                </Link>
              </Button>
            )}
          </div>
        </div>
      </SheetContent>
    </Sheet>
  )
}
