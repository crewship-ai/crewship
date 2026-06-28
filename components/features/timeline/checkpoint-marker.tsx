"use client"

import { useState } from "react"
import { toast } from "sonner"
import { Flag, GitBranch, MoreHorizontal, RotateCcw } from "lucide-react"
import { Spinner } from "@/components/ui/spinner"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu"
import { formatRelativeTime } from "@/lib/time"
import { apiFetch } from "@/lib/api-fetch"
import type { JournalEntry } from "@/lib/types/journal"

interface CheckpointMarkerProps {
  entry: JournalEntry
  onFork: (entry: JournalEntry) => void
}

/**
 * Milestone marker for `checkpoint.created` entries. Renders with a larger
 * flag icon and an actions dropdown — fork opens the dialog, restore POSTs
 * to /api/v1/checkpoints/{id}/restore. The endpoint is wired so 404 now
 * means "checkpoint not found" rather than "feature not implemented".
 */
export function CheckpointMarker({ entry, onFork }: CheckpointMarkerProps) {
  const [restoring, setRestoring] = useState(false)

  const label =
    typeof entry.payload?.label === "string"
      ? (entry.payload.label as string)
      : typeof entry.payload?.name === "string"
        ? (entry.payload.name as string)
        : "Checkpoint"

  const checkpointId =
    typeof entry.payload?.checkpoint_id === "string"
      ? (entry.payload.checkpoint_id as string)
      : entry.id

  async function handleRestore() {
    setRestoring(true)
    try {
      const res = await apiFetch(`/api/v1/checkpoints/${encodeURIComponent(checkpointId)}/restore`, { method: "POST" })
      if (res.status === 404) {
        toast.error("Checkpoint not found or restore unavailable")
      } else if (!res.ok) {
        toast.error(`Restore failed (${res.status})`)
      } else {
        toast.success("Mission restored to checkpoint")
      }
    } catch {
      toast.error("Restore failed")
    } finally {
      setRestoring(false)
    }
  }

  return (
    <div className="relative flex gap-3 pl-6">
      <span
        aria-hidden
        className="absolute left-[-7px] top-2.5 w-4 h-4 rounded-full bg-amber-500/20 border-2 border-amber-400 flex items-center justify-center"
      >
        <Flag className="h-2 w-2 text-amber-300" />
      </span>

      <div className="flex-1 min-w-0 rounded-lg border-2 border-amber-500/40 bg-amber-500/5 px-3 py-2">
        <div className="flex items-center gap-2 flex-wrap">
          <Badge className="gap-1 bg-amber-500/20 text-amber-300 border border-amber-500/40">
            <Flag className="h-3 w-3" /> Checkpoint
          </Badge>
          <span className="text-sm font-medium text-foreground">{label}</span>
          <span className="ml-auto text-[11px] text-muted-foreground font-mono tabular-nums">
            {formatRelativeTime(entry.ts)}
          </span>
          <DropdownMenu>
            <DropdownMenuTrigger asChild>
              <Button variant="ghost" size="sm" className="h-6 w-6 p-0">
                <MoreHorizontal className="h-3 w-3" />
                <span className="sr-only">Actions</span>
              </Button>
            </DropdownMenuTrigger>
            <DropdownMenuContent align="end">
              <DropdownMenuItem onClick={() => onFork(entry)}>
                <GitBranch className="h-3 w-3 mr-1.5" />
                Fork from here
              </DropdownMenuItem>
              <DropdownMenuItem onClick={handleRestore} disabled={restoring}>
                {restoring ? (
                  <Spinner className="h-3 w-3 mr-1.5" />
                ) : (
                  <RotateCcw className="h-3 w-3 mr-1.5" />
                )}
                Restore
              </DropdownMenuItem>
            </DropdownMenuContent>
          </DropdownMenu>
        </div>
        {entry.summary && (
          <p className="mt-1 text-[12px] text-muted-foreground leading-snug">{entry.summary}</p>
        )}
      </div>
    </div>
  )
}
