"use client"

import { useRef, useState } from "react"
import { Button } from "@/components/ui/button"
import { formatDateTime } from "@/lib/time"
import type { DocumentProposalDetail, ProposalDecision } from "@/lib/documents/proposals"
import { DocumentCompare } from "../document-history/document-compare"

const RESULT: Record<Exclude<ProposalDecision["status"], "done">, string> = {
  conflict: "Nothing was applied. The document changed since this proposal was made; the whole proposal is refused.",
  denied: "You no longer have access to decide on this proposal.",
  unavailable: "The service is unavailable. Nothing was applied; try again later.",
  unknown: "The result is unknown: the request may not have completed. Check the document history before trying again. Trying again sends the same request, so it cannot apply twice.",
  error: "The request was rejected. Nothing was applied.",
}
const STATE: Record<DocumentProposalDetail["status"], string> = { open: "Open", accepted: "Accepted", rejected: "Rejected", conflict: "Conflict", stale: "Out of date" }
type Kind = "accept" | "reject"

/** Compare, accept or reject one proposal. The proposal never appears in the live document before accept. */
export function ProposalReview({ proposal, headRev, reviewedRev, canDecide, hasLocalDraft, onDownloadDraft, onAccept, onReject }: {
  proposal: DocumentProposalDetail
  /** Live confirmed head. */
  headRev: number
  /** Head revision when this proposal was opened; sent as the expected revision. */
  reviewedRev: number
  canDecide: boolean
  hasLocalDraft: boolean
  onDownloadDraft: () => void
  onAccept: (requestId: string) => Promise<ProposalDecision>
  onReject: (requestId: string) => Promise<ProposalDecision>
}) {
  const [busy, setBusy] = useState(false)
  const [confirmReject, setConfirmReject] = useState(false)
  const [outcome, setOutcome] = useState<ProposalDecision>()
  const attempt = useRef<{ kind: Kind; id: string }>(undefined)
  const open = proposal.status === "open" && outcome?.status !== "done"
  // Accepting reloads the editor: unsent edits must be confirmed or downloaded first.
  const needsChoice = hasLocalDraft
  async function decide(kind: Kind) {
    if (busy || (kind === "accept" && needsChoice)) return
    setBusy(true); setOutcome(undefined)
    if (attempt.current?.kind !== kind) attempt.current = { kind, id: crypto.randomUUID() }
    const current = attempt.current
    const result = await (kind === "accept" ? onAccept(current.id) : onReject(current.id))
    setBusy(false); setOutcome(result)
    if (result.status !== "unknown") { attempt.current = undefined; setConfirmReject(false) }
  }
  const scope = proposal.target.kind === "document" ? "the whole document" : `${proposal.target.blockIds.length} block${proposal.target.blockIds.length === 1 ? "" : "s"}`
  const retry = outcome?.status === "unknown" ? attempt.current?.kind : undefined
  return <section aria-label={`Proposal from ${proposal.author}`} className="space-y-3 rounded-lg border p-4">
    <div className="space-y-1">
      <h3 className="font-semibold">Proposal from {proposal.author}</h3>
      <p className="text-sm text-muted-foreground">{STATE[proposal.status]} · changes {scope} · based on revision {proposal.baseRev} · <time dateTime={proposal.createdAt}>{Number.isFinite(Date.parse(proposal.createdAt)) ? formatDateTime(proposal.createdAt) : "Date unavailable"}</time></p>
    </div>
    {proposal.baseRev !== headRev && open && <p role="status">The document has moved on since revision {proposal.baseRev}. Accepting is refused if the changed text overlaps.</p>}
    {headRev !== reviewedRev && open && <p role="status">The document changed while you were reviewing (revision {reviewedRev} to {headRev}). Accepting names revision {reviewedRev}, so the server refuses it if it no longer matches.</p>}
    <DocumentCompare base={proposal.baseContent} target={proposal.proposedContent} baseLabel="the original" targetLabel="the proposal" />
    {outcome?.status === "done" && <p role="status">{outcome.rev === undefined ? "Done." : `Applied as revision ${outcome.rev}.`}</p>}
    {outcome && outcome.status !== "done" && <p role="alert">{RESULT[outcome.status]}</p>}
    {open && (canDecide ? <div className="space-y-2">
      {hasLocalDraft && <div role="status" className="space-y-2">
        <p>You have unsent edits. Accepting reloads the document, so wait until they are confirmed, or download them first.</p>
        <Button type="button" variant="outline" className="coarse:min-h-[44px]" onClick={onDownloadDraft}>Download unsent edits</Button>
      </div>}
      <div className="flex flex-wrap gap-2">
        <Button type="button" className="coarse:min-h-[44px]" disabled={busy || needsChoice} onClick={() => void decide("accept")}>{retry === "accept" ? "Try again (same request)" : "Accept proposal"}</Button>
        {confirmReject ? <>
          <Button type="button" variant="destructive" className="coarse:min-h-[44px]" disabled={busy} onClick={() => void decide("reject")}>{retry === "reject" ? "Try again (same request)" : "Confirm reject"}</Button>
          <Button type="button" variant="ghost" className="coarse:min-h-[44px]" disabled={busy} onClick={() => setConfirmReject(false)}>Keep reviewing</Button>
        </> : <Button type="button" variant="outline" className="coarse:min-h-[44px]" disabled={busy} onClick={() => setConfirmReject(true)}>Reject proposal</Button>}
      </div>
    </div> : <p className="text-sm">You can compare this proposal but not decide on it.</p>)}
  </section>
}
