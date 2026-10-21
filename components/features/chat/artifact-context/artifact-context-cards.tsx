"use client"
import { useState } from "react"
import { toast } from "@/lib/toast"
import { Button } from "@/components/ui/button"
import { postWorkCardCommand, type WorkCardCommand } from "./client"
import type { LinkedViewerTarget } from "./linked-viewer-targets"
import type { WorkCardActionId, WorkCardModel } from "./types"
import { useArtifactContextEnabled } from "./use-artifact-context-enabled"
import { useWorkCards } from "./use-artifact-context-data"
import { WorkResultCard } from "./work-result-card"

export type OpenLinked = (target: LinkedViewerTarget, title: string | null, opener: HTMLElement | null) => void

interface Props { workspaceId: string; chatId: string; currentUserId: string | null | undefined; onOpen: OpenLinked }

const COMMAND: Partial<Record<WorkCardActionId, WorkCardCommand>> = { cancel: "cancel", retry: "retry", check_result: "check-result" }

/** Agent work results of this conversation, from the server projection. Renders nothing while the flag is off or the endpoint is not served. */
export function ArtifactContextCards({ workspaceId, chatId, currentUserId, onOpen }: Props) {
  const enabled = useArtifactContextEnabled()
  const { cards, result, stale, refetch } = useWorkCards({ workspaceId, chatId, currentUserId })
  const [dismissed, setDismissed] = useState<Set<string>>(new Set())
  if (!enabled || !result) return null
  if (result.status === "error" && cards.length === 0) return <section aria-label="Agent work results" className="mx-auto my-4 w-full max-w-3xl">
    <div role="alert" className="space-y-2 rounded-xl border p-4 text-sm">
      <p>Temporarily unavailable. Agent work results could not be refreshed. This is not a removal of access.</p>
      <Button type="button" size="sm" variant="outline" className="coarse:min-h-[44px]" onClick={() => void refetch()}>Try again</Button>
    </div></section>
  const visible = cards.filter(entry => !dismissed.has(entry.model.id))
  if (visible.length === 0) return null

  async function act(action: WorkCardActionId, card: WorkCardModel, trigger: HTMLButtonElement) {
    const entry = cards.find(item => item.model.id === card.id)
    if (action === "open") { if (entry?.target) onOpen(entry.target, entry.title, trigger); return }
    if (action === "dismiss") { setDismissed(previous => new Set(previous).add(card.id)); return }
    const command = COMMAND[action]
    if (command) {
      const outcome = await postWorkCardCommand(workspaceId, chatId, card.id, command)
      if (outcome.status === "ok") toast.success("Done.")
      else if (outcome.status === "absent") setDismissed(previous => new Set(previous).add(card.id))
      else toast.error(outcome.status === "unsupported" ? "This action is not available on this server yet." : "That did not go through. Check the result before trying again.")
    }
    void refetch()
  }
  return <section aria-label="Agent work results" className="mx-auto my-4 w-full max-w-3xl space-y-3">
    <h3 className="text-sm font-medium">Agent work</h3>
    {stale && <p role="status" className="text-xs text-muted-foreground">Results could not refresh. Showing the last known state; this is not a removal of access.</p>}
    <div className="grid gap-3 sm:grid-cols-2">{visible.map(entry => <WorkResultCard key={entry.model.id} card={stale ? { ...entry.model, load: "unavailable" } : entry.model} onAction={act} />)}</div>
  </section>
}
