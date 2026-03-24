"use client"

import { Bot, Users } from "lucide-react"
import { Card, CardContent } from "@/components/ui/card"

interface CrewStatsProps {
  agentCount: number
  memberCount: number
}

export function CrewStats({ agentCount, memberCount }: CrewStatsProps) {
  return (
    <div className="grid grid-cols-2 gap-4">
      <Card>
        <CardContent className="p-4">
          <div className="flex items-center gap-2 text-muted-foreground">
            <Bot className="h-4 w-4" />
            <span className="text-label">Agents</span>
          </div>
          <p className="text-2xl font-bold mt-1">{agentCount}</p>
        </CardContent>
      </Card>
      <Card>
        <CardContent className="p-4">
          <div className="flex items-center gap-2 text-muted-foreground">
            <Users className="h-4 w-4" />
            <span className="text-label">Members</span>
          </div>
          <p className="text-2xl font-bold mt-1">{memberCount}</p>
        </CardContent>
      </Card>
    </div>
  )
}
