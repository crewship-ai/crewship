"use client"
import { useState } from "react"
import { Check, ExternalLink, Lock, Pencil } from "lucide-react"
import { Button } from "@/components/ui/button"
import { cn } from "@/lib/utils"
import { useCraftAvailable, useCraftLease, useCraftLeaseActions } from "./use-craft-lease"
import type { CraftLeaseHolder } from "../projects/types"

interface CraftLockBarProps { workspaceId: string; slug: string; onEdit: () => void }

/** The page route that mounts the WordCraft editor or the GridCraft workbook for this slug. */
export function craftEditorHref(workspaceId: string, slug: string): string {
  return `/pages/${encodeURIComponent(slug)}?${new URLSearchParams({ workspace_id: workspaceId })}`
}

const holderName = (holder: CraftLeaseHolder) => holder.kind === "agent" ? `${holder.display_name} (agent)` : holder.display_name

const BTN = "coarse:min-h-[44px]"

/**
 * Who holds the Craft edit lease, and the one action that makes sense from
 * here. Renders nothing until the lease surface is served, so a workspace
 * without it never sees a lock it cannot use. Release and Save version are
 * left out until the lease API offers them.
 */
export function CraftLockBar({ workspaceId, slug, onEdit }: CraftLockBarProps) {
  const available = useCraftAvailable(workspaceId, slug)
  const lease = useCraftLease(workspaceId, available ? slug : null)
  const { acquire, requestHandoff } = useCraftLeaseActions(workspaceId, slug)
  // Keyed by generation: a new holder (or the same holder re-acquiring) is a
  // new lease, and the earlier request no longer applies to it.
  const [requestedGeneration, setRequestedGeneration] = useState<number | null>(null)
  const status = lease.data
  if (!available || !status) return null

  const { holder, mine, can_write: canWrite } = status
  const state = !holder ? "free" : mine ? "mine" : "held"
  const requested = requestedGeneration === status.generation
  const title = state === "free" ? "Free" : state === "mine" ? "Editing: you" : `Editing: ${holderName(holder!)}`
  const detail = state === "free" ? "nobody is editing"
    : state === "mine" ? `generation ${status.generation} · held while this tab stays connected`
    : requested ? `Request sent to ${holder!.display_name} · they can hand over or decline`
    : "you see a read-only preview"

  return <div data-testid="craft-lock-bar" data-state={state}
    className={cn("flex flex-wrap items-center gap-2 border-b px-3 py-2 text-sm", state === "mine" ? "bg-primary/10" : "bg-muted")}>
    {/* Only the holder line changes under the reader; actions stay out of the live region. */}
    <span role="status" aria-live="polite" className="flex min-w-0 flex-[1_1_200px] items-center gap-1.5">
      {state === "free" ? <Check aria-hidden className="size-4 shrink-0 text-success" />
        : <Lock aria-hidden className={cn("size-4 shrink-0", state === "mine" ? "text-primary" : "text-warn")} />}
      <b className="font-semibold">{title}</b>
      <small className="min-w-0 truncate text-xs text-muted-foreground">{detail}</small>
    </span>
    {!canWrite ? <span className="text-xs text-muted-foreground">Read only</span>
      : state === "free" ? <>
        {acquire.isError && <span className="text-xs text-destructive">Could not start editing</span>}
        <Button type="button" size="sm" className={BTN} disabled={acquire.isPending}
          onClick={() => acquire.mutate(undefined, { onSuccess: () => onEdit() })}><Pencil aria-hidden />Edit</Button>
      </>
      : state === "mine" ? <Button asChild size="sm" variant="ghost" className={BTN}>
        <a href={craftEditorHref(workspaceId, slug)} target="_blank" rel="noopener noreferrer"><ExternalLink aria-hidden />Open in new tab</a>
      </Button>
      : !requested && <>
        {requestHandoff.isError && <span className="text-xs text-destructive">Could not request edit</span>}
        <Button type="button" size="sm" variant="secondary" className={BTN} disabled={requestHandoff.isPending}
          onClick={() => { const generation = status.generation; requestHandoff.mutate(undefined, { onSuccess: () => setRequestedGeneration(generation) }) }}>Request edit</Button>
      </>}
  </div>
}
