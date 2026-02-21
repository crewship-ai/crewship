"use client"

import { useEffect, useState } from "react"
import { RefreshCw, Sun } from "lucide-react"
import { Button } from "@/components/ui/button"
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card"

interface CrewStandupProps {
  crewId: string
  workspaceId: string
}

export function CrewStandup({ crewId, workspaceId }: CrewStandupProps) {
  const [standup, setStandup] = useState<string | null>(null)
  const [loading, setLoading] = useState(true)
  const [refreshing, setRefreshing] = useState(false)

  async function fetchStandup(showRefresh = false) {
    if (showRefresh) setRefreshing(true)
    else setLoading(true)
    try {
      const res = await fetch(
        `/api/v1/crews/${crewId}/standup?workspace_id=${workspaceId}`
      )
      if (res.ok) {
        const data = (await res.json()) as { standup: string }
        setStandup(data.standup)
      }
    } catch {
      // Silently fail
    } finally {
      setLoading(false)
      setRefreshing(false)
    }
  }

  useEffect(() => {
    fetchStandup()
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [crewId, workspaceId])

  if (loading) {
    return (
      <div>
        <h2 className="text-base font-semibold mb-3">Standup Summary</h2>
        <div className="text-sm text-muted-foreground">Loading standup...</div>
      </div>
    )
  }

  const isEmpty = standup?.includes("No peer interactions") && !standup?.includes("Escalations")

  return (
    <div>
      <div className="flex items-center justify-between mb-3">
        <h2 className="text-base font-semibold">Standup Summary (last 24h)</h2>
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
            <p className="text-sm text-muted-foreground">No activity in the last 24 hours.</p>
            <p className="text-xs text-muted-foreground/70 mt-1">
              Standup summaries appear after agents interact with each other.
            </p>
          </div>
        </div>
      ) : (
        <Card>
          <CardHeader className="pb-3">
            <CardTitle className="text-sm font-medium flex items-center gap-2">
              <Sun className="h-4 w-4" />
              Crew Activity Report
            </CardTitle>
          </CardHeader>
          <CardContent>
            <pre className="text-sm whitespace-pre-wrap font-sans">{standup}</pre>
          </CardContent>
        </Card>
      )}
    </div>
  )
}
