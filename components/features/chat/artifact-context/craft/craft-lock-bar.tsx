"use client"
import { Check, ExternalLink, Lock, Pencil } from "lucide-react"
import { Button } from "@/components/ui/button"
import { cn } from "@/lib/utils"
import { useCraftLeaseState } from "./use-craft-lease"
import type { CraftLeaseHolder } from "../projects/types"

interface CraftLockBarProps { workspaceId: string; slug: string; onEdit: () => void }

/** The page route that mounts the WordCraft editor or the GridCraft workbook; an `@<page id>` reference opens by id. */
export function craftEditorHref(workspaceId: string, slug: string): string {
  return slug.startsWith("@")
    ? `/pages?${new URLSearchParams({ ref: slug.slice(1), workspace_id: workspaceId })}`
    : `/pages/${encodeURIComponent(slug)}?${new URLSearchParams({ workspace_id: workspaceId })}`
}

const holderName = (holder: CraftLeaseHolder) => holder.kind === "agent" ? `${holder.display_name} (agent)` : holder.display_name

const BTN = "coarse:min-h-[44px]"

/**
 * Who holds the Craft edit lease, and the one action that makes sense from
 * here. Renders nothing until the lease surface is served, so a workspace
 * without it never sees a lock it cannot use. Chat never takes the lease:
 * Edit opens the editor, which acquires on its own admission. Release and
 * Save version stay in the editor.
 */
export function CraftLockBar(props: CraftLockBarProps) {
  return <CraftLockBarView {...props} lease={useCraftLeaseState(props.workspaceId, props.slug)} />
}

/** The bar for a host that already reads the lease, so one surface keeps one admission. */
export function CraftLockBarView({ workspaceId, slug, onEdit, lease }: CraftLockBarProps & { lease: ReturnType<typeof useCraftLeaseState> }) {
  const { status, requestHandoff, requestedGeneration, heldByMe, requestedByMe } = lease
  if (!status) return null

  const { holder, can_write: canWrite } = status
  const state = !holder ? "free" : heldByMe || status.mine ? "mine" : "held"
  // Keyed by generation: a new holder (or the same holder re-acquiring) is a
  // new lease, and the earlier request no longer applies to it.
  const requested = requestedByMe || (requestedGeneration !== null && requestedGeneration === status.generation)
  const title = state === "free" ? "Free" : state === "mine" ? "Editing: you" : `Editing: ${holderName(holder!)}`
  const detail = state === "free" ? "nobody is editing"
    : state === "mine" ? "in the editor"
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
      : state === "free" ? <Button type="button" size="sm" className={BTN} onClick={onEdit}><Pencil aria-hidden />Edit</Button>
      : state === "mine" ? <Button asChild size="sm" variant="ghost" className={BTN}>
        <a href={craftEditorHref(workspaceId, slug)} target="_blank" rel="noopener noreferrer"><ExternalLink aria-hidden />Open in new tab</a>
      </Button>
      : !requested && <>
        {requestHandoff.isError && <span className="text-xs text-destructive">Could not request edit</span>}
        <Button type="button" size="sm" variant="secondary" className={BTN} disabled={requestHandoff.isPending}
          onClick={() => requestHandoff.mutate()}>Request edit</Button>
      </>}
  </div>
}
