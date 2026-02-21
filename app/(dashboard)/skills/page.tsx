"use client"

import { useCallback, useEffect, useRef, useState } from "react"
import { z } from "zod"
import { Blocks, Search } from "lucide-react"
import { Card, CardContent } from "@/components/ui/card"
import { Badge } from "@/components/ui/badge"
import { Input } from "@/components/ui/input"
import { PageHeader } from "@/components/layout/page-header"
import { FilterBar } from "@/components/layout/filter-bar"
import { EmptyState } from "@/components/layout/empty-state"
import { Skeleton } from "@/components/ui/skeleton"
import { useWorkspace } from "@/hooks/use-workspace"
import { ImportSkillDialog } from "@/components/skills/import-dialog"
import Link from "next/link"

const SkillSchema = z.object({
  id: z.string(),
  name: z.string(),
  slug: z.string(),
  display_name: z.string().nullable(),
  description: z.string().nullable(),
  version: z.string().nullable(),
  author: z.string().nullable(),
  category: z.string(),
  source: z.string(),
  icon: z.string().nullable(),
  verification: z.string().nullable(),
  downloads: z.number().nullable(),
  rating_avg: z.number().nullable(),
  rating_count: z.number().nullable(),
  tags: z.unknown().nullable(),
  featured: z.boolean(),
  pricing_tier: z.string().nullable(),
  tool_count: z.number().nullable(),
  created_at: z.string(),
  updated_at: z.string(),
})
const SkillsArraySchema = z.array(SkillSchema)
type Skill = z.infer<typeof SkillSchema>

const CATEGORY_FILTERS = [
  "All",
  "Coding",
  "Research",
  "Development",
  "DevOps",
  "Communication",
  "Custom",
]

export default function SkillsPage() {
  const { workspaceId, loading: wsLoading } = useWorkspace()
  const [skills, setSkills] = useState<Skill[]>([])
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)
  const [activeFilter, setActiveFilter] = useState("All")
  const [searchQuery, setSearchQuery] = useState("")
  const [debouncedSearch, setDebouncedSearch] = useState("")
  const debounceTimer = useRef<ReturnType<typeof setTimeout> | null>(null)

  const handleSearchChange = useCallback((value: string) => {
    setSearchQuery(value)
    if (debounceTimer.current) clearTimeout(debounceTimer.current)
    debounceTimer.current = setTimeout(() => {
      setDebouncedSearch(value)
    }, 300)
  }, [])

  useEffect(() => {
    return () => {
      if (debounceTimer.current) clearTimeout(debounceTimer.current)
    }
  }, [])

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
        const params = new URLSearchParams({ workspace_id: workspaceId! })
        if (debouncedSearch) params.set("search", debouncedSearch)
        if (activeFilter !== "All") params.set("category", activeFilter.toUpperCase())

        const res = await fetch(`/api/v1/skills?${params}`)
        if (!res.ok) {
          setError("Failed to load skills")
          return
        }
        const json = await res.json()
        const data = SkillsArraySchema.parse(json)
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
  }, [workspaceId, wsLoading, activeFilter, debouncedSearch])

  const isLoading = wsLoading || loading

  function handleImported() {
    if (!workspaceId) return
    setLoading(true)
    setError(null)
    const params = new URLSearchParams({ workspace_id: workspaceId! })
    if (debouncedSearch) params.set("search", debouncedSearch)
    if (activeFilter !== "All") params.set("category", activeFilter.toUpperCase())

    fetch(`/api/v1/skills?${params}`)
      .then((res) => (res.ok ? res.json() : Promise.reject()))
      .then((json) => {
        const data = SkillsArraySchema.parse(json)
        setSkills(data)
      })
      .catch(() => setError("Failed to reload skills"))
      .finally(() => setLoading(false))
  }

  return (
    <div className="p-4 sm:p-6 space-y-4 sm:space-y-6">
      <PageHeader title="Skills" description="Browse and manage agent skills">
        {workspaceId && (
          <ImportSkillDialog workspaceId={workspaceId} onImported={handleImported} />
        )}
      </PageHeader>

      <div className="flex flex-col sm:flex-row gap-3">
        <div className="relative flex-1 max-w-sm">
          <Search className="absolute left-2.5 top-2.5 h-4 w-4 text-muted-foreground" />
          <Input
            placeholder="Search skills..."
            value={searchQuery}
            onChange={(e) => handleSearchChange(e.target.value)}
            className="pl-9"
          />
        </div>
      </div>

      <FilterBar
        filters={CATEGORY_FILTERS}
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
      ) : skills.length === 0 ? (
        <EmptyState
          icon={Blocks}
          title={debouncedSearch || activeFilter !== "All" ? "No matching skills" : "No skills available"}
          description={
            debouncedSearch || activeFilter !== "All"
              ? "No skills match the current filter or search."
              : "Skills will appear here once they are added to the platform."
          }
        />
      ) : (
        <div className="grid grid-cols-1 sm:grid-cols-2 lg:grid-cols-3 gap-3 sm:gap-4">
          {skills.map((skill) => (
            <Link key={skill.id} href={`/skills/${skill.id}`}>
              <Card className="hover:border-primary/50 transition-colors cursor-pointer h-full">
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
            </Link>
          ))}
        </div>
      )}
    </div>
  )
}
