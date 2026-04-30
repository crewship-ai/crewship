"use client"

import { useCallback, useEffect, useMemo, useState } from "react"
import { useParams } from "next/navigation"
import Link from "next/link"
import { ArrowLeft } from "lucide-react"
import { Skeleton } from "@/components/ui/skeleton"
import { useWorkspace } from "@/hooks/use-workspace"
import { useRealtimeEvent, type RealtimeEvent } from "@/hooks/use-realtime"
import { MissionModes } from "@/components/features/orchestration/mission-modes"
import type { Mission, MissionTask } from "@/lib/types/mission"

/**
 * Mission Modes detail page. Resolves the mission id from the dynamic
 * route segment, fetches the single mission record, and renders the
 * three-view (Spec / Document / Graph) shell. Realtime updates flow
 * through useRealtimeEvent — the same WebSocket pipeline the
 * orchestration page uses — so a task status change from any agent
 * propagates here without a manual refresh.
 *
 * Backend endpoint /api/v1/missions/:id is filterable by id today; we
 * use the list endpoint with id filter for now to avoid blocking on a
 * detail-shaped handler. When the dedicated handler lands, swap the
 * URL — the response shape is identical.
 */
export function MissionModesClient() {
  const params = useParams<{ missionId: string }>()
  const missionId = params?.missionId ?? ""
  const { workspaceId, loading: wsLoading } = useWorkspace()

  const [mission, setMission] = useState<Mission | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [loading, setLoading] = useState(true)

  // The static-export placeholder produces missionId === "_"; bail
  // before issuing any fetch so the build doesn't 404 against
  // /api/v1/missions/_.
  const isValidMission = Boolean(missionId) && missionId !== "_"

  const fetchMission = useCallback(async () => {
    if (!workspaceId || !isValidMission) return
    try {
      const res = await fetch(
        `/api/v1/missions?workspace_id=${workspaceId}&id=${missionId}&include_tasks=true&limit=1`,
      )
      if (!res.ok) {
        setError(`Mission lookup failed (${res.status})`)
        setMission(null)
        return
      }
      const list = (await res.json()) as Mission[]
      const found = list.find((m) => m.id === missionId) ?? null
      setMission(found)
      if (!found) setError("Mission not found")
      else setError(null)
    } catch (e) {
      setError(e instanceof Error ? e.message : "Failed to load mission")
    } finally {
      setLoading(false)
    }
  }, [workspaceId, missionId, isValidMission])

  useEffect(() => {
    fetchMission()
  }, [fetchMission])

  // Realtime: task status changes update the loaded mission in place
  // without an extra fetch — keeps the Spec/Graph views animating
  // while an agent is running. mission.updated triggers a full
  // refetch so structural changes (new tasks, new plan blob) land.
  const handleTaskUpdate = useCallback(
    (event: RealtimeEvent) => {
      const { id, status, mission_id } = event.payload as {
        id?: string
        status?: string
        mission_id?: string
      }
      if (!id || !status) return
      if (mission_id && mission_id !== missionId) return
      setMission((prev) => {
        if (!prev) return prev
        const tasks = prev.tasks ?? []
        const idx = tasks.findIndex((t) => t.id === id)
        if (idx === -1) return prev
        const next = [...tasks]
        next[idx] = { ...next[idx], status: status as MissionTask["status"] }
        return { ...prev, tasks: next }
      })
    },
    [missionId],
  )
  useRealtimeEvent("task.updated", handleTaskUpdate)
  useRealtimeEvent("mission.updated", useCallback(() => fetchMission(), [fetchMission]))

  const ready = useMemo(
    () => !wsLoading && !loading && !!mission,
    [wsLoading, loading, mission],
  )

  if (!isValidMission) {
    return <NotFound message="Mission id is missing or invalid." />
  }

  if (wsLoading || loading) {
    return (
      <div className="flex flex-col h-full">
        <header className="flex items-center gap-4 px-6 py-3 border-b">
          <Skeleton className="h-5 w-40" />
        </header>
        <div className="p-6">
          <Skeleton className="h-[600px] w-full rounded-xl" />
        </div>
      </div>
    )
  }

  if (error || !ready || !mission) {
    return <NotFound message={error ?? "Mission not available."} />
  }

  return <MissionModes mission={mission} />
}

function NotFound({ message }: { message: string }) {
  return (
    <div className="flex flex-col h-full">
      <header className="flex items-center gap-4 px-6 py-3 border-b bg-background">
        <Link
          href="/orchestration"
          className="flex items-center gap-1 text-sm text-muted-foreground hover:text-foreground"
        >
          <ArrowLeft className="h-4 w-4" />
          Orchestration
        </Link>
      </header>
      <div className="flex-1 flex items-center justify-center text-center px-6">
        <div className="max-w-md">
          <p className="text-sm text-muted-foreground">{message}</p>
        </div>
      </div>
    </div>
  )
}
