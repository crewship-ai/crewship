"use client"

import Link from "next/link"
import { Bot, Users } from "lucide-react"
import { Card, CardContent } from "@/components/ui/card"

interface TeamCount {
  agents: number
  members: number
}

interface TeamData {
  id: string
  name: string
  slug: string
  description: string | null
  color: string | null
  icon: string | null
  _count: TeamCount
}

export function TeamCard({ team }: { team: TeamData }) {
  return (
    <Link href={`/teams/${team.id}`}>
      <Card className="hover:border-primary/50 transition-colors cursor-pointer h-full">
        <CardContent className="p-4 sm:p-5">
          <div className="flex items-start gap-3">
            <div
              className="flex h-10 w-10 items-center justify-center rounded-lg text-lg shrink-0"
              style={{ backgroundColor: team.color ? `${team.color}20` : undefined }}
            >
              {team.icon ?? (
                <Users
                  className="h-5 w-5"
                  style={{ color: team.color ?? "#6b7280" }}
                />
              )}
            </div>
            <div className="flex-1 min-w-0">
              <div className="flex items-center gap-2">
                <span
                  className="h-2.5 w-2.5 rounded-full shrink-0"
                  style={{ backgroundColor: team.color ?? "#6b7280" }}
                />
                <h3 className="text-sm font-semibold truncate">{team.name}</h3>
              </div>
              {team.description && (
                <p className="text-xs text-muted-foreground mt-1 line-clamp-2">
                  {team.description}
                </p>
              )}
            </div>
          </div>

          <div className="mt-3 pt-3 border-t flex items-center gap-4 text-xs text-muted-foreground">
            <span className="flex items-center gap-1">
              <Bot className="h-3 w-3" />
              {team._count.agents} agents
            </span>
            <span className="flex items-center gap-1">
              <Users className="h-3 w-3" />
              {team._count.members} members
            </span>
          </div>
        </CardContent>
      </Card>
    </Link>
  )
}
