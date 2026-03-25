"use client"

import { useEffect, useState, useCallback } from "react"
import { RefreshCw, Sun } from "lucide-react"
import { Button } from "@/components/ui/button"
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card"
import { z } from "zod"
import { useRealtimeEvent } from "@/hooks/use-realtime"

const standupResponseSchema = z.object({
  standup: z.string(),
  crew_id: z.string(),
  since: z.string(),
})

interface CrewStandupProps {
  crewId: string
  workspaceId: string
}

export function CrewStandup({ crewId, workspaceId }: CrewStandupProps) {
  const [standup, setStandup] = useState<string | null>(null)
  const [loading, setLoading] = useState(true)
  const [refreshing, setRefreshing] = useState(false)

  const fetchStandup = useCallback(async (showRefresh = false) => {
    if (showRefresh) setRefreshing(true)
    else setLoading(true)
    try {
      const res = await fetch(
        `/api/v1/crews/${crewId}/standup?workspace_id=${workspaceId}`
      )
      if (res.ok) {
        const json = await res.json()
        const parsed = standupResponseSchema.safeParse(json)
        if (parsed.success) {
          setStandup(parsed.data.standup)
        } else {
          setStandup((json as { standup: string }).standup ?? null)
        }
      }
    } catch {
      // Silently fail
    } finally {
      setLoading(false)
      setRefreshing(false)
    }
  }, [crewId, workspaceId])

  useEffect(() => {
    fetchStandup()
  }, [fetchStandup])

  // Real-time: auto-refresh standup when crew activity changes
  const silentRefresh = useCallback(() => { fetchStandup(true) }, [fetchStandup])
  useRealtimeEvent("mission.updated", silentRefresh)
  useRealtimeEvent("escalation.created", silentRefresh)
  useRealtimeEvent("escalation.resolved", silentRefresh)
  useRealtimeEvent("peer_conversation.updated", silentRefresh)

  if (loading) {
    return (
      <div>
        <h2 className="text-default font-semibold mb-3">Standup Summary</h2>
        <div className="text-body text-muted-foreground">Loading standup...</div>
      </div>
    )
  }

  const isEmpty = !standup?.trim() || (standup.includes("No peer interactions") && !standup.includes("Escalations"))

  return (
    <div>
      <div className="flex items-center justify-between mb-3">
        <h2 className="text-default font-semibold">Standup Summary (last 24h)</h2>
        <Button
          variant="outline"
          size="sm"
          className="gap-2"
          onClick={() => fetchStandup(true)}
          disabled={refreshing}
        >
          <RefreshCw className={`h-3.5 w-3.5 ${refreshing ? "animate-spin" : ""}`} />
          Refresh
        </Button>
      </div>

      {isEmpty ? (
        <div className="flex flex-col items-center gap-3 py-8 text-center">
          <Sun className="h-8 w-8 text-muted-foreground/50" />
          <div>
            <p className="text-body text-muted-foreground">No activity in the last 24 hours.</p>
            <p className="text-label text-muted-foreground/70 mt-1">
              Standup summaries appear after agents interact with each other.
            </p>
          </div>
        </div>
      ) : (
        <Card>
          <CardHeader className="pb-3">
            <CardTitle className="text-body font-medium flex items-center gap-2">
              <Sun className="h-4 w-4" />
              Crew Activity Report
            </CardTitle>
          </CardHeader>
          <CardContent>
            <pre className="text-body whitespace-pre-wrap font-sans">{standup}</pre>
          </CardContent>
        </Card>
      )}
    </div>
  )
}
