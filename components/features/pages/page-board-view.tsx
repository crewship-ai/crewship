"use client"
import dynamic from "next/dynamic"
import { useCallback, useEffect, useRef, useState } from "react"
import type { ExcalidrawElement } from "@excalidraw/excalidraw/element/types"
import { usePageBoard, PageBoardError, type PageBoard } from "@/hooks/use-page-board"
import { manualBoardBatch } from "@/lib/boards/manual-batch"
import { Button } from "@/components/ui/button"
import { Dialog, DialogContent, DialogHeader, DialogTitle, DialogDescription, DialogFooter } from "@/components/ui/dialog"
import { BoardToolbar } from "./board-toolbar"
import { BoardPlacementLink } from "./board-artifact-placement"
import { BoardPinDialog } from "./board-pin-dialog"
const Editor = dynamic(async () => {
  window.EXCALIDRAW_ASSET_PATH = "/excalidraw-assets/"
  return import("./board-editor")
}, { ssr: false, loading: () => <p role="status" className="p-6 text-sm">Loading drawing tools…</p> })
const placementId = (id: string) => `board-placement:${id}`
const allowed = new Set(["rectangle", "ellipse", "diamond", "line", "arrow", "freedraw", "text", "frame"])
export function PageBoardView({ workspaceId, slug, onDirtyChange }: { workspaceId: string; slug: string; onDirtyChange?: (dirty: boolean) => void }) {
  const { query, write } = usePageBoard(workspaceId, slug)
  const [base, setBase] = useState<PageBoard | null>(null)
  const [draft, setDraft] = useState<readonly ExcalidrawElement[] | null>(null)
  const [generation, setGeneration] = useState(0)
  const epoch = useRef(0)
  const observed = useRef<string | null>(null)
  const [confirmReload, setConfirmReload] = useState(false)
  const [pinning, setPinning] = useState(false)
  const drawing = draft?.filter(element => !base?.placements.some(item => placementId(item.id) === element.id)) ?? []
  const batch = base && draft ? manualBoardBatch(base, drawing) : null
  const placement_updates = base && draft ? base.placements.flatMap(item => {
    const element = draft.find(element => element.id === placementId(item.id))
    return element && !element.isDeleted && ["x", "y", "width", "height"].some(field => element[field as "x"] !== item[field as "x"]) ? [{ id: item.id, x: element.x, y: element.y, width: element.width, height: element.height }] : []
  }) : []
  const placement_deletes = base && draft ? base.placements.filter(item => !draft.some(element => element.id === placementId(item.id) && !element.isDeleted)).map(item => ({ id: item.id })) : []
  const dirty = Boolean(batch && (batch.upserts.length || batch.deletes.length || placement_updates.length || placement_deletes.length))
  const invalidPlacement = Boolean(base && draft?.some(element => base.placements.some(item => placementId(item.id) === element.id) && !element.isDeleted && (element.angle !== 0 || element.groupIds.length > 0 || element.frameId != null)))
  const invalid = invalidPlacement || drawing.some(element => !element.isDeleted && (!allowed.has(element.type) || element.link != null || element.customData != null))
  const replace = useCallback((canonical: PageBoard) => { epoch.current++; observed.current = null; setGeneration(epoch.current); setBase(canonical); setDraft(null) }, [])
  const onChange = useCallback((elements: readonly ExcalidrawElement[]) => {
    if (epoch.current !== generation) return
    // Excalidraw also emits onChange for host/app-state renders. Clone only a
    // changed scene; unconditional cloning creates a host/editor render loop.
    const signature = JSON.stringify(elements)
    if (signature === observed.current) return
    observed.current = signature
    setDraft(structuredClone(elements))
  }, [generation])
  useEffect(() => { onDirtyChange?.(dirty); return () => onDirtyChange?.(false) }, [dirty, onDirtyChange])
  const denied = query.error instanceof PageBoardError && [401, 403, 404].includes(query.error.status)
  useEffect(() => { if (denied) { epoch.current++; setBase(null); setDraft(null); setPinning(false) } }, [denied])
  useEffect(() => { if (query.data && !dirty && !write.isPending && query.data !== base) replace(query.data) }, [query.data, dirty, write.isPending, base, replace])
  const reload = async () => {
    const response = await query.refetch()
    if (response.data && !response.error) { replace(response.data); write.reset(); setConfirmReload(false) }
  }
  if (!base || denied) return <div className="space-y-3 rounded-xl border p-5 text-sm">{query.error ? <><p role="alert">{query.error.message}</p><Button variant="outline" onClick={() => void reload()}>Retry board</Button></> : <p role="status">Loading board…</p>}</div>
  const authorization = query.data ?? base
  return <section className="overflow-hidden rounded-xl border bg-card" aria-label="Board">
    <BoardToolbar seq={base.seq} dirty={dirty} writable={authorization.can_write} pending={write.isPending} invalid={invalid} onSave={async () => {
      if (!batch || !dirty) return
      try { replace(await write.mutateAsync({ base_seq: base.seq, operation: { kind: "batch", batch: { ...batch, placement_updates, placement_deletes } } })) } catch { /* Preserve the original draft and CAS base. */ }
    }} onReload={() => { if (dirty) setConfirmReload(true); else void reload() }} onPin={() => { write.reset(); setPinning(true) }} />
    {query.error && <p role="alert" className="px-4 pb-3 text-sm text-warn">Could not refresh board access or changes. Showing the last authorized snapshot. {query.error.message}</p>}
    {query.data && query.data.seq !== base.seq && dirty && <p role="status" className="px-4 pb-3 text-sm text-warn">New changes are available. Your drawing stays on sequence {base.seq}; saving will check for conflicts.</p>}
    {write.error && !pinning && <p role="alert" className="px-4 pb-3 text-sm text-destructive">{write.error.message} Your unsaved drawing is retained. Reload latest deliberately to discard it.</p>}
    {invalid && <p role="alert" className="px-4 pb-3 text-sm text-destructive">Only local drawing shapes, text and approved artifact placements can be saved. Remove unsupported links or embeds. Artifact pins support moving and resizing; remove rotation, groups or frame membership before saving.</p>}
    {!authorization.can_write && <p className="px-4 pb-3 text-sm text-muted-foreground">Read-only board. Your access does not allow saving changes.</p>}
    <Editor key={generation} base={base} authorization={authorization} workspaceId={workspaceId} disabled={!authorization.can_write || write.isPending} onChange={onChange} />
    <div className="space-y-2 border-t p-4"><p className="text-xs text-muted-foreground">Undo applies to the current unsaved drawing. Saving or reloading resets drawing history. Images and live collaboration are not available yet.</p>{authorization.placements.map(item => <BoardPlacementLink key={item.id} workspaceId={workspaceId} placement={item} />)}</div>
    {pinning && <BoardPinDialog workspaceId={workspaceId} pending={write.isPending} error={write.error?.message ?? null} onClose={() => { setPinning(false); write.reset() }} onPin={async (source_page_slug, source_revision) => {
      try { replace(await write.mutateAsync({ base_seq: base.seq, operation: { kind: "pin", source_page_slug, source_revision, x: 80 + base.placements.length * 40, y: 100 + base.placements.length * 40, width: 420, height: 300 } })); setPinning(false) } catch { /* The selected source stays available for explicit retry. */ }
    }} />}
    <Dialog open={confirmReload} onOpenChange={setConfirmReload}><DialogContent><DialogHeader><DialogTitle>Discard unsaved drawing?</DialogTitle><DialogDescription>Reloading replaces your local drawing with the latest saved board and clears undo history. Your pending changes will be lost.</DialogDescription></DialogHeader><DialogFooter><Button variant="ghost" onClick={() => setConfirmReload(false)}>Keep drawing</Button><Button variant="destructive" onClick={() => void reload()}>Discard and reload</Button></DialogFooter></DialogContent></Dialog>
  </section>
}
declare global { interface Window { EXCALIDRAW_ASSET_PATH: string } }
