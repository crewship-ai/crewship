"use client"
import { useEffect, useRef, useState } from 'react'
import { Button } from '@/components/ui/button'
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { useBoardRequests, BoardRequestError, type BoardAgentRequest, type BoardRequestDraft, type BoardRequestGeometry } from '@/hooks/use-board-requests'
import type { PageBoard } from '@/hooks/use-page-board'
import type { BoardElement } from '@/lib/boards/revision-adapter'
import { boardConversionSupported } from "@/lib/boards/diagram-frame"
import { convertBoardDiagram } from '@/lib/boards/diagram'
import { renderSelectionPreview } from '@/lib/boards/selection-preview'
import type { BinaryFileData } from '@excalidraw/excalidraw/types'
import { requestRunningLabel } from '@/lib/pages/request-execution'
import { RequestExecutionDetails } from './request-execution-details'
type FocusWorkArea = (target: BoardRequestGeometry, label: string) => void
export function BoardAgentWork({ workspaceId, slug, board, selectedIds, disabled, onDirtyChange, files = [], onFocusWorkArea }: { workspaceId: string; slug: string; board: PageBoard; selectedIds: string[]; disabled: boolean; onDirtyChange?: (dirty: boolean) => void; files?: BinaryFileData[]; onFocusWorkArea?: FocusWorkArea }) {
  const requests = useBoardRequests(workspaceId, slug)
  const [draft, setDraft] = useState<BoardRequestDraft | null>(null)
  const preview = useRef<AbortController | null>(null)
  const [preparingPreview, setPreparingPreview] = useState(false)
  const [previewError, setPreviewError] = useState<string | null>(null)
  const closeDraft = () => { preview.current?.abort(); setPreparingPreview(false); setDraft(null) }
  useEffect(() => () => preview.current?.abort(), [])
  useEffect(() => { preview.current?.abort(); setPreparingPreview(false); setDraft(null); setPreviewError(null) }, [workspaceId, slug])
  const uncertain = Boolean(requests.send.error && (!(requests.send.error instanceof BoardRequestError) || requests.send.error.status >= 500))
  const hasSavedSelection = board.records.some(item => !item.deleted && selectedIds.includes(item.element_id)) || board.placements.some(item => selectedIds.includes(`board-placement:${item.id}`))
  const dirty = Boolean(draft || requests.send.isPending || uncertain)
  useEffect(() => { onDirtyChange?.(dirty); return () => onDirtyChange?.(false) }, [dirty, onDirtyChange])
  const denied = requests.query.error instanceof BoardRequestError && [401,403,404].includes(requests.query.error.status)
  useEffect(() => { if (denied || requests.query.data?.can_dispatch === false) { preview.current?.abort(); setPreparingPreview(false); setDraft(null); setPreviewError(null) } }, [denied, requests.query.data?.can_dispatch])
  const open = (kind: 'diagram' | 'artifact', instruction = '', retryOf?: string) => {
    preview.current?.abort(); setPreviewError(null); setPreparingPreview(false)
    requests.send.reset()
    const elements = board.records.filter(item => !item.deleted && item.element && selectedIds.includes(item.element_id))
    const placements = board.placements.filter(item => selectedIds.includes(`board-placement:${item.id}`))
    const geometry = [...elements.map(item => item.element!), ...placements].filter(item => typeof item.x === 'number' && typeof item.y === 'number')
    const x = geometry.length ? Math.min(...geometry.map(item => Number(item.x))) : 80
    const y = geometry.length ? Math.min(...geometry.map(item => Number(item.y))) : 80
    const intent = crypto.randomUUID()
    setDraft({ ...(retryOf ? { retry_of_request_id: retryOf } : {}), intent_id: intent, kind, instruction, chat_id: '', base_seq: board.seq, selected_element_ids: elements.map(item => item.element_id), selected_placement_ids: placements.map(item => item.id), target: { x, y, width: 640, height: 420 } })
    if (kind === 'artifact') {
      const controller = new AbortController(); preview.current = controller; setPreparingPreview(true)
      void renderSelectionPreview(board, selectedIds, files, controller.signal).then(preview_png => {
        if (!controller.signal.aborted) setDraft(current => current?.intent_id === intent ? { ...current, preview_png } : current)
      }).catch(error => { if (!controller.signal.aborted) setPreviewError(error instanceof Error ? error.message : 'Preview unavailable.') })
        .finally(() => { if (!controller.signal.aborted) setPreparingPreview(false) })
    }
  }
  if (denied) return <p role="alert" className="p-4 text-sm">{requests.query.error?.message}</p>
  const data = requests.query.data
  return <section aria-label="Board agent work" className="space-y-3 border-t p-4">
    <div className="flex flex-wrap items-center justify-between gap-3"><div><h3 className="text-sm font-medium">Agent work</h3><p className="text-xs text-muted-foreground">Send an explicit request. Generated diagrams need your approval before insertion.</p></div>{data?.can_dispatch && <div className="flex flex-wrap gap-2"><Button variant="outline" size="sm" disabled={disabled} onClick={() => open('diagram')}>Generate diagram</Button><Button variant="outline" size="sm" disabled={disabled || !hasSavedSelection} onClick={() => open('artifact')}>Create artifact from selection</Button></div>}</div>
    {requests.query.error && <p role="alert" className="text-sm text-warn">Could not refresh agent work. {requests.query.error.message}</p>}
    {requests.cancel.error && <p role="alert" className="text-sm text-destructive">{requests.cancel.error.message}</p>}
    {data?.requests.map(item => <BoardRequestCard onFocusWorkArea={onFocusWorkArea} key={item.id} item={item} board={board} disabled={disabled} requests={requests} onNew={() => open(item.kind, item.instruction, item.id)} />)}
    {data && !data.requests.length && <p className="text-xs text-muted-foreground">No agent requests for this board.</p>}
    {draft && data?.can_dispatch && <Dialog open onOpenChange={value => { if (!value && !uncertain && !requests.send.isPending) closeDraft() }}><DialogContent><DialogHeader><DialogTitle>{draft.kind === 'diagram' ? 'Generate a diagram' : 'Create an artifact from selection'}</DialogTitle><DialogDescription>The selected private chat and its agent receive your instructions and the saved selection from board sequence {draft.base_seq}. Later edits are excluded. Sending authorizes this request only.</DialogDescription></DialogHeader>
      <p className="text-xs text-muted-foreground">{draft.selected_element_ids.length} drawing elements · {draft.selected_placement_ids.length} artifact pins. {draft.kind === 'artifact' ? 'The result is a private artifact. The server pins it only if your board access and original sequence still allow it.' : 'Only plain flowchart nodes and edges are supported. No HTML, links or styling.'}</p>
      {draft.kind === 'artifact' && <div className="space-y-2">
        {preparingPreview && <p role="status" className="text-xs">Preparing selection preview…</p>}
        {previewError && <p role="status" className="text-xs text-warn">{previewError} The saved scene can still be sent without an image.</p>}
        {draft.preview_png && <img src={`data:image/png;base64,${draft.preview_png}`} alt="Selected drawings; artifact pins are empty placeholders" className="max-h-40 w-full rounded border object-contain" />}
        <p className="text-xs text-muted-foreground">The preview is an illustration of selected drawings. Artifact pins are empty placeholders; their contents are supplied separately only when authorized.</p>
        {(preparingPreview || draft.preview_png) && <Button size="sm" variant="ghost" disabled={uncertain || requests.send.isPending} onClick={() => { preview.current?.abort(); setPreparingPreview(false); setDraft(current => current ? { ...current, preview_png: undefined } : null) }}>Continue without preview</Button>}
      </div>}
      {draft.retry_of_request_id && <p role="alert" className="text-sm text-warn">This is a new request using the current board and current selection. The previous runtime may still be running; sending can duplicate work. Review this fresh selection and instructions before sending.</p>}
      <label className="space-y-1 text-sm">Private chat<select aria-label="Private chat for board request" className="w-full rounded-md border bg-background p-2" value={draft.chat_id} disabled={uncertain || requests.send.isPending} onChange={event => setDraft({ ...draft, chat_id: event.target.value })}><option value="">Choose a private chat</option>{data.eligible_chats.map(chat => <option key={chat.chat_id} value={chat.chat_id}>{chat.title} · {chat.agent_name || chat.agent_slug || 'Agent'}</option>)}</select></label>
      {!data.eligible_chats.length && <p className="text-sm text-muted-foreground">No eligible private chats are available. Use a private chat you own with an available agent.</p>}
      <label className="space-y-1 text-sm">Instructions<textarea aria-label="Board request instructions" className="min-h-28 w-full rounded-md border bg-background p-2" value={draft.instruction} disabled={uncertain || requests.send.isPending} onChange={event => setDraft({ ...draft, instruction: event.target.value })} /></label>
      {requests.send.error && <p role="alert" className="text-sm text-destructive">{requests.send.error.message}{uncertain && ' Retry this unchanged submission to confirm its outcome.'}</p>}
      <DialogFooter><Button variant="ghost" disabled={uncertain || requests.send.isPending} onClick={closeDraft}>Cancel</Button><Button disabled={disabled || !draft.chat_id || !draft.instruction.trim() || requests.send.isPending || preparingPreview} onClick={async () => { try { await requests.send.mutateAsync(draft); closeDraft() } catch { /* Keep exact snapshot and instructions. */ } }}>{requests.send.isPending ? 'Sending…' : uncertain ? 'Retry submission' : 'Send request'}</Button></DialogFooter>
    </DialogContent></Dialog>}
  </section>
}
function BoardRequestCard({ item, board, disabled, requests, onNew, onFocusWorkArea }: { item: BoardAgentRequest; board: PageBoard; disabled: boolean; requests: ReturnType<typeof useBoardRequests>; onNew: () => void; onFocusWorkArea?: FocusWorkArea }) {
  const conversion = useRef<AbortController | null>(null)
  useEffect(() => () => conversion.current?.abort(), [])
  useEffect(() => { if (disabled || !item.can_insert) conversion.current?.abort() }, [disabled, item.can_insert])
  const [prepared, setPrepared] = useState<{ id: string; base_seq: number; source_hash: string; upserts: { element: BoardElement; expected_rev: 0 }[] } | null>(null)
  const [preparing, setPreparing] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [canReview, setCanReview] = useState(false)
  const insert = async () => {
    conversion.current?.abort(); const controller = new AbortController(); conversion.current = controller
    setPreparing(true); setError(null); setCanReview(false)
    try {
      let input = prepared
      if (!input) {
        const detail = await requests.detail(item.id)
        if (!detail.can_insert || detail.dialect !== 'flowchart-v1' || detail.converter_version !== '2.2.2' || !detail.source_hash) throw new Error('This diagram is no longer ready to insert. Refresh agent work.')
        const elements = await convertBoardDiagram(detail.mermaid_source, detail.target, detail.converter_url ?? requests.query.data?.converter_url, controller.signal)
        input = { id: item.id, base_seq: board.seq, source_hash: detail.source_hash, upserts: elements.map(element => ({ element, expected_rev: 0 })) }
        setPrepared(input)
      }
      if (controller.signal.aborted) return
      await requests.insert.mutateAsync(input)
      setPrepared(null)
    } catch (failure) { setError(failure instanceof Error ? failure.message : 'Could not insert diagram.'); setCanReview(failure instanceof BoardRequestError && failure.status === 409) }
    finally { setPreparing(false) }
  }
  return <article className="space-y-2 rounded-lg border p-3"><div className="flex flex-wrap items-center justify-between gap-2"><p className="text-sm font-medium">{item.kind === 'diagram' ? 'Diagram' : 'Artifact'} · {item.agent_name || 'Agent'}</p><span className="text-xs text-muted-foreground">{item.status === 'running' ? requestRunningLabel(item) : item.status.replaceAll('_',' ')}</span></div><p className="whitespace-pre-wrap text-sm">{item.instruction}</p><p className="text-xs text-muted-foreground">Saved selection from sequence {item.base_seq}</p>
    <RequestExecutionDetails request={item} />
    {item.can_insert && !boardConversionSupported(requests.query.data?.converter_url) && <p className="text-xs text-muted-foreground">Isolated insertion is unavailable. A separate runtime site and supported Chromium browser are required.</p>}
    {item.error && <p className="text-sm text-destructive">{item.error}</p>}
    {item.result && <a className="text-sm text-primary underline" href={`/pages/${encodeURIComponent(item.result.page_slug)}?revision=${item.result.revision}`} target="_blank" rel="noopener noreferrer">{item.result.title} · Revision {item.result.revision}</a>}
    {item.pin_status === 'pinned' && <p className="text-xs text-muted-foreground">Artifact pinned to the board.</p>}
    {item.pin_status === 'created_not_pinned' && <p className="text-xs text-muted-foreground">Artifact created without a pin. {item.pin_reason}</p>}
    {item.result_seq != null && <p className="text-xs text-muted-foreground">Inserted at board sequence {item.result_seq}</p>}
    {error && <p role="alert" className="text-sm text-destructive">{error}</p>}
    <div className="flex flex-wrap gap-2">{onFocusWorkArea && item.target && [item.target.x, item.target.y, item.target.width, item.target.height].every(value => Number.isFinite(value) && Math.abs(value) <= 1000000) && <Button variant="outline" size="sm" className="coarse:h-12" onClick={() => onFocusWorkArea(item.target, `${item.kind === "diagram" ? "Diagram" : "Artifact"} request · ${item.status.replaceAll("_", " ")} · saved sequence ${item.base_seq}`)}>Show work area</Button>}{item.can_insert && <Button variant="outline" size="sm" disabled={disabled || preparing || requests.insert.isPending || !boardConversionSupported(requests.query.data?.converter_url)} onClick={() => void insert()}>{preparing ? 'Preparing…' : prepared ? 'Retry insertion' : 'Insert diagram'}</Button>}{item.can_cancel && <Button variant="ghost" size="sm" disabled={requests.cancel.isPending} onClick={() => requests.cancel.mutate(item.id)}>Cancel request</Button>}{['failed','cancelled'].includes(item.status) && requests.query.data?.can_dispatch && <Button variant="ghost" size="sm" disabled={disabled} onClick={onNew}>New request</Button>}{prepared && canReview && <Button variant="ghost" size="sm" onClick={() => { setPrepared(null); requests.insert.reset(); setError(null) }}>Review current board before insertion</Button>}</div>
  </article>
}
