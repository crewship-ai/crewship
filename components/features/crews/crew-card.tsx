"use client"

import Link from "next/link"
import { Bot, Users } from "lucide-react"
import { Card, CardContent } from "@/components/ui/card"
import { CrewIcon } from "@/components/ui/crew-icon"

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
}

export function CrewCard({ crew }: { crew: CrewData }) {
  return (
    <Link href={`/crews/${crew.id}`}>
      <Card className="hover:border-primary/50 transition-colors cursor-pointer h-full border-border/80 shadow-md">
        <CardContent className="p-4 sm:p-5">
          <div className="flex items-start gap-3">
            <CrewIcon icon={crew.icon || "briefcase"} color={crew.color} />
            <div className="flex-1 min-w-0">
              <h3 className="text-body font-semibold truncate">{crew.name}</h3>
              {crew.description && (
                <p className="text-label text-muted-foreground mt-1 line-clamp-2">
                  {crew.description}
                </p>
              )}
            </div>
          </div>

          <div className="mt-3 pt-3 border-t flex items-center gap-4 text-label text-muted-foreground">
            <span className="flex items-center gap-1">
              <Bot className="h-3 w-3" />
              {crew._count.agents} agents
            </span>
            <span className="flex items-center gap-1">
              <Users className="h-3 w-3" />
              {crew._count.members} members
            </span>
          </div>
        </CardContent>
      </Card>
    </Link>
  )
}
