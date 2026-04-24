"use client"

import Link from "next/link"
import { ExternalLink, Settings as SettingsIcon } from "lucide-react"
import {
  Sheet,
  SheetContent,
  SheetDescription,
  SheetHeader,
  SheetTitle,
} from "@/components/ui/sheet"
import { Button } from "@/components/ui/button"

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
 * Drawer for editing either an agent or a crew. The body is a stub in
 * Phase 1; Phase 3 will inline the existing settings forms (agent
 * settings-client, crew config tabs) directly into the Sheet so the "Open
 * full" page becomes redundant.
 */
export function SettingsDrawer({ entity, open, onOpenChange }: SettingsDrawerProps) {
  const fullPath = entity
    ? entity.kind === "agent"
      ? `/crews/agents/${entity.id}/settings`
      : `/crews/${entity.id}`
    : null

  return (
    <Sheet open={open} onOpenChange={onOpenChange}>
      <SheetContent side="right" className="w-full sm:max-w-md flex flex-col">
        <SheetHeader>
          <SheetTitle className="flex items-center gap-2">
            <SettingsIcon className="h-4 w-4" />
            Settings {entity ? `— ${entity.name}` : ""}
          </SheetTitle>
          <SheetDescription>
            Inline settings form lands in Phase 3.
          </SheetDescription>
        </SheetHeader>
        <div className="flex-1 flex items-center justify-center p-6 text-center text-micro text-muted-foreground">
          <div className="space-y-3">
            <p>Inline settings editing is landing in Phase 3.</p>
            {fullPath && (
              <Button variant="outline" size="sm" className="gap-1.5" asChild>
                <Link href={fullPath}>
                  Open full settings page
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
