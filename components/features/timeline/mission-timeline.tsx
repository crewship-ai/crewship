"use client"

import { useState } from "react"
import { BookOpen, AlertCircle } from "lucide-react"
import { Spinner } from "@/components/ui/spinner"
import { JournalEntryCard } from "@/components/features/journal/journal-entry-card"
import { CheckpointMarker } from "./checkpoint-marker"
import { ForkDialog } from "./fork-dialog"
import type { JournalEntry } from "@/lib/types/journal"

interface MissionTimelineProps {
  missionId: string
  entries: JournalEntry[]
  loading: boolean
  error: string | null
}

/**
 * Vertical timeline of mission events. Checkpoints render as amber milestone
 * markers; regular entries re-use the journal entry card so styling stays
 * consistent with the main journal view.
 */
export function MissionTimeline({ missionId, entries, loading, error }: MissionTimelineProps) {
  const [forkOpen, setForkOpen] = useState(false)
  const [forkTarget, setForkTarget] = useState<JournalEntry | null>(null)

  function openFork(entry: JournalEntry) {
    setForkTarget(entry)
    setForkOpen(true)
  }

  if (loading && entries.length === 0) {
    return (
      <div className="flex items-center justify-center py-16 text-muted-foreground">
        <Spinner className="h-4 w-4 mr-2" /> Loading timeline…
      </div>
    )
  }

  if (error && entries.length === 0) {
    return (
      <div className="flex flex-col items-center gap-2 py-16 text-center">
        <div className="w-10 h-10 rounded-lg bg-red-500/10 flex items-center justify-center">
          <AlertCircle className="h-4 w-4 text-red-400" />
        </div>
        <div className="text-sm text-foreground/80">Couldn&apos;t load timeline</div>
        <div className="text-[11px] text-muted-foreground max-w-sm">{error}</div>
      </div>
    )
  }

  if (entries.length === 0) {
    return (
      <div className="flex flex-col items-center gap-2 py-16 text-center">
        <div className="w-10 h-10 rounded-lg bg-muted/50 flex items-center justify-center">
          <BookOpen className="h-4 w-4 text-muted-foreground/60" />
        </div>
        <div className="text-sm font-medium text-foreground/80">No events yet</div>
        <div className="text-[11px] text-muted-foreground max-w-sm">
          This mission hasn&apos;t emitted any journal entries.
        </div>
      </div>
    )
  }

  // Timeline is rendered oldest-first so the story reads top → bottom.
  const chronological = [...entries].reverse()

  const forkLabel =
    forkTarget && typeof forkTarget.payload?.label === "string"
      ? (forkTarget.payload.label as string)
      : undefined
  const forkCheckpointId =
    forkTarget && typeof forkTarget.payload?.checkpoint_id === "string"
      ? (forkTarget.payload.checkpoint_id as string)
      : (forkTarget?.id ?? null)

  return (
    <>
      <div className="relative border-l-2 border-border/50 ml-2 pl-0 space-y-3">
        {chronological.map((entry) => {
          if (entry.entry_type === "checkpoint.created") {
            return <CheckpointMarker key={entry.id} entry={entry} onFork={openFork} />
          }
          return (
            <div key={entry.id} className="relative pl-6">
              <span
                aria-hidden
                className="absolute left-[-5px] top-3 w-2.5 h-2.5 rounded-full bg-border border border-background"
              />
              <JournalEntryCard entry={entry} />
            </div>
          )
        })}
      </div>

      <ForkDialog
        open={forkOpen}
        onOpenChange={setForkOpen}
        missionId={missionId}
        checkpointId={forkCheckpointId}
        checkpointLabel={forkLabel}
      />
    </>
  )
}
