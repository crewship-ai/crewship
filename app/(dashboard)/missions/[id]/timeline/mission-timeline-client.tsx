"use client"

import { useCallback, useMemo, useState } from "react"
import Link from "next/link"
import { useRouter } from "next/navigation"
import { ArrowLeft, ChevronRight, Flag, Map, RefreshCw } from "lucide-react"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card"
import {
  Sheet,
  SheetContent,
  SheetDescription,
  SheetHeader,
  SheetTitle,
} from "@/components/ui/sheet"
import { cn } from "@/lib/utils"
import { useJournalList } from "@/hooks/use-journal-list"
import { useJournalStream } from "@/hooks/use-journal-stream"
import { useWorkspace } from "@/hooks/use-workspace"
import { useUrlSegment } from "@/lib/use-url-segment"
import { MissionTimeline } from "@/components/features/timeline/mission-timeline"
import { formatDateTime, formatRelativeTime } from "@/lib/time"
import type { JournalEntry } from "@/lib/types/journal"

/**
 * Mission timeline view. Reads the journal filtered by `mission_id` and
 * renders a vertical story with checkpoints as milestone markers.
 *
 * Layout pattern: "Detail page with breadcrumbs" (single entity focus).
 * Mirrors `app/(dashboard)/orchestration/issues/[identifier]/issue-detail-client.tsx`:
 * breadcrumb top bar, centered max-width content, right-side Sheet for
 * checkpoint metadata. See `docs/design/patterns.md` #5.
 */
// Mission id read from the URL, not useParams() — avoids the static-export
// "_" placeholder bug (see useUrlSegment). The id is the middle segment of
// /missions/<id>/timeline.
const MISSION_TIMELINE_RE = /^\/missions\/([^/]+)\/timeline\/?$/

