"use client"

import * as React from "react"
import { RotateCcw } from "lucide-react"

import { Button } from "@/components/ui/button"
import { toast } from "@/lib/toast"
import { useReplayWorkItem, useWorkItem, replayAvailability } from "@/hooks/use-work-items"
import { WorkReplayDialog } from "@/components/features/work/work-replay-dialog"

/**
 * Replay of one inbound call (INT-08), through POST
 * /work-items/{id}/replay — the same route and dialog the Work queue uses.
 * The work item is read only when the dialog opens. Outbound "Send again"
 * has no route (#3446) and is not drawn.
 */
export function ReceiptReplay({
  workspaceId,
  workId,
  expiresAt,
}: {
  workspaceId: string
  workId: string
  expiresAt: string | null
}) {
  const [open, setOpen] = React.useState(false)
  const [error, setError] = React.useState<string | null>(null)
  const { item } = useWorkItem(workspaceId, open ? workId : null)
  const replay = useReplayWorkItem(workspaceId, {
    onCreated: () => {
      setOpen(false)
      setError(null)
      toast.success("Replay queued as new work")
    },
    onError: (err) => setError(err instanceof Error ? err.message : "Replay failed."),
  })
  return (
    <>
      <Button type="button" variant="outline" size="sm" className="coarse:h-[44px]" onClick={() => setOpen(true)}>
        <RotateCcw className="size-3" aria-hidden />
        Replay
      </Button>
      {open && item && (
        <WorkReplayDialog
          open
          onOpenChange={setOpen}
          item={item}
          // The call is listed because its payload is retained; the server
          // still refuses a work item that has not finished.
          availability={replayAvailability(item, {
            delivery: { id: item.source_ref, raw_body_available: true, raw_body_expires_at: expiresAt } as never,
            loading: false,
            notFound: false,
          })}
          pending={replay.isPending}
          error={error}
          onConfirm={({ reason, targetRevision }) => replay.mutate({ workItemId: workId, reason, targetRevision })}
        />
      )}
    </>
  )
}
