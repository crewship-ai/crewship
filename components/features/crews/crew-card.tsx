"use client"

import { memo } from "react"
import Link from "next/link"
import { Bot, Users } from "lucide-react"
import { Card, CardContent } from "@/components/ui/card"
import { CrewIcon } from "@/components/ui/crew-icon"

interface AgentStatusSummary {
  running: number
  error: number
  idle: number
  stopped: number
}

interface TeamCount {
  agents: number
  members: number
}

interface CrewData {
  id: string
  name: string
  slug: string
  description: string | null
  color: string | null
  icon: string | null
  created_at?: string
  _count: TeamCount
  agent_status_summary?: AgentStatusSummary | null
}

function CrewHealthIndicator({ summary }: { summary: AgentStatusSummary }) {
  const parts: string[] = []
  if (summary.running > 0) parts.push(`${summary.running} running`)
  if (summary.error > 0) parts.push(`${summary.error} error`)
  if (parts.length === 0) return null

  const hasError = summary.error > 0

  return (
    <span className={`text-micro ${hasError ? "text-red-600 dark:text-red-400" : "text-emerald-600 dark:text-emerald-400"}`}>
      {parts.join(" · ")}
    </span>
  )
}

export const CrewCard = memo(function CrewCard({ crew }: { crew: CrewData }) {
  return (
    <Link
      href={`/cruise/crews/${crew.id}`}
      className="rounded-[var(--radius)] focus-visible:ring-2 focus-visible:ring-primary focus-visible:ring-offset-2 outline-none"
    >
      <Card className="hover:border-primary/50 hover:bg-accent/30 hover:shadow-md transition-all duration-150 cursor-pointer h-full border-border/80 shadow-md">
        <CardContent className="p-4 sm:p-5">
          <div className="flex items-start gap-3">
            <CrewIcon icon={crew.icon || "briefcase"} color={crew.color} />
            <div className="flex-1 min-w-0">
              <h3 className="text-body font-semibold truncate">{crew.name}</h3>
              <p className="text-label text-muted-foreground mt-1 line-clamp-2 min-h-[2.5rem]">
                {crew.description || <span className="italic">No description</span>}
              </p>
            </div>
          </div>

          <div className="mt-3 pt-3 border-t flex items-center justify-between text-label text-muted-foreground">
            <div className="flex items-center gap-4">
              <span className="flex items-center gap-1">
                <Bot className="h-3 w-3" />
                {crew._count.agents} agents
              </span>
              <span className="flex items-center gap-1">
                <Users className="h-3 w-3" />
                {crew._count.members} members
              </span>
            </div>
            {crew.agent_status_summary && (
              <CrewHealthIndicator summary={crew.agent_status_summary} />
            )}
          </div>
        </CardContent>
      </Card>
    </Link>
  )
})
