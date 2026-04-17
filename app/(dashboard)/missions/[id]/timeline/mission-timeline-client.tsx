"use client"

import { useCallback, useMemo } from "react"
import Link from "next/link"
import { useParams } from "next/navigation"
import { ArrowLeft, Map, RefreshCw } from "lucide-react"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import { cn } from "@/lib/utils"
import { useJournalList } from "@/hooks/use-journal-list"
import { useJournalStream } from "@/hooks/use-journal-stream"
import { useWorkspace } from "@/hooks/use-workspace"
import { MissionTimeline } from "@/components/features/timeline/mission-timeline"

/**
 * Mission timeline view. Reads the journal filtered by `mission_id` and
 * renders a vertical story with checkpoints as milestone markers.
 */
export function MissionTimelineClient() {
  const params = useParams<{ id: string }>()
  const missionId = params?.id ?? ""
  const { workspaceId, loading: wsLoading } = useWorkspace()

  // `_` is the static-export placeholder route — bail before firing any
  // fetch / EventSource so we don't stream for a bogus mission id.
  const isValidMission = Boolean(missionId) && missionId !== "_"

  const queryParams = useMemo<Record<string, string | undefined>>(
    () => ({ mission_id: isValidMission ? missionId : undefined }),
    [missionId, isValidMission],
  )

  const { entries, loading, error, refresh, prependLive } = useJournalList({
    workspaceId,
    params: queryParams,
    enabled: !wsLoading && isValidMission,
    limit: 200,
  })

  const handleLive = useCallback(
    (entry: Parameters<typeof prependLive>[0]) => {
      prependLive(entry)
    },
    [prependLive],
  )
  useJournalStream({
    workspaceId,
    params: queryParams,
    enabled: !wsLoading && isValidMission,
    onEntry: handleLive,
  })

  const checkpointCount = entries.filter((e) => e.entry_type === "checkpoint.created").length

  if (!isValidMission) {
    return (
      <div className="flex flex-col items-center gap-2 py-24 text-center">
        <div className="w-10 h-10 rounded-lg bg-muted/50 flex items-center justify-center">
          <Map className="h-4 w-4 text-muted-foreground/60" />
        </div>
        <div className="text-sm font-medium text-foreground/80">No mission selected</div>
        <div className="text-[11px] text-muted-foreground max-w-sm">
          Open a mission&apos;s timeline from its detail view.
        </div>
      </div>
    )
  }

  return (
    <div className="p-4 md:p-6 space-y-4 max-w-4xl">
      <header className="flex items-center justify-between gap-3 flex-wrap">
        <div className="flex items-center gap-2">
          <Button variant="ghost" size="sm" className="h-7 px-2 text-xs" asChild>
            <Link href="/orchestration">
              <ArrowLeft className="h-3 w-3 mr-1" /> Back
            </Link>
          </Button>
          <Map className="h-4 w-4 text-foreground/60" />
          <h1 className="text-body font-medium text-foreground/80">Mission timeline</h1>
          <Badge variant="outline" className="text-[10px] font-mono border-border/60">
            {missionId.slice(0, 8)}
          </Badge>
          <Badge variant="outline" className="text-[10px] border-border/60">
            {entries.length} events
          </Badge>
          {checkpointCount > 0 && (
            <Badge variant="outline" className="text-[10px] bg-amber-500/15 text-amber-300 border-amber-500/40">
              {checkpointCount} checkpoints
            </Badge>
          )}
        </div>
        <Button
          variant="outline"
          size="sm"
          className="h-7 px-2.5 text-xs"
          onClick={() => refresh()}
          disabled={loading}
        >
          <RefreshCw className={cn("h-3 w-3 mr-1.5", loading && "animate-spin")} />
          Refresh
        </Button>
      </header>

      <MissionTimeline missionId={missionId} entries={entries} loading={loading} error={error} />
    </div>
  )
}
