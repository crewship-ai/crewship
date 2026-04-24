"use client"

import Link from "next/link"
import { ExternalLink, ScrollText } from "lucide-react"
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

export interface LogsDrawerProps {
  agent: AgentBrief | null
  open: boolean
  onOpenChange: (open: boolean) => void
}

export function LogsDrawer({ agent, open, onOpenChange }: LogsDrawerProps) {
  return (
    <Sheet open={open} onOpenChange={onOpenChange}>
      <SheetContent side="right" className="w-full sm:max-w-md flex flex-col">
        <SheetHeader>
          <SheetTitle className="flex items-center gap-2">
            <ScrollText className="h-4 w-4" />
            Logs {agent ? `— ${agent.name}` : ""}
          </SheetTitle>
          <SheetDescription>
            Live log tail — streaming journal entries inline lands in Phase 3.
          </SheetDescription>
        </SheetHeader>
        <div className="flex-1 flex items-center justify-center p-6 text-center text-micro text-muted-foreground">
          <div className="space-y-3">
            <p>Inline log tail is landing in Phase 3.</p>
            {agent && (
              <Button variant="outline" size="sm" className="gap-1.5" asChild>
                <Link href={`/crews/agents/${agent.id}/logs`}>
                  Open full logs page
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
