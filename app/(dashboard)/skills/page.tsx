"use client"

import { useCallback, useEffect, useRef, useState } from "react"
import { z } from "zod"
import { Blocks, Search, AlertCircle } from "lucide-react"
import { Input } from "@/components/ui/input"
import { Button } from "@/components/ui/button"
import { PageShell } from "@/components/layout/page-shell"
import { FilterBar } from "@/components/layout/filter-bar"
import { EmptyState } from "@/components/layout/empty-state"
import { Skeleton } from "@/components/ui/skeleton"
import { useWorkspace } from "@/hooks/use-workspace"
import { ImportSkillDialog } from "@/components/skills/import-dialog"
import { SkillCard } from "@/components/features/skills/skill-card"

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

  const buildParams = useCallback(() => {
    const params = new URLSearchParams({ workspace_id: workspaceId! })
    if (debouncedSearch) params.set("search", debouncedSearch)
    if (activeFilter !== "All") params.set("category", activeFilter.toUpperCase())
    return params
  }, [workspaceId, debouncedSearch, activeFilter])

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
        const res = await fetch(`/api/v1/skills?${buildParams()}`)
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
  }, [workspaceId, wsLoading, activeFilter, debouncedSearch, buildParams])

  const isLoading = wsLoading || loading
  const hasActiveFilter = debouncedSearch || activeFilter !== "All"

  function handleImported() {
    if (!workspaceId) return
    setLoading(true)
    setError(null)

    fetch(`/api/v1/skills?${buildParams()}`)
      .then((res) => (res.ok ? res.json() : Promise.reject()))
      .then((json) => {
        const data = SkillsArraySchema.parse(json)
        setSkills(data)
      })
      .catch(() => setError("Failed to reload skills"))
      .finally(() => setLoading(false))
  }

  function handleClearFilters() {
    setActiveFilter("All")
    setSearchQuery("")
    setDebouncedSearch("")
  }

  return (
    <PageShell
      title="Skills"
      description="Browse and manage agent capabilities"
      actions={
        workspaceId ? (
          <ImportSkillDialog workspaceId={workspaceId} onImported={handleImported} />
        ) : null
      }
    >
      <div className="flex flex-col gap-3 sm:flex-row sm:items-center sm:justify-between">
        <div className="relative flex-1 max-w-sm">
          <Search className="absolute left-2.5 top-2.5 h-4 w-4 text-muted-foreground" />
          <Input
            placeholder="Search skills..."
            value={searchQuery}
            onChange={(e) => handleSearchChange(e.target.value)}
            className="pl-9"
            aria-label="Search skills"
          />
        </div>
        <FilterBar
          filters={CATEGORY_FILTERS}
          active={activeFilter}
          onFilter={setActiveFilter}
        />
      </div>

      {error && (
        <div className="flex items-center gap-3">
          <AlertCircle className="h-5 w-5 text-destructive shrink-0" />
          <p className="text-body text-destructive flex-1">{error}</p>
        </div>
      )}

      {isLoading ? (
        <div className="grid grid-cols-1 gap-4 sm:grid-cols-2 lg:grid-cols-3">
          {Array.from({ length: 6 }).map((_, i) => (
            <Skeleton key={i} className="h-[180px] rounded-[var(--radius)]" />
          ))}
        </div>
      ) : skills.length === 0 ? (
        <EmptyState
          icon={Blocks}
          title={hasActiveFilter ? "No matching skills" : "No skills available"}
          description={
            hasActiveFilter
              ? "No skills match the current filter or search."
              : "Import your first skill or browse the marketplace to get started."
          }
        >
          {hasActiveFilter ? (
            <Button className="mt-4" variant="outline" onClick={handleClearFilters}>
              Clear filters
            </Button>
          ) : workspaceId ? (
            <div className="mt-4">
              <ImportSkillDialog workspaceId={workspaceId} onImported={handleImported} />
            </div>
          ) : null}
        </EmptyState>
      ) : (
        <div className="grid grid-cols-1 gap-4 sm:grid-cols-2 lg:grid-cols-3">
          {skills.map((skill) => (
            <SkillCard key={skill.id} skill={skill} />
          ))}
        </div>
      )}
    </PageShell>
  )
}
