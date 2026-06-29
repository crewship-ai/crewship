"use client"

import { useCallback, useState } from "react"
import { Target } from "lucide-react"
import Link from "next/link"
import { Card, CardContent } from "@/components/ui/card"
import { Progress } from "@/components/ui/progress"
import { MissionStatusBadge } from "@/components/features/missions/mission-status-badge"
import { CreateMissionDialog } from "@/components/features/missions/create-mission-dialog"
import { useRealtimeEvent } from "@/hooks/use-realtime"
import { useApiResource } from "@/hooks/use-api-resource"
import type { Mission } from "@/lib/types/mission"

interface CrewMissionsProps {
  crewId: string
  workspaceId: string
  canCreate: boolean
  leadAgents: { id: string; name: string; slug: string }[]
}

export function CrewMissions({ crewId, workspaceId, canCreate, leadAgents }: CrewMissionsProps) {
  // `refreshing` drives the "Updating..." indicator during the post-create
  // refetch; the initial load uses the hook's own `loading`.
  const [refreshing, setRefreshing] = useState(false)
  const { data, loading, reload } = useApiResource<Mission[]>(
    `/api/v1/crews/${crewId}/missions?workspace_id=${workspaceId}&limit=5`,
    { keepDataOnError: true },
  )
  const missions = data ?? []

  // After creating a mission, refetch with the "Updating..." indicator on
  // (silent so the hook's loading spinner stays down while data refreshes).
  const refreshWithIndicator = useCallback(() => {
    setRefreshing(true)
    void reload({ silent: true }).finally(() => setRefreshing(false))
  }, [reload])

  // Real-time: refetch (silently) when mission or task status changes.
  const refreshSilently = useCallback(() => { reload({ silent: true }) }, [reload])
  useRealtimeEvent("mission.updated", refreshSilently)
  useRealtimeEvent("task.updated", refreshSilently)

  if (loading) {
    return (
      <div>
        <h2 className="text-default font-semibold mb-3">Missions</h2>
        <div className="text-body text-muted-foreground">Loading missions...</div>
      </div>
    )
  }

  return (
    <div>
      <div className="flex items-center justify-between mb-3">
        <h2 className="text-default font-semibold">Missions</h2>
        <div className="flex items-center gap-2">
          <span role="status" aria-live="polite" className="text-label text-muted-foreground">
            {refreshing ? "Updating..." : "Live"}
          </span>
          {canCreate && (
            <CreateMissionDialog
              crewId={crewId}
              workspaceId={workspaceId}
              leadAgents={leadAgents}
              onCreated={refreshWithIndicator}
            />
          )}
        </div>
      </div>

      {missions.length === 0 ? (
        <div className="flex flex-col items-center gap-3 py-8 text-center">
          <Target className="h-8 w-8 text-muted-foreground/50" />
          <div>
            <p className="text-body text-muted-foreground">No missions yet.</p>
            <p className="text-label text-muted-foreground/70 mt-1">
              Missions organize complex tasks into trackable subtasks for the crew.
            </p>
          </div>
        </div>
      ) : (
        <div className="space-y-2">
          {missions.map((mission) => {
            const stats = mission.task_stats
            const progress =
              stats && stats.total > 0
                ? Math.round((stats.completed / stats.total) * 100)
                : 0

            return (
              <Link
                key={mission.id}
                href={`/crews/${crewId}/missions/${mission.id}`}
              >
                <Card className="hover:border-primary/50 transition-colors cursor-pointer">
                  <CardContent className="p-3 sm:p-4">
                    <div className="flex items-center justify-between gap-3">
                      <div className="flex-1 min-w-0">
                        <div className="flex items-center gap-2">
                          <h3 className="text-body font-medium truncate">{mission.title}</h3>
                          <MissionStatusBadge status={mission.status} />
                        </div>
                        <div className="flex items-center gap-3 mt-1">
                          <span className="text-label text-muted-foreground">
                            Lead: @{mission.lead_agent_slug}
                          </span>
                          {stats && stats.total > 0 && (
                            <span className="text-label text-muted-foreground">
                              {stats.completed}/{stats.total} tasks
                            </span>
                          )}
                        </div>
                      </div>
                    </div>
                    {stats && stats.total > 0 && (
                      <Progress value={progress} className="mt-2 h-1.5" />
                    )}
                  </CardContent>
                </Card>
              </Link>
            )
          })}
        </div>
      )}
    </div>
  )
}
