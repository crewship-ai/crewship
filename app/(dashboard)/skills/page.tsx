"use client"

import { useEffect, useState } from "react"
import { Blocks } from "lucide-react"
import { Card, CardContent } from "@/components/ui/card"
import { Badge } from "@/components/ui/badge"
import { PageHeader } from "@/components/layout/page-header"
import { FilterBar } from "@/components/layout/filter-bar"
import { EmptyState } from "@/components/layout/empty-state"
import { Skeleton } from "@/components/ui/skeleton"
import { useWorkspace } from "@/hooks/use-workspace"

interface Skill {
  id: string
  name: string
  slug: string
  display_name: string | null
  description: string | null
  version: string | null
  author: string | null
  category: string
  source: string
  icon: string | null
  verification: string | null
  downloads: number | null
  rating_avg: number | null
  rating_count: number | null
  tags: string[]
  featured: boolean
  pricing_tier: string | null
  tool_count: number | null
  created_at: string
  updated_at: string
}

export default function SkillsPage() {
  const { workspaceId, loading: wsLoading } = useWorkspace()
  const [skills, setSkills] = useState<Skill[]>([])
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)
  const [activeFilter, setActiveFilter] = useState("All")

  useEffect(() => {
    if (!workspaceId) {
      if (!wsLoading) setLoading(false)
      return
    }

    let cancelled = false

    async function fetchSkills() {
      setLoading(true)
      setError(null)
      try {
        const res = await fetch(`/api/v1/skills?workspace_id=${workspaceId}`)
        if (!res.ok) {
          setError("Failed to load skills")
          return
        }
        const data = (await res.json()) as Skill[]
        if (!cancelled) setSkills(data)
      } catch {
        if (!cancelled) setError("Failed to load skills")
      } finally {
        if (!cancelled) setLoading(false)
      }
    }

    fetchSkills()
    return () => {
      cancelled = true
    }
  }, [workspaceId, wsLoading])

  const isLoading = wsLoading || loading

  const sourceFilters = ["All", "Bundled", "Managed", "Marketplace", "Custom"]

  const filteredSkills =
    activeFilter === "All"
      ? skills
      : skills.filter((s) => s.source === activeFilter.toUpperCase())

  return (
    <div className="p-4 sm:p-6 space-y-4 sm:space-y-6">
      <PageHeader title="Skills" description="Browse and manage agent skills" />

      <FilterBar
        filters={sourceFilters}
        active={activeFilter}
        onFilter={setActiveFilter}
      />

      {error && <p className="text-sm text-destructive">{error}</p>}

      {isLoading ? (
        <div className="grid grid-cols-1 sm:grid-cols-2 lg:grid-cols-3 gap-3 sm:gap-4">
          {Array.from({ length: 3 }).map((_, i) => (
            <Skeleton key={i} className="h-[120px] rounded-xl" />
          ))}
        </div>
      ) : filteredSkills.length === 0 ? (
        <EmptyState
          icon={Blocks}
          title={skills.length === 0 ? "No skills available" : "No matching skills"}
          description={
            skills.length === 0
              ? "Skills will appear here once they are added to the platform."
              : "No skills match the current filter."
          }
        />
      ) : (
        <div className="grid grid-cols-1 sm:grid-cols-2 lg:grid-cols-3 gap-3 sm:gap-4">
          {filteredSkills.map((skill) => (
            <Card key={skill.id} className="hover:border-primary/50 transition-colors cursor-pointer">
              <CardContent className="p-4 sm:p-5">
                <div className="flex items-start gap-3">
                  <div className="flex h-10 w-10 items-center justify-center rounded-lg bg-muted text-lg shrink-0">
                    {skill.icon ?? "🔧"}
                  </div>
                  <div className="flex-1 min-w-0">
                    <div className="flex items-center gap-2 flex-wrap">
                      <h3 className="text-sm font-semibold truncate">
                        {skill.display_name ?? skill.name}
                      </h3>
                      <Badge variant="secondary" className="text-[10px] shrink-0">
                        {skill.source}
                      </Badge>
                    </div>
                    {skill.description && (
                      <p className="mt-1 text-xs text-muted-foreground line-clamp-2">
                        {skill.description}
                      </p>
                    )}
                    <div className="mt-2 flex items-center gap-2 flex-wrap">
                      <Badge variant="outline" className="text-[10px]">
                        {skill.category}
                      </Badge>
                      {skill.version && (
                        <span className="text-[10px] text-muted-foreground">
                          v{skill.version}
                        </span>
                      )}
                      {skill.tool_count != null && skill.tool_count > 0 && (
                        <span className="text-[10px] text-muted-foreground">
                          {skill.tool_count} tools
                        </span>
                      )}
                    </div>
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
