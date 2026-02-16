"use client"

import { useEffect, useState } from "react"
import { Users, Plus } from "lucide-react"
import { Button } from "@/components/ui/button"
import { PageHeader } from "@/components/layout/page-header"
import { EmptyState } from "@/components/layout/empty-state"
import { Skeleton } from "@/components/ui/skeleton"
import { TeamCard } from "@/components/features/teams/team-card"
import { useOrg } from "@/hooks/use-org"
import Link from "next/link"

interface Team {
  id: string
  name: string
  slug: string
  description: string | null
  color: string | null
  icon: string | null
  _count: { agents: number; members: number }
}

export default function TeamsPage() {
  const { orgId, loading: orgLoading } = useOrg()
  const [teams, setTeams] = useState<Team[]>([])
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)

  useEffect(() => {
    if (!orgId) {
      if (!orgLoading) setLoading(false)
      return
    }

    let cancelled = false

    async function fetchTeams() {
      setLoading(true)
      setError(null)
      try {
        const res = await fetch(`/api/v1/teams?org_id=${orgId}`)
        if (!res.ok) {
          setError("Failed to load teams")
          return
        }
        const data = (await res.json()) as Team[]
        if (!cancelled) setTeams(data)
      } catch {
        if (!cancelled) setError("Failed to load teams")
      } finally {
        if (!cancelled) setLoading(false)
      }
    }

    fetchTeams()
    return () => {
      cancelled = true
    }
  }, [orgId, orgLoading])

  const isLoading = orgLoading || loading

  return (
    <div className="p-4 sm:p-6 space-y-4 sm:space-y-6">
      <PageHeader title="Teams" description="Organize agents into departments">
        <Button asChild>
          <Link href="/teams/new">
            <Plus className="mr-2 h-4 w-4" />
            New Team
          </Link>
        </Button>
      </PageHeader>

      {error && <p className="text-sm text-destructive">{error}</p>}

      {isLoading ? (
        <div className="grid grid-cols-1 sm:grid-cols-2 lg:grid-cols-3 gap-3 sm:gap-4">
          {Array.from({ length: 3 }).map((_, i) => (
            <Skeleton key={i} className="h-[140px] rounded-xl" />
          ))}
        </div>
      ) : teams.length === 0 ? (
        <EmptyState
          icon={Users}
          title="No teams yet"
          description="Create a team to group your agents by department or function."
        >
          <Button className="mt-4" asChild>
            <Link href="/teams/new">
              <Plus className="mr-2 h-4 w-4" />
              Create Team
            </Link>
          </Button>
        </EmptyState>
      ) : (
        <div className="grid grid-cols-1 sm:grid-cols-2 lg:grid-cols-3 gap-3 sm:gap-4">
          {teams.map((team) => (
            <TeamCard key={team.id} team={team} />
          ))}
        </div>
      )}
    </div>
  )
}
