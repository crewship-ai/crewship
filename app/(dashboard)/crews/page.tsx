"use client"

import { useEffect, useState, useMemo } from "react"
import { Users, Plus, Search, RotateCcw, ArrowUpDown } from "lucide-react"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { PageHeader } from "@/components/layout/page-header"
import { EmptyState } from "@/components/layout/empty-state"
import { Skeleton } from "@/components/ui/skeleton"
import { CrewCard } from "@/components/features/crews/crew-card"
import { CrewActivityFeed } from "@/components/features/crews/crew-activity-feed"
import { Separator } from "@/components/ui/separator"
import { useWorkspace } from "@/hooks/use-workspace"
import { useAbilities } from "@/hooks/use-abilities"
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu"
import { toast } from "sonner"
import Link from "next/link"

interface Crew {
  id: string
  name: string
  slug: string
  description: string | null
  color: string | null
  icon: string | null
  created_at: string
  _count: { agents: number; members: number }
}

type SortOption = "name" | "created_at" | "agents"

const sortLabels: Record<SortOption, string> = {
  name: "Name",
  created_at: "Newest first",
  agents: "Most agents",
}

export default function CrewsPage() {
  const { workspaceId, loading: wsLoading } = useWorkspace()
  const { abilities } = useAbilities()
  const [crews, setCrews] = useState<Crew[]>([])
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)
  const [search, setSearch] = useState("")
  const [sortBy, setSortBy] = useState<SortOption>("name")

  async function fetchCrews() {
    if (!workspaceId) return

    setLoading(true)
    setError(null)
    try {
      const res = await fetch(`/api/v1/crews?workspace_id=${workspaceId}`)
      if (!res.ok) {
        const msg = "Failed to load crews"
        setError(msg)
        toast.error(msg)
        return
      }
      const data = (await res.json()) as Crew[]
      setCrews(data)
    } catch {
      const msg = "Failed to load crews"
      setError(msg)
      toast.error(msg)
    } finally {
      setLoading(false)
    }
  }

  useEffect(() => {
    if (!workspaceId) {
      if (!wsLoading) setLoading(false)
      return
    }

    fetchCrews()
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [workspaceId, wsLoading])

  const filteredAndSorted = useMemo(() => {
    let result = crews

    if (search.trim()) {
      const q = search.toLowerCase()
      result = result.filter(
        (c) =>
          c.name.toLowerCase().includes(q) ||
          c.slug.toLowerCase().includes(q) ||
          c.description?.toLowerCase().includes(q)
      )
    }

    return [...result].sort((a, b) => {
      switch (sortBy) {
        case "name":
          return a.name.localeCompare(b.name)
        case "created_at":
          return new Date(b.created_at).getTime() - new Date(a.created_at).getTime()
        case "agents":
          return b._count.agents - a._count.agents
        default:
          return 0
      }
    })
  }, [crews, search, sortBy])

  const isLoading = wsLoading || loading

  return (
    <div className="p-4 sm:p-6 space-y-4 sm:space-y-6">
      <PageHeader title="Crews" description="Organize agents into departments">
        {abilities.can("create", "Crew") && (
          <Button asChild>
            <Link href="/crews/new">
              <Plus className="mr-2 h-4 w-4" />
              New Crew
            </Link>
          </Button>
        )}
      </PageHeader>

      {error && (
        <div className="flex items-center gap-3">
          <p className="text-sm text-destructive flex-1">{error}</p>
          <Button variant="outline" size="sm" onClick={fetchCrews} className="gap-2 shrink-0">
            <RotateCcw className="h-3.5 w-3.5" />
            Try Again
          </Button>
        </div>
      )}

      {!isLoading && crews.length > 0 && (
        <div className="flex flex-col gap-3 sm:flex-row sm:items-center sm:justify-between">
          <div className="relative flex-1 max-w-sm">
            <Search className="absolute left-3 top-1/2 -translate-y-1/2 h-4 w-4 text-muted-foreground" />
            <Input
              placeholder="Search crews..."
              value={search}
              onChange={(e) => setSearch(e.target.value)}
              className="pl-9"
            />
          </div>
          <DropdownMenu>
            <DropdownMenuTrigger asChild>
              <Button variant="outline" size="sm" className="gap-2 shrink-0">
                <ArrowUpDown className="h-3.5 w-3.5" />
                {sortLabels[sortBy]}
              </Button>
            </DropdownMenuTrigger>
            <DropdownMenuContent align="end">
              {(Object.keys(sortLabels) as SortOption[]).map((key) => (
                <DropdownMenuItem key={key} onClick={() => setSortBy(key)}>
                  {sortLabels[key]}
                </DropdownMenuItem>
              ))}
            </DropdownMenuContent>
          </DropdownMenu>
        </div>
      )}

      {isLoading ? (
        <div className="grid grid-cols-1 sm:grid-cols-2 lg:grid-cols-3 gap-3 sm:gap-4">
          {Array.from({ length: 3 }).map((_, i) => (
            <Skeleton key={i} className="h-[140px] rounded-xl" />
          ))}
        </div>
      ) : crews.length === 0 ? (
        <EmptyState
          icon={Users}
          title="No crews yet"
          description="Create a crew to group your agents by department or function."
        >
          {abilities.can("create", "Crew") && (
            <Button className="mt-4" asChild>
              <Link href="/crews/new">
                <Plus className="mr-2 h-4 w-4" />
                Create Crew
              </Link>
            </Button>
          )}
        </EmptyState>
      ) : filteredAndSorted.length === 0 ? (
        <EmptyState
          icon={Search}
          title="No matching crews"
          description="No crews match your search. Try a different query."
        />
      ) : (
        <div className="grid grid-cols-1 sm:grid-cols-2 lg:grid-cols-3 gap-3 sm:gap-4">
          {filteredAndSorted.map((crew) => (
            <CrewCard key={crew.id} crew={crew} />
          ))}
        </div>
      )}

      {!isLoading && crews.length > 0 && workspaceId && (
        <>
          <Separator />
          <CrewActivityFeed workspaceId={workspaceId} />
        </>
      )}
    </div>
  )
}
