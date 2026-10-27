"use client"
import { Bot, Layers, User } from "lucide-react"
import { toast } from "@/lib/toast"
import { Button } from "@/components/ui/button"
import { useCraftProposalActions } from "./use-craft-lease"
import type { CraftProposal } from "../projects/types"

interface CraftProposalCardProps {
  workspaceId: string
  slug: string
  proposal: CraftProposal
  onCompare: (proposal: CraftProposal) => void
}

const GUARANTEE = "the file stays as it is until you accept"
const BTN = "coarse:min-h-[44px]"

/**
 * A suggested change to a Craft document; nothing lands until a human
 * accepts it. The design's "N changes" needs a count the proposal does not
 * carry, so the summary takes that place.
 */
export function CraftProposalCard({ workspaceId, slug, proposal, onCompare }: CraftProposalCardProps) {
  const { accept, reject } = useCraftProposalActions(workspaceId, slug)
  const author = proposal.author.display_name
  const base = `v${proposal.base_version}`
  if (proposal.state === "accepted" || proposal.state === "rejected") {
    return <p className="text-xs text-muted-foreground">{author}&apos;s proposal was {proposal.state}</p>
  }
  const stale = proposal.state === "stale"
  const pending = accept.isPending || reject.isPending
  const Avatar = proposal.author.kind === "agent" ? Bot : User
  return <article aria-label={`${author} prepared a proposal on ${base}`} aria-busy={pending || undefined}
    className="flex min-w-0 flex-wrap items-center gap-2.5 rounded-card border bg-card px-3 py-2.5 text-sm">
    <span aria-hidden className="grid size-7 shrink-0 place-items-center rounded-full bg-muted"><Avatar className="size-4 text-muted-foreground" /></span>
    <span className="grid min-w-0 flex-[1_1_180px]">
      <b className="font-semibold">{author} prepared a proposal on {base}</b>
      <small className="text-xs text-muted-foreground">{proposal.summary ? `${proposal.summary} · ${GUARANTEE}` : GUARANTEE[0].toUpperCase() + GUARANTEE.slice(1)}</small>
      {stale && <small className="text-xs text-warn">Out of date — the document changed</small>}
      {(accept.isError || reject.isError) && <small role="alert" className="text-xs text-destructive">Could not apply your decision. Try again.</small>}
    </span>
    <span className="flex shrink-0 flex-wrap gap-1">
      <Button type="button" size="sm" variant="ghost" className={BTN} onClick={() => onCompare(proposal)}><Layers aria-hidden />Compare</Button>
      {!stale && <>
        <Button type="button" size="sm" variant="ghost" className={BTN} disabled={pending}
          onClick={() => reject.mutate(proposal.id, { onSuccess: () => toast.success(`Proposal rejected · ${author} was told`) })}>Reject</Button>
        <Button type="button" size="sm" className={BTN} disabled={pending}
          onClick={() => accept.mutate(proposal.id, { onSuccess: () => toast.success(`Accepted · saved as a new version after ${base}`) })}>Accept</Button>
      </>}
    </span>
  </article>
}
