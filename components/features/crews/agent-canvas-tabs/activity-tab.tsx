"use client"

import Link from "next/link"
import { CrewActivityFeed } from "@/components/features/crews/crew-activity-feed"

export interface ActivityTabProps {
  workspaceId: string
  agentId: string
}

export function ActivityTab({ workspaceId, agentId }: ActivityTabProps) {
  return (
    <section className="space-y-3">
      <div className="flex items-baseline justify-between">
        <h2 className="text-lg font-semibold">Activity</h2>
        <Link href={`/journal?agent_id=${encodeURIComponent(agentId)}`} className="text-xs text-blue-300 hover:underline">
          View all →
        </Link>
      </div>
      <div className="rounded-xl border border-white/8 bg-card max-h-[640px] overflow-hidden">
        <CrewActivityFeed
          workspaceId={workspaceId}
          agentId={agentId}
        />
      </div>
    </section>
  )
}
