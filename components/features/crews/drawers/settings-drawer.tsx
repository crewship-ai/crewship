"use client"

import Link from "next/link"
import { ExternalLink, Settings as SettingsIcon } from "lucide-react"
import {
  Sheet,
  SheetContent,
  SheetHeader,
  SheetTitle,
} from "@/components/ui/sheet"
import { Button } from "@/components/ui/button"
import { SettingsPageClient } from "@/app/(dashboard)/crews/agents/[agentId]/settings/settings-client"
import { AgentDetailProvider } from "@/hooks/use-agent-detail"

interface EntityBrief {
  kind: "agent" | "crew"
  id: string
  name: string
}

export interface SettingsDrawerProps {
  entity: EntityBrief | null
  open: boolean
  onOpenChange: (open: boolean) => void
}

/**
 * Agent settings inline. The crew variant currently links to the full
 * crew page — embedding the 600-line config form here would crowd the
 * Sheet and the crew config is edited rarely enough that a link is
 * the right ratio. Refactor later if product data says otherwise.
 */
export function SettingsDrawer({ entity, open, onOpenChange }: SettingsDrawerProps) {
  const fullPath = entity
    ? entity.kind === "agent"
      ? `/crews/agents/${entity.id}/settings`
      : `/crews/${entity.id}`
    : null

  return (
    <Sheet open={open} onOpenChange={onOpenChange}>
      <SheetContent
        side="right"
        className="w-full sm:max-w-2xl p-0 flex flex-col"
        showCloseButton={false}
      >
        <SheetHeader className="px-4 py-3 border-b border-border shrink-0">
          <SheetTitle className="flex items-center gap-2 text-label">
            <SettingsIcon className="h-4 w-4" />
            Settings {entity ? `— ${entity.name}` : ""}
          </SheetTitle>
        </SheetHeader>
        <div className="flex-1 min-h-0 overflow-y-auto">
          {entity?.kind === "agent" ? (
            <AgentDetailProvider agentId={entity.id}>
              <SettingsPageClient />
            </AgentDetailProvider>
          ) : entity?.kind === "crew" ? (
            <div className="flex items-center justify-center h-full p-6 text-center text-micro text-muted-foreground">
              <div className="space-y-3">
                <p>Crew configuration lives on the full crew page for now.</p>
                {fullPath && (
                  <Button variant="outline" size="sm" className="gap-1.5" asChild>
                    <Link href={fullPath}>
                      Open crew page
                      <ExternalLink className="h-3 w-3" />
                    </Link>
                  </Button>
                )}
              </div>
            </div>
          ) : (
            <div className="flex items-center justify-center h-full p-6 text-micro text-muted-foreground">
              Select an entity to edit.
            </div>
          )}
        </div>
        {entity?.kind === "agent" && fullPath && (
          <div className="border-t border-border px-4 py-2 shrink-0 flex items-center justify-end">
            <Button variant="ghost" size="sm" className="h-7 gap-1.5 text-micro" asChild>
              <Link href={fullPath}>
                Open full settings page
                <ExternalLink className="h-3 w-3" />
              </Link>
            </Button>
          </div>
        )}
      </SheetContent>
    </Sheet>
  )
}
