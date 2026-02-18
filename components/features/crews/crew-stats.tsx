"use client"

import { Bot, Users, HardDrive, Cpu, Clock } from "lucide-react"
import { Card, CardContent } from "@/components/ui/card"

interface CrewStatsProps {
  agentCount: number
  memberCount: number
  memoryMb: number
  cpus: number
  ttlHours: number | null
}

export function CrewStats({ agentCount, memberCount, memoryMb, cpus, ttlHours }: CrewStatsProps) {
  return (
    <div className="space-y-4">
      <div className="grid grid-cols-2 sm:grid-cols-4 gap-4">
        <Card>
          <CardContent className="p-4">
            <div className="flex items-center gap-2 text-muted-foreground">
              <Bot className="h-4 w-4" />
              <span className="text-xs">Agents</span>
            </div>
            <p className="text-2xl font-bold mt-1">{agentCount}</p>
          </CardContent>
        </Card>
        <Card>
          <CardContent className="p-4">
            <div className="flex items-center gap-2 text-muted-foreground">
              <Users className="h-4 w-4" />
              <span className="text-xs">Members</span>
            </div>
            <p className="text-2xl font-bold mt-1">{memberCount}</p>
          </CardContent>
        </Card>
        <Card>
          <CardContent className="p-4">
            <div className="flex items-center gap-2 text-muted-foreground">
              <HardDrive className="h-4 w-4" />
              <span className="text-xs">Memory</span>
            </div>
            <p className="text-2xl font-bold mt-1">{memoryMb} MB</p>
          </CardContent>
        </Card>
        <Card>
          <CardContent className="p-4">
            <div className="flex items-center gap-2 text-muted-foreground">
              <Cpu className="h-4 w-4" />
              <span className="text-xs">CPUs</span>
            </div>
            <p className="text-2xl font-bold mt-1">{cpus}</p>
          </CardContent>
        </Card>
      </div>

      {ttlHours && (
        <div className="flex items-center gap-2 text-sm text-muted-foreground">
          <Clock className="h-4 w-4" />
          <span>Container TTL: {ttlHours}h</span>
        </div>
      )}
    </div>
  )
}
