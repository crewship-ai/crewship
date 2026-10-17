"use client"
import { useState } from 'react'
import { Button } from '@/components/ui/button'
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { MAX_BOARD_IMAGE_INPUT } from '@/lib/boards/files'
import type { BoardImageIntent, BoardImageReady } from '@/hooks/use-board-image-upload'

export function BoardImageUploadDialog({ baseSeq, pending, error, onClose, onUpload }: { baseSeq: number; pending: boolean; error: string | null; onClose: () => void; onUpload: (intent: BoardImageIntent) => Promise<BoardImageReady | null> }) {
  const [intent, setIntent] = useState<BoardImageIntent | null>(null)
  const [validation, setValidation] = useState<string | null>(null)
  return <Dialog open onOpenChange={open => { if (!open && !pending) onClose() }}><DialogContent><DialogHeader><DialogTitle>Add image</DialogTitle><DialogDescription>Choose a PNG or JPEG up to 5 MiB and 2048 × 2048 pixels. Images stay private to this board. Save the board after placing the image; unused uploads remain within the board storage limits.</DialogDescription></DialogHeader>
    <label htmlFor="board-image-file" className="text-sm font-medium">Image file</label><input id="board-image-file" type="file" accept="image/png,image/jpeg,.png,.jpg,.jpeg" disabled={pending} onChange={event => {
      const file = event.target.files?.[0]
      if (!file) { setIntent(null); return }
      if (!file.size || file.size > MAX_BOARD_IMAGE_INPUT) { setValidation('Choose an image up to 5 MiB.'); setIntent(null); return }
      setValidation(null); setIntent({ file, baseSeq, mutationId: crypto.randomUUID() })
    }} />
    {(validation || error) && <p role="alert" className="text-sm text-destructive">{validation || error}</p>}
    <DialogFooter><Button variant="outline" disabled={pending} onClick={onClose}>Cancel</Button><Button disabled={pending || !intent} onClick={() => { if (intent) void onUpload(intent) }}>{pending ? 'Uploading…' : 'Upload and place'}</Button></DialogFooter>
  </DialogContent></Dialog>
}
