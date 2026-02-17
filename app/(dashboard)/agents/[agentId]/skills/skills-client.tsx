"use client"

import { useParams } from "next/navigation"

import { use, useState, useEffect } from "react"
import { Puzzle, AlertCircle, Inbox } from "lucide-react"
import { Card, CardContent } from "@/components/ui/card"
import { Badge } from "@/components/ui/badge"
import { Skeleton } from "@/components/ui/skeleton"
import { useWorkspace } from "@/hooks/use-workspace"

interface SkillData {
  id: string
  name: string
  slug: string
  display_name: string | null
  description: string | null
  category: string | null
  source: string
  icon: string | null
  version: string | null
}

interface AgentSkill {
  id: string
  agent_id: string
  skill_id: string
  enabled: boolean
  config: Record<string, unknown> | null
  skill: SkillData
}

const SOURCE_STYLES: Record<string, string> = {
  BUILTIN: "bg-blue-50 text-blue-700 dark:bg-blue-950 dark:text-blue-400",
  CUSTOM: "bg-amber-50 text-amber-700 dark:bg-amber-950 dark:text-amber-400",
  MARKETPLACE: "bg-violet-50 text-violet-700 dark:bg-violet-950 dark:text-violet-400",
}

export function SkillsPageClient() {
  const { agentId } = useParams<{ agentId: string }>()
  const { workspaceId, loading: wsLoading } = useWorkspace()
  const [skills, setSkills] = useState<AgentSkill[]>([])
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)

  useEffect(() => {
    if (!workspaceId) return

    let cancelled = false

    async function fetchSkills() {
      try {
        const res = await fetch(`/api/v1/agents/${agentId}/skills?workspace_id=${workspaceId}`)
        if (!res.ok) {
          if (!cancelled) setError("Failed to load skills")
          return
        }
        const data: AgentSkill[] = await res.json()
        if (!cancelled) setSkills(data)
      } catch {
        if (!cancelled) setError("Network error. Please try again.")
      } finally {
        if (!cancelled) setLoading(false)
      }
    }

    fetchSkills()
    return () => { cancelled = true }
  }, [agentId, workspaceId])

  if (wsLoading || loading) {
    return <SkillsSkeleton />
  }

  if (error) {
    return (
      <div className="p-4 sm:p-6">
        <div className="flex items-center gap-2 text-destructive">
          <AlertCircle className="h-5 w-5" />
          <p className="text-sm">{error}</p>
        </div>
      </div>
    )
  }

  return (
    <div className="p-4 sm:p-6 space-y-4 sm:space-y-6">
      {/* Header */}
      <div className="flex items-center justify-between">
        <p className="text-sm text-muted-foreground">
          {skills.length} skill{skills.length !== 1 ? "s" : ""} assigned
        </p>
      </div>

      {skills.length === 0 ? (
        <div className="flex flex-col items-center justify-center py-16 text-center">
          <Inbox className="h-10 w-10 text-muted-foreground/50 mb-3" />
          <p className="text-sm font-medium text-muted-foreground">No skills assigned</p>
          <p className="text-xs text-muted-foreground mt-1">Assign skills to enable agent capabilities.</p>
        </div>
      ) : (
        <div className="grid gap-3">
          {skills.map((agentSkill) => (
            <Card key={agentSkill.id} className="py-0">
              <CardContent className="p-4 sm:p-5">
                <div className="flex items-start gap-3">
                  <div className="flex h-10 w-10 shrink-0 items-center justify-center rounded-lg bg-primary/10">
                    <Puzzle className="h-5 w-5 text-primary" />
                  </div>
                  <div className="space-y-1 min-w-0">
                    <div className="flex items-center gap-2 flex-wrap">
                      <h3 className="text-sm font-medium">
                        {agentSkill.skill.display_name ?? agentSkill.skill.name}
                      </h3>
                      {agentSkill.skill.category && (
                        <Badge variant="outline" className="text-xs">{agentSkill.skill.category}</Badge>
                      )}
                      <Badge
                        variant="secondary"
                        className={`text-xs ${SOURCE_STYLES[agentSkill.skill.source] ?? ""}`}
                      >
                        {agentSkill.skill.source}
                      </Badge>
                      {!agentSkill.enabled && (
                        <Badge variant="secondary" className="text-xs">Disabled</Badge>
                      )}
                      {agentSkill.skill.version && (
                        <span className="text-xs text-muted-foreground font-mono">v{agentSkill.skill.version}</span>
                      )}
                    </div>
                    {agentSkill.skill.description && (
                      <p className="text-xs text-muted-foreground leading-relaxed">{agentSkill.skill.description}</p>
                    )}
                  </div>
                </div>
              </CardContent>
            </Card>
          ))}
        </div>
      )}
    </div>
  )
}

function SkillsSkeleton() {
  return (
    <div className="p-4 sm:p-6 space-y-4 sm:space-y-6">
      <Skeleton className="h-5 w-32" />
      <div className="grid gap-3">
        {Array.from({ length: 3 }).map((_, i) => (
          <Skeleton key={i} className="h-20 rounded-lg" />
        ))}
      </div>
    </div>
  )
}
