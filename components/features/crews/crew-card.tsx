"use client"

import Link from "next/link"
import { Bot, Users } from "lucide-react"
import { Card, CardContent } from "@/components/ui/card"

interface CrewData {
  id: string
  name: string
  slug: string
  description: string | null
  color: string | null
  icon: string | null
  _count_agents: number
  _count_members: number
}

export function CrewCard({ crew }: { crew: CrewData }) {
  return (
    <Link href={`/crews/${crew.id}`}>
      <Card className="hover:border-primary/50 transition-colors cursor-pointer h-full">
        <CardContent className="p-4 sm:p-5">
          <div className="flex items-start gap-3">
            <div
              className="flex h-10 w-10 items-center justify-center rounded-lg text-lg shrink-0"
              style={{ backgroundColor: crew.color ? `${crew.color}20` : undefined }}
            >
              {crew.icon ?? (
                <Users
                  className="h-5 w-5"
                  style={{ color: crew.color ?? "#6b7280" }}
                />
              )}
            </div>
            <div className="flex-1 min-w-0">
              <div className="flex items-center gap-2">
                <span
                  className="h-2.5 w-2.5 rounded-full shrink-0"
                  style={{ backgroundColor: crew.color ?? "#6b7280" }}
                />
                <h3 className="text-sm font-semibold truncate">{crew.name}</h3>
              </div>
              {crew.description && (
                <p className="text-xs text-muted-foreground mt-1 line-clamp-2">
                  {crew.description}
                </p>
              )}
            </div>
          </div>

          <div className="mt-3 pt-3 border-t flex items-center gap-4 text-xs text-muted-foreground">
            <span className="flex items-center gap-1">
              <Bot className="h-3 w-3" />
              {crew._count_agents ?? 0} agents
            </span>
            <span className="flex items-center gap-1">
              <Users className="h-3 w-3" />
              {crew._count_members ?? 0} members
            </span>
          </div>
        </CardContent>
      </Card>
    </Link>
  )
}
