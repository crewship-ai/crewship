"use client"

import { useCallback, useEffect, useState } from "react"
import { useParams } from "next/navigation"
import { ArrowLeft } from "lucide-react"
import Link from "next/link"
import { Button } from "@/components/ui/button"
import { Skeleton } from "@/components/ui/skeleton"
import { MissionHeader } from "@/components/features/missions/mission-header"
import { MissionBoard } from "@/components/features/missions/mission-board"
import { useWorkspace } from "@/hooks/use-workspace"
import { useRealtimeEvent } from "@/hooks/use-realtime"
import type { Mission } from "@/lib/types/mission"

export function MissionDetailPageClient() {
  const params = useParams<{ crewId: string; missionId: string }>()
  const { workspaceId, loading: wsLoading } = useWorkspace()

  const [mission, setMission] = useState<Mission | null>(null)
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)

  const fetchMission = useCallback(async (showLoading = true) => {
    if (!workspaceId) return
    if (showLoading) {
      setLoading(true)
      setError(null)
    }
    try {
      const res = await fetch(
        `/api/v1/crews/${params.crewId}/missions/${params.missionId}?workspace_id=${workspaceId}`
      )
      if (!res.ok) {
        if (showLoading) setError("Mission not found")
        return
      }
      const data = (await res.json()) as Mission
      setMission(data)
    } catch {
      if (showLoading) setError("Failed to load mission")
    } finally {
      if (showLoading) setLoading(false)
    }
  }, [workspaceId, params.crewId, params.missionId])

  useEffect(() => {
    if (!workspaceId) {
      if (!wsLoading) setLoading(false)
      return
    }
    fetchMission()
  }, [workspaceId, wsLoading, fetchMission])

  // Real-time: refetch when mission or task status changes
  useRealtimeEvent("mission.updated", useCallback(() => { fetchMission(false) }, [fetchMission]))
  useRealtimeEvent("task.updated", useCallback(() => { fetchMission(false) }, [fetchMission]))

  const isLoading = wsLoading || loading

  if (!workspaceId && !wsLoading) {
    return (
      <div className="p-4 sm:p-6 space-y-4 max-w-4xl">
        <Button variant="ghost" size="sm" asChild>
          <Link href={`/crews/${params.crewId}`}>
            <ArrowLeft className="mr-2 h-4 w-4" />
            Back to Crew
          </Link>
        </Button>
        <p className="text-sm text-muted-foreground">No workspace selected.</p>
      </div>
    )
  }

  if (error) {
    return (
      <div className="p-4 sm:p-6 space-y-4 max-w-4xl">
        <Button variant="ghost" size="sm" asChild>
          <Link href={`/crews/${params.crewId}`}>
            <ArrowLeft className="mr-2 h-4 w-4" />
            Back to Crew
          </Link>
        </Button>
        <p className="text-sm text-destructive">{error}</p>
      </div>
    )
  }

  if (isLoading) {
    return (
      <div className="p-4 sm:p-6 space-y-4 max-w-4xl">
        <Skeleton className="h-8 w-48" />
        <Skeleton className="h-[80px] rounded-xl" />
        <Skeleton className="h-[300px] rounded-xl" />
      </div>
    )
  }

  if (!mission) return null

  return (
    <div className="p-4 sm:p-6 space-y-6 max-w-4xl">
      <Button variant="ghost" size="sm" asChild>
        <Link href={`/crews/${params.crewId}`}>
          <ArrowLeft className="mr-2 h-4 w-4" />
          Back to Crew
        </Link>
      </Button>

      <MissionHeader mission={mission} />

      <MissionBoard tasks={mission.tasks ?? []} taskStats={mission.task_stats} />

      <div className="text-xs text-muted-foreground">
        Created {new Date(mission.created_at).toLocaleDateString()}
      </div>
    </div>
  )
}