export function MissionTimelineClient() {
  const router = useRouter()
  const missionId = useUrlSegment(MISSION_TIMELINE_RE) ?? ""
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

  const checkpoints = useMemo<JournalEntry[]>(
    () => entries.filter((e) => e.entry_type === "checkpoint.created"),
    [entries],
  )

  // Right-side sheet state — kept in sync with "most recent checkpoint"
  // so the page always has something meaningful to show on load.
  const [sheetOpen, setSheetOpen] = useState(false)
  const [selectedCheckpointId, setSelectedCheckpointId] = useState<string | null>(null)
  const selectedCheckpoint = useMemo(
    () => checkpoints.find((c) => c.id === selectedCheckpointId) ?? null,
    [checkpoints, selectedCheckpointId],
  )

  const openCheckpoint = useCallback((entry: JournalEntry) => {
    setSelectedCheckpointId(entry.id)
    setSheetOpen(true)
  }, [])

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
    <div className="h-[calc(100vh-48px)] flex flex-col bg-background">
      {/* ---- Breadcrumb top bar (h-9, matches orchestration detail pages) ---- */}
      <div className="shrink-0 z-20 flex items-center h-9 bg-card border-b border-border/60 px-2 sm:px-3 gap-2 overflow-x-auto [&::-webkit-scrollbar]:hidden [-ms-overflow-style:none] [scrollbar-width:none]">
        <Button
          variant="ghost"
          size="sm"
          className="h-7 px-2 text-xs shrink-0"
          onClick={() => router.push("/activity")}
          aria-label="Back to activity"
        >
          <ArrowLeft className="h-3 w-3 mr-1" /> Back
        </Button>

        <nav aria-label="Breadcrumb" className="flex items-center gap-1 text-xs text-muted-foreground min-w-0">
          <Link href="/activity" className="hover:text-foreground transition-colors shrink-0">
            Activity
          </Link>
          <ChevronRight className="h-3 w-3 shrink-0 opacity-60" />
          <Link href={`/missions/${missionId}`} className="hover:text-foreground transition-colors shrink-0">
            Missions
          </Link>
          <ChevronRight className="h-3 w-3 shrink-0 opacity-60" />
          <Map className="h-3 w-3 text-foreground/60 shrink-0" />
          <span className="text-foreground/80 font-medium truncate">Timeline</span>
        </nav>

        <Badge variant="outline" className="text-[10px] font-mono border-border/60 shrink-0">
          {missionId.slice(0, 8)}
        </Badge>
        <Badge variant="outline" className="text-[10px] border-border/60 shrink-0">
          {entries.length} events
        </Badge>
        {checkpoints.length > 0 && (
          <button
            type="button"
            onClick={() => {
              if (checkpoints[0]) openCheckpoint(checkpoints[0])
            }}
            className="shrink-0"
          >
            <Badge variant="outline" className="gap-1 text-[10px] bg-amber-500/15 text-amber-300 border-amber-500/40 cursor-pointer">
              <Flag className="h-3 w-3" />
              {checkpoints.length} checkpoints
            </Badge>
          </button>
        )}

        <div className="flex-1" />

        <Button
          variant="outline"
          size="sm"
          className="h-7 px-2.5 text-xs shrink-0"
          onClick={() => refresh()}
          disabled={loading}
        >
          <RefreshCw className={cn("h-3 w-3 mr-1.5", loading && "animate-spin")} />
          Refresh
        </Button>
      </div>

      {/* ---- Main content (centered max-w, like issue detail) ---- */}
      <div className="flex-1 min-h-0 overflow-y-auto">
        <div className="max-w-4xl mx-auto px-4 md:px-6 py-5 space-y-4">
          <Card className="py-0 gap-0 overflow-hidden">
            <CardHeader className="px-4 py-2 border-b border-border/50">
              <CardTitle className="text-[12px] font-semibold uppercase tracking-wider text-muted-foreground flex items-center gap-2">
                <Map className="h-3.5 w-3.5" />
                Mission timeline
              </CardTitle>
            </CardHeader>
            <CardContent className="p-4">
              <MissionTimeline missionId={missionId} entries={entries} loading={loading} error={error} />
            </CardContent>
          </Card>

          {checkpoints.length > 0 && (
            <Card className="py-0 gap-0 overflow-hidden">
              <CardHeader className="px-4 py-2 border-b border-border/50">
                <CardTitle className="text-[12px] font-semibold uppercase tracking-wider text-muted-foreground flex items-center gap-2">
                  <Flag className="h-3.5 w-3.5" />
                  Checkpoints
                </CardTitle>
              </CardHeader>
              <CardContent className="p-0">
                <ul className="divide-y divide-border/40">
                  {checkpoints.map((cp) => (
                    <li key={cp.id}>
                      <button
                        type="button"
                        onClick={() => openCheckpoint(cp)}
                        className="w-full flex items-center gap-3 px-4 py-2 text-left hover:bg-accent/40 transition-colors"
                      >
                        <Flag className="h-3 w-3 text-amber-400 shrink-0" />
                        <span className="text-[12px] text-foreground/80 truncate flex-1">
                          {cp.summary || "Checkpoint"}
                        </span>
                        <span className="text-[11px] text-muted-foreground font-mono tabular-nums shrink-0">
                          {formatRelativeTime(cp.ts)}
                        </span>
                      </button>
                    </li>
                  ))}
                </ul>
              </CardContent>
            </Card>
          )}
        </div>
      </div>

      {/* ---- Right-side Sheet for checkpoint detail (replaces inline expansion) ---- */}
      <Sheet open={sheetOpen} onOpenChange={setSheetOpen}>
        <SheetContent className="sm:max-w-md w-full">
          {selectedCheckpoint && (
            <>
              <SheetHeader>
                <SheetTitle className="text-sm font-medium flex items-center gap-2">
                  <Flag className="h-3.5 w-3.5 text-amber-400" />
                  Checkpoint
                  <span className="text-[10px] font-mono text-muted-foreground tabular-nums">
                    {selectedCheckpoint.id.slice(0, 8)}
                  </span>
                </SheetTitle>
                <SheetDescription className="text-xs">
                  Created {formatDateTime(selectedCheckpoint.ts)}
                </SheetDescription>
              </SheetHeader>
              <div className="px-4 space-y-3 overflow-y-auto flex-1 min-h-0 pb-4">
                <div>
                  <div className="text-[10px] uppercase tracking-wider text-muted-foreground/80 font-semibold mb-1">
                    Summary
                  </div>
                  <p className="text-[12px] text-foreground/80">{selectedCheckpoint.summary || "—"}</p>
                </div>
                {selectedCheckpoint.payload && Object.keys(selectedCheckpoint.payload).length > 0 && (
                  <div>
                    <div className="text-[10px] uppercase tracking-wider text-muted-foreground/80 font-semibold mb-1">
                      Payload
                    </div>
                    <pre className="max-h-64 overflow-auto rounded border border-border/50 bg-muted/30 p-2 text-[10px] font-mono text-muted-foreground">
                      {JSON.stringify(selectedCheckpoint.payload, null, 2)}
                    </pre>
                  </div>
                )}
              </div>
            </>
          )}
        </SheetContent>
      </Sheet>
    </div>
  )
}
