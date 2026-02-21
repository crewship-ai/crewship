"use client"

import { useEffect, useState } from "react"
import { RefreshCw, Target } from "lucide-react"
import Link from "next/link"
import { Button } from "@/components/ui/button"
import { Card, CardContent } from "@/components/ui/card"
import { Progress } from "@/components/ui/progress"
import { MissionStatusBadge } from "@/components/features/missions/mission-status-badge"
import { CreateMissionDialog } from "@/components/features/missions/create-mission-dialog"
import type { Mission } from "@/lib/types/mission"

interface CrewMissionsProps {
  crewId: string
  workspaceId: string
  canCreate: boolean
  leadAgents: { id: string; name: string; slug: string }[]
}

export function CrewMissions({ crewId, workspaceId, canCreate, leadAgents }: CrewMissionsProps) {
  const [missions, setMissions] = useState<Mission[]>([])
  const [loading, setLoading] = useState(true)
  const [refreshing, setRefreshing] = useState(false)

  async function fetchMissions(showRefresh = false) {
    if (showRefresh) setRefreshing(true)
    else setLoading(true)
    try {
      const res = await fetch(
        `/api/v1/crews/${crewId}/missions?workspace_id=${workspaceId}&limit=5`
      )
      if (res.ok) {
        const data = (await res.json()) as Mission[]
        setMissions(data)
      }
    } catch {
      // Silently fail
    } finally {
      setLoading(false)
      setRefreshing(false)
    }
  }

  useEffect(() => {
    fetchMissions()
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [crewId, workspaceId])

  if (loading) {
    return (
      <div>
        <h2 className="text-base font-semibold mb-3">Missions</h2>
        <div className="text-sm text-muted-foreground">Loading missions...</div>
      </div>
    )
  }

  return (
    <div>
      <div className="flex items-center justify-between mb-3">
        <h2 className="text-base font-semibold">Missions</h2>
        <div className="flex items-center gap-2">
          <Button
            variant="outline"
            size="sm"
            className="gap-2"
            onClick={() => fetchMissions(true)}
            disabled={refreshing}
          >
            <RefreshCw className={`h-3.5 w-3.5 ${refreshing ? "animate-spin" : ""}`} />
            Refresh
          </Button>
          {canCreate && (
            <CreateMissionDialog
              crewId={crewId}
              workspaceId={workspaceId}
              leadAgents={leadAgents}
              onCreated={() => fetchMissions(true)}
            />
          )}
        </div>
      </div>

      {missions.length === 0 ? (
        <div className="flex flex-col items-center gap-3 py-8 text-center">
          <Target className="h-8 w-8 text-muted-foreground/50" />
          <div>
            <p className="text-sm text-muted-foreground">No missions yet.</p>
            <p className="text-xs text-muted-foreground/70 mt-1">
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
                          <h3 className="text-sm font-medium truncate">{mission.title}</h3>
                          <MissionStatusBadge status={mission.status} />
                        </div>
                        <div className="flex items-center gap-3 mt-1">
                          <span className="text-xs text-muted-foreground">
                            Lead: @{mission.lead_agent_slug}
                          </span>
                          {stats && stats.total > 0 && (
                            <span className="text-xs text-muted-foreground">
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
