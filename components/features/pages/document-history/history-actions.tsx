"use client"

import { useRef, useState } from "react"
import { Button } from "@/components/ui/button"
import type { DocumentHistoryActions, HistoryActionResult } from "@/lib/documents/history-actions"

const FAILURE: Record<Exclude<HistoryActionResult["status"], "done">, string> = {
  conflict: "Nothing was changed. Later edits touch the same blocks, so this change cannot be reverted automatically.",
  stale: "Nothing was changed. The document moved on while you were deciding; reopen history and try again.",
  retention: "This revision is no longer retained, so it cannot be used.",
  denied: "You no longer have access to change this document.",
  unavailable: "The service is unavailable. Nothing was changed; try again later.",
  unknown: "The result is unknown: the request may not have completed. Check the document history before trying again. Trying again sends the same request, so it cannot apply twice.",
  error: "The request was rejected. Nothing was changed.",
}

interface Props {
  checkpointId: string
  revision: number
  /** Current confirmed head; compared with the revision the user reviewed. */
  headRev: number
  /** Unsent local edits exist. Reverting reloads the editor, so it waits until they are confirmed or downloaded. */
  hasLocalDraft: boolean
  actions: DocumentHistoryActions
  onDownloadDraft: () => void
  onDone: (rev: number) => void
}

/** Revert one change. Restore is a different action (a new revision from a retained one) with its own flow. Callers render this only for people who can write. */
export function HistoryActions({ checkpointId, revision, headRev, hasLocalDraft, actions, onDownloadDraft, onDone }: Props) {
  const [open, setOpen] = useState(false)
  const [reviewedRev, setReviewedRev] = useState(headRev)
  const [busy, setBusy] = useState(false)
  const [result, setResult] = useState<HistoryActionResult>()
  /** One request ID per decision, bound to the revision it names: a retry repeats it, a new expected revision never reuses it. */
  const request = useRef<{ id: string; rev: number }>(undefined)
  const moved = open && headRev !== reviewedRev
  const close = () => { if (!busy) { setOpen(false); request.current = undefined } }
  async function confirm() {
    if (busy || hasLocalDraft) return
    setBusy(true); setResult(undefined)
    if (request.current?.rev !== reviewedRev) request.current = { id: crypto.randomUUID(), rev: reviewedRev }
    const outcome = await actions.revertChange({ checkpointId, expectedRev: reviewedRev, requestId: request.current.id })
    setBusy(false); setResult(outcome)
    if (outcome.status === "unknown") return // keep the same request ID for a retry
    request.current = undefined
    if (outcome.status === "done") { setOpen(false); onDone(outcome.rev) }
  }
  const retrying = result?.status === "unknown"
  return <div className="space-y-3" aria-label={`Actions for revision ${revision}`} role="group">
    <Button type="button" variant="outline" className="coarse:min-h-[44px]" disabled={busy} aria-expanded={open} onClick={() => { setResult(undefined); setReviewedRev(headRev); setOpen(true) }}>Revert this change</Button>
    {open && <div role="group" aria-label="Confirm revert" className="space-y-2 rounded-md border p-3" onKeyDown={event => { if (event.key === "Escape") close() }}>
      <p>Undo only the change recorded at this checkpoint (revision {revision}). Work saved after it is kept. If later work touches the same blocks, nothing changes and the conflict is shown.</p>
      {hasLocalDraft && <div role="status" className="space-y-2">
        <p>You have unsent edits. Reverting reloads the document, so wait until they are confirmed, or download them first.</p>
        <Button type="button" variant="outline" className="coarse:min-h-[44px]" onClick={onDownloadDraft}>Download unsent edits</Button>
      </div>}
      {moved && <p role="status">The document changed after you opened this confirmation (revision {reviewedRev} to {headRev}). The request names revision {reviewedRev}, so the server refuses it if it no longer matches.</p>}
      <div className="flex flex-wrap gap-2">
        <Button type="button" className="coarse:min-h-[44px]" disabled={busy || hasLocalDraft} onClick={() => void confirm()}>{busy ? "Working…" : retrying ? "Try again (same request)" : "Revert this change"}</Button>
        <Button type="button" variant="ghost" className="coarse:min-h-[44px]" disabled={busy} onClick={close}>Cancel</Button>
      </div>
    </div>}
    {result && result.status !== "done" && <p role="alert">{FAILURE[result.status]}{result.status === "conflict" && result.blocks.length > 0 ? ` (${result.blocks.length} block${result.blocks.length === 1 ? "" : "s"})` : ""}</p>}
  </div>
}
