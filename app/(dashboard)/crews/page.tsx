"use client"

import { useEffect, useState } from "react"
import { Users, Plus } from "lucide-react"
import { Button } from "@/components/ui/button"
import { PageHeader } from "@/components/layout/page-header"
import { EmptyState } from "@/components/layout/empty-state"
import { Skeleton } from "@/components/ui/skeleton"
import { CrewCard } from "@/components/features/crews/crew-card"
import { useWorkspace } from "@/hooks/use-workspace"
import { useAbilities } from "@/hooks/use-abilities"
import Link from "next/link"

interface Crew {
  id: string
  name: string
  slug: string
  description: string | null
  color: string | null
  icon: string | null
  _count: { agents: number; members: number }
}

export default function CrewsPage() {
  const { workspaceId, loading: wsLoading } = useWorkspace()
  const { abilities } = useAbilities()
  const [crews, setCrews] = useState<Crew[]>([])
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)

  useEffect(() => {
    if (!workspaceId) {
      if (!wsLoading) setLoading(false)
      return
    }

    let cancelled = false

    async function fetchCrews() {
      setLoading(true)
      setError(null)
      try {
        const res = await fetch(`/api/v1/crews?workspace_id=${workspaceId}`)
        if (!res.ok) {
          setError("Failed to load crews")
          return
        }
        const data = (await res.json()) as Crew[]
        if (!cancelled) setCrews(data)
      } catch {
        if (!cancelled) setError("Failed to load crews")
      } finally {
        if (!cancelled) setLoading(false)
      }
    }

    fetchCrews()
    return () => {
      cancelled = true
    }
  }, [workspaceId, wsLoading])

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

      {error && <p className="text-sm text-destructive">{error}</p>}

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
      ) : (
        <div className="grid grid-cols-1 sm:grid-cols-2 lg:grid-cols-3 gap-3 sm:gap-4">
          {crews.map((crew) => (
            <CrewCard key={crew.id} crew={crew} />
          ))}
        </div>
      )}
    </div>
  )
}
