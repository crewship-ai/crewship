"use client"

import { CrewConnections } from "@/components/features/orchestration/crew-connections"

interface ConnectionsSectionProps {
  workspaceId: string
}

export function ConnectionsSection({ workspaceId }: ConnectionsSectionProps) {
  return (
    <div className="space-y-4">
      <div className="mb-2">
        <h4 className="text-[14px] font-medium text-foreground">Crew Connections</h4>
        <p className="text-[12px] text-muted-foreground/50 mt-1">
          Connect crews to enable cross-crew task dispatch in missions.
        </p>
      </div>
      <CrewConnections workspaceId={workspaceId} />
    </div>
  )
}
