"use client"

import { User } from "lucide-react"
import { MissionStatusBadge } from "./mission-status-badge"
import { formatCost } from "@/lib/utils/format"
import { formatDurationLong } from "@/lib/time"
import type { Mission } from "@/lib/types/mission"

interface MissionHeaderProps {
  mission: Mission
}

export function MissionHeader({ mission }: MissionHeaderProps) {
  return (
    <div className="space-y-2">
      <div className="flex items-start justify-between gap-4">
        <div className="space-y-1">
          <h1 className="text-title font-bold">{mission.title}</h1>
          {mission.description && (
            <p className="text-body text-muted-foreground">{mission.description}</p>
          )}
        </div>
        <MissionStatusBadge status={mission.status} />
      </div>

      <div className="flex items-center gap-4 text-label text-muted-foreground">
        <span className="flex items-center gap-1">
          <User className="h-3 w-3" />
          Lead: @{mission.lead_agent_slug}
        </span>
        <span>Duration: {formatDurationLong(mission.created_at, mission.completed_at)}</span>
        <span>Cost: {formatCost(mission.total_estimated_cost)}</span>
        <span className="font-mono text-[10px]">{mission.trace_id}</span>
      </div>
    </div>
  )
}
