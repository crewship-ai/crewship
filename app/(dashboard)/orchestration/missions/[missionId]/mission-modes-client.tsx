"use client"

import { useCallback, useEffect, useMemo, useRef, useState } from "react"
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

  // Track the most recent fetch so a slower response for an old
  // mission/workspace can't overwrite the current view after the user
  // navigated away. Each fetch increments the token; only the response
  // that still matches the latest value is allowed to set state.
  const fetchTokenRef = useRef(0)

  const fetchMission = useCallback(
    async (signal: AbortSignal) => {
      // Workspace bootstrap can settle to null when the user has no
      // active workspace selected. Surface that explicitly instead of
      // leaving the page on the skeleton forever.
      if (!workspaceId) {
        if (!isValidMission) return
        setMission(null)
        setError("No active workspace selected.")
        setLoading(false)
        return
      }
      if (!isValidMission) return

      const myToken = ++fetchTokenRef.current
      try {
        const res = await fetch(
          `/api/v1/missions?workspace_id=${workspaceId}&id=${missionId}&include_tasks=true&limit=1`,
          { signal },
        )
        if (myToken !== fetchTokenRef.current) return
        if (!res.ok) {
          setError(`Mission lookup failed (${res.status})`)
          setMission(null)
          return
        }
        const list = (await res.json()) as Mission[]
        if (myToken !== fetchTokenRef.current) return
        const found = list.find((m) => m.id === missionId) ?? null
        setMission(found)
        setError(found ? null : "Mission not found")
      } catch (e) {
        // Aborts are expected on rapid navigation; never surface them.
        if (signal.aborted || (e instanceof DOMException && e.name === "AbortError")) {
          return
        }
        if (myToken !== fetchTokenRef.current) return
        setError(e instanceof Error ? e.message : "Failed to load mission")
      } finally {
        if (myToken === fetchTokenRef.current) setLoading(false)
      }
    },
    [workspaceId, missionId, isValidMission],
  )

  useEffect(() => {
    const ac = new AbortController()
    void fetchMission(ac.signal)
    return () => ac.abort()
  }, [fetchMission])

  // Realtime: task status changes update the loaded mission in place
  // without an extra fetch — keeps the Spec/Graph views animating
  // while an agent is running. mission.updated triggers a full
  // refetch so structural changes (new tasks, new plan blob) land,
  // but only when the broadcast is for THIS mission — the workspace
  // socket fires mission.updated for every mission and unfiltered
  // refetches would generate avoidable load on busy workspaces.
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
  const handleMissionUpdate = useCallback(
    (event: RealtimeEvent) => {
      const id = (event.payload as { id?: string }).id
      if (!id || id !== missionId) return
      const ac = new AbortController()
      void fetchMission(ac.signal)
      // No teardown wired — the next fetch will bump the token and
      // any in-flight earlier response is discarded by the guard.
    },
    [fetchMission, missionId],
  )
  useRealtimeEvent("mission.updated", handleMissionUpdate)

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
