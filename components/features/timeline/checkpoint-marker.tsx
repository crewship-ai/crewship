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
import { checkpointIdOf, checkpointLabelOf } from "./checkpoint-ref"
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

  const label = checkpointLabelOf(entry) ?? "Checkpoint"
  // Same resolution the fork path uses — the checkpoint id lives in refs, and
  // `entry.id` (the journal row) is not a substitute for it. Null means the
  // entry can't address a checkpoint, so restore is disabled rather than
  // firing a request that can only 404.
  const checkpointId = checkpointIdOf(entry)

  async function handleRestore() {
    if (!checkpointId) return
    setRestoring(true)
    try {
      const res = await apiFetch(`/api/v1/checkpoints/${encodeURIComponent(checkpointId)}/restore`, { method: "POST" })
      if (res.status === 404) {
        toast.error("Checkpoint not found or restore unavailable")
      } else if (!res.ok) {
        toast.error(`Restore preview failed (${res.status})`)
      } else {
        // Say what the endpoint DID, which is not what this used to claim.
        //
        // It reported "Mission restored to checkpoint". POST
        // /checkpoints/{id}/restore does not restore anything —
        // cartographer.Restore says so in its own doc comment ("no DB rows are
        // mutated, no containers are torn down, no memory is rewound") and
        // journals the attempt as "restore preview for checkpoint …". The
        // rewind is deferred to a handler that does not exist yet.
        //
        // So the toast reports the one thing the call actually produces: the
        // divergence list, which is what a real restore would have to abandon.
        // Throwing that away AND claiming success was the worst of both.
        const body = (await res.json().catch(() => null)) as { warn_divergence?: unknown } | null
        const diverged = Array.isArray(body?.warn_divergence) ? body.warn_divergence.length : 0
        toast.info("Restore preview — nothing has been rewound", {
          description: diverged > 0
            ? `Restoring here would abandon ${diverged} later event${diverged === 1 ? "" : "s"}. Rewinding is not implemented yet.`
            : "No later events to abandon. Rewinding is not implemented yet.",
        })
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
        className="absolute left-[-7px] top-2.5 w-4 h-4 rounded-full bg-warn/20 border-2 border-warn flex items-center justify-center"
      >
        <Flag className="h-2 w-2 text-warn" />
      </span>

      <div className="flex-1 min-w-0 rounded-lg border-2 border-warn/40 bg-warn/5 px-3 py-2">
        <div className="flex items-center gap-2 flex-wrap">
          <Badge className="gap-1 bg-warn/20 text-warn border border-warn/40">
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
              <DropdownMenuItem onClick={handleRestore} disabled={restoring || !checkpointId}>
                {restoring ? (
                  <Spinner className="h-3 w-3 mr-1.5" />
                ) : (
                  <RotateCcw className="h-3 w-3 mr-1.5" />
                )}
                Preview restore
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
