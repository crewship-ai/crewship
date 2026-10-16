"use client"
import { Button } from '@/components/ui/button'
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import type { BoardPlacement } from '@/hooks/use-page-board'
import type { CollaborativeBatch } from '@/lib/boards/collaboration-types'
export interface BoardPinModeIntent { base_seq: number; placement: BoardPlacement; batch: CollaborativeBatch }
export function pinModeIntent(baseSeq: number, placement: BoardPlacement): BoardPinModeIntent {
  if (!placement.source || !Number.isSafeInteger(placement.placement_rev) || placement.placement_rev! < 1) throw new Error('Reload the pin to confirm its current version.')
  const mode = placement.mode === 'follow_latest' ? 'fixed' : 'follow_latest'
  const revision = mode === 'fixed' ? placement.source.revision : placement.source.pinned_revision ?? placement.source.revision
  return { base_seq: baseSeq, placement: structuredClone(placement), batch: { base_seq: baseSeq, upserts: [], deletes: [], placement_modes: [{ id: placement.id, expected_rev: placement.placement_rev!, mode, source_revision: revision }] } }
}
export function BoardPinModeDialog({ intent, pending, uncertain, error, onClose, onConfirm }: { intent: BoardPinModeIntent; pending: boolean; uncertain: boolean; error: string | null; onClose: () => void; onConfirm: () => void }) {
  const change = intent.batch.placement_modes![0]
  const follows = change.mode === 'follow_latest'
  return <Dialog open onOpenChange={open => { if (!open && !pending && !uncertain) onClose() }}><DialogContent><DialogHeader><DialogTitle>{follows ? 'Follow latest artifact revision?' : 'Keep this artifact revision fixed?'}</DialogTitle><DialogDescription>{follows ? 'This pin will display new saved revisions automatically when you are not editing the board. This does not enable scripts or grant access to the source.' : `This pin will stay on revision ${change.source_revision}. Future source revisions will not change its preview.`}</DialogDescription></DialogHeader>
    <p className="text-sm">{intent.placement.source?.title ?? "Artifact unavailable"} · Board sequence {intent.base_seq}</p>
    {error && <p role="alert" className="text-sm text-destructive">{error}{uncertain ? ' Retry this unchanged mode change to confirm its outcome.' : ' The original choice is retained. Close and review the current board before choosing again.'}</p>}
    <DialogFooter><Button variant="ghost" className="coarse:h-12" disabled={pending || uncertain} onClick={onClose}>Cancel</Button><Button className="coarse:h-12" disabled={pending} onClick={onConfirm}>{pending ? 'Saving…' : uncertain ? 'Retry mode change' : follows ? 'Follow latest' : `Keep revision ${change.source_revision}`}</Button></DialogFooter>
  </DialogContent></Dialog>
}
