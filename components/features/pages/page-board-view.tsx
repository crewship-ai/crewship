"use client"
import dynamic from "next/dynamic"
import type { BoardEditorViewState } from "./board-editor"
import { useCallback, useEffect, useMemo, useRef, useState } from "react"
import type { Collaborator, SocketId } from "@excalidraw/excalidraw/types"
import { useBoardImages } from "@/hooks/use-board-images"
import { useBoardImageUpload } from "@/hooks/use-board-image-upload"
import type { BoardImageUpload } from "@/lib/boards/files"
import { BoardElementPermissions } from "./board-element-permissions"
import { BoardComments } from "./board-comments"
import { BoardImageUploadDialog } from "./board-image-upload-dialog"
import { useBoardFollow } from "@/hooks/use-board-follow"
import { BoardFollowControls } from "./board-follow-controls"
import { useIssuePeople } from "@/hooks/use-issue-people"
import { useContentPresence } from "@/hooks/use-content-presence"
import { ContentPresenceStatus } from "./content-presence"
import type { ExcalidrawElement } from "@excalidraw/excalidraw/element/types"
import { usePageBoard, PageBoardError, type PageBoard } from "@/hooks/use-page-board"
import { planBoardMerge } from "@/lib/boards/collaboration-plan"
import { planSavedUndo, savedBoardCommand, type SavedBoardCommand } from "@/lib/boards/command-history"
import type { BoardDraft, CollaborativeBatch } from "@/lib/boards/collaboration-types"
import { manualBoardBatch } from "@/lib/boards/manual-batch"
import { Button } from "@/components/ui/button"
import { Dialog, DialogContent, DialogHeader, DialogTitle, DialogDescription, DialogFooter } from "@/components/ui/dialog"
import { BoardAgentWork } from "./board-agent-work"
import { BoardToolbar } from "./board-toolbar"
import { BoardPlacementLink } from "./board-artifact-placement"
import { previewPlacements } from "@/lib/boards/preview-placements"
import { BoardPinModeDialog, pinModeIntent, type BoardPinModeIntent } from "./board-pin-mode-dialog"
import { BoardPinDialog } from "./board-pin-dialog"
const Editor = dynamic(async () => {
  window.EXCALIDRAW_ASSET_PATH = "/excalidraw-assets/"
  return import("./board-editor")
}, { ssr: false, loading: () => <p role="status" className="p-6 text-sm">Loading drawing tools…</p> })
const placementId = (id: string) => `board-placement:${id}`
const allowed = new Set(["rectangle", "ellipse", "diamond", "line", "arrow", "freedraw", "text", "frame", "image"])
export function PageBoardView({ workspaceId, slug, pageId, onDirtyChange }: { workspaceId: string; slug: string; pageId?: string; onDirtyChange?: (dirty: boolean) => void }) {
  const { query, write, receipt, uncertain } = usePageBoard(workspaceId, slug)
  const [base, setBase] = useState<PageBoard | null>(null)
  const [draft, setDraft] = useState<readonly ExcalidrawElement[] | null>(null)
  const [generation, setGeneration] = useState(0)
  const viewState = useRef<BoardEditorViewState | undefined>(undefined)
  const rememberView = useCallback((state: BoardEditorViewState) => { viewState.current = state }, [])
  const epoch = useRef(0)
  const observed = useRef<string | null>(null)
  const [confirmReload, setConfirmReload] = useState(false)
  const [pinning, setPinning] = useState(false)
  const [modeIntent, setModeIntent] = useState<BoardPinModeIntent | null>(null)
  const previewSnapshot = useRef<import("@/hooks/use-page-board").BoardPlacement[]>([])
  const [imageDialog, setImageDialog] = useState(false)
  const [insertImage, setInsertImage] = useState<BoardImageUpload | null>(null)
  const [imageAccessLost, setImageAccessLost] = useState(false)
  const [focusLabel, setFocusLabel] = useState<string>()
  const [focusPoint, setFocusPoint] = useState<{ x: number; y: number; requestId: string }>()
  const [permissionDirty, setPermissionDirty] = useState(false)
  const [commentDirty, setCommentDirty] = useState(false)
  const [requestDirty, setRequestDirty] = useState(false)
  const [busy, setBusy] = useState(false)
  const [selectedIds, setSelectedIds] = useState<string[]>([])
  const onSelection = useCallback((ids: string[]) => { const sorted = [...ids].sort(); setSelectedIds(previous => JSON.stringify(previous) === JSON.stringify(sorted) ? previous : sorted) }, [])
  const [editorScene, setEditorScene] = useState<PageBoard | undefined>()
  const [commands, setCommands] = useState<SavedBoardCommand[]>([])
  const [undoNow, setUndoNow] = useState(Date.now)
  const [undoNotice, setUndoNotice] = useState<string | null>(null)
  useEffect(() => { setUndoNow(Date.now()); if (!commands.length) return; const timer = setInterval(() => setUndoNow(Date.now()), 30000); return () => clearInterval(timer) }, [commands])
  const undoAttempt = useRef<CollaborativeBatch | null>(null)
  const saveAttempt = useRef<{ base: PageBoard; forward: CollaborativeBatch } | null>(null)
  const [collaborationError, setCollaborationError] = useState<string | null>(null)
  const drawing = useMemo(() => draft?.filter(element => !base?.placements.some(item => placementId(item.id) === element.id)) ?? [], [draft, base])
  const batch = base && draft ? manualBoardBatch(base, drawing) : null
  const placement_updates = base && draft ? base.placements.flatMap(item => {
    const element = draft.find(element => element.id === placementId(item.id))
    return element && !element.isDeleted && ["x", "y", "width", "height"].some(field => element[field as "x"] !== item[field as "x"]) ? [{ id: item.id, expected_rev: item.placement_rev, x: element.x, y: element.y, width: element.width, height: element.height }] : []
  }) : []
  const placement_deletes = base && draft ? base.placements.filter(item => !draft.some(element => element.id === placementId(item.id) && !element.isDeleted)).map(item => ({ id: item.id, expected_rev: item.placement_rev })) : []
  const dirty = Boolean(batch && (batch.upserts.length || batch.deletes.length || placement_updates.length || placement_deletes.length))
  const imageDenied = useCallback(() => { setImageAccessLost(true); void query.refetch().then(result => { if (result.data && !result.error) setImageAccessLost(false) }) }, [query])
  const imageIds = useMemo(() => [...new Set(drawing.flatMap(element => element.type === "image" && !element.isDeleted && element.fileId ? [element.fileId] : []))], [drawing])
  const images = useBoardImages(workspaceId, slug, pageId ?? slug, imageIds, imageDenied)
  const imageUpload = useBoardImageUpload(workspaceId, slug, pageId ?? slug, imageDenied)
  const imageInserted = useCallback(() => setInsertImage(null), [])
  const invalidPlacement = Boolean(base && draft?.some(element => base.placements.some(item => placementId(item.id) === element.id) && !element.isDeleted && (element.angle !== 0 || element.groupIds.length > 0 || element.frameId != null)))
  const invalid = invalidPlacement || drawing.some(element => !element.isDeleted && (!allowed.has(element.type) || element.link != null || element.customData != null))
  const replace = useCallback((canonical: PageBoard) => { epoch.current++; observed.current = null; setGeneration(epoch.current); setBase(canonical); setDraft(null); setEditorScene(undefined); setCollaborationError(null) }, [])
  const onChange = useCallback((elements: readonly ExcalidrawElement[]) => {
    if (epoch.current !== generation) return
    // Excalidraw also emits onChange for host/app-state renders. Clone only a
    // changed scene; unconditional cloning creates a host/editor render loop.
    const signature = JSON.stringify(elements)
    if (signature === observed.current) return
    observed.current = signature
    setDraft(structuredClone(elements))
  }, [generation])
  useEffect(() => { onDirtyChange?.(dirty || uncertain || imageUpload.pending || Boolean(modeIntent) || commentDirty || permissionDirty || requestDirty); return () => onDirtyChange?.(false) }, [dirty, uncertain, imageUpload.pending, modeIntent, commentDirty, permissionDirty, requestDirty, onDirtyChange])
  const presenceContext = { kind: "board" as const, pageId: pageId ?? "", boardSeq: base?.seq ?? 0 }
  const presence = useContentPresence(presenceContext, Boolean(pageId && query.data))
  const { people } = useIssuePeople(workspaceId, presence.available)
  const collaborators = useMemo(() => {
    const result = new Map<SocketId, Collaborator>()
    for (const member of presence.members) {
      if (member.user_id === presence.currentUserId || member.cursor?.coordinate_space !== "board") continue
      result.set(member.connection_id as SocketId, { id: member.user_id, username: people.find(person => person.id === member.user_id)?.name ?? "Viewer", pointer: { x: member.cursor.x, y: member.cursor.y, tool: "pointer" } })
    }
    return result
  }, [presence.members, presence.currentUserId, people])
  const follow = useBoardFollow(presence.members, presence.currentUserId, presence.available, base?.seq ?? 0, Boolean(query.data) && !imageAccessLost)
  const stopFollow = follow.stop
  const selectFollow = follow.select
  const stopFollowing = useCallback(() => { stopFollow(); setFocusPoint(undefined); setFocusLabel(undefined) }, [stopFollow])
  const selectFollowing = useCallback((connection: string) => { setFocusPoint(undefined); setFocusLabel(undefined); selectFollow(connection) }, [selectFollow])
  const viewerNames = useMemo(() => Object.fromEntries(people.map(person => [person.id, person.name])), [people])
  const sendCursor = presence.setCursor
  const onPointer = useCallback((x: number, y: number) => sendCursor({ x, y, coordinate_space: "board" }), [sendCursor])
  const denied = query.error instanceof PageBoardError && [401, 403, 404].includes(query.error.status)
  useEffect(() => { if (denied) { viewState.current = undefined; epoch.current++; setBase(null); setDraft(null); setPinning(false); setModeIntent(null); previewSnapshot.current = []; setImageDialog(false); setInsertImage(null); setCommands([]); undoAttempt.current = null; saveAttempt.current = null; setEditorScene(undefined) } }, [denied])
  const localDraft = useMemo<BoardDraft | null>(() => base && draft ? { elements: drawing, placements: base.placements.flatMap(item => {
    const projection = draft.find(element => element.id === placementId(item.id) && !element.isDeleted)
    const { source: _source, ...geometry } = item
    return projection ? [{ ...geometry, x: projection.x, y: projection.y, width: projection.width, height: projection.height }] : []
  }) } : null, [base, draft, drawing])
  useEffect(() => {
    const latest = query.data
    if (!latest || latest === base || write.isPending || imageUpload.pending || modeIntent || uncertain || busy || (base && latest.seq < base.seq)) return
    if (!base || !dirty || !localDraft) { replace(latest); return }
    if (latest.seq <= base.seq) return
    const plan = planBoardMerge(base, localDraft, latest)
    if (plan.kind === "conflict") { setCollaborationError("Remote changes overlap your drawing or connected shapes. Your draft is retained; export it or reload latest deliberately."); return }
    if (plan.kind !== "merged") return
    const display: PageBoard = { ...plan.canonical, placements: [...plan.draft.placements], records: plan.draft.elements.map(element => ({ element_id: element.id, element_rev: 0, element, deleted: false })) }
    epoch.current++; observed.current = null; setGeneration(epoch.current); setBase(plan.canonical); setEditorScene(display); setDraft([
      ...plan.draft.elements as ExcalidrawElement[],
      ...plan.draft.placements.map(item => ({ ...(draft?.find(element => element.id === placementId(item.id)) ?? { id: placementId(item.id), type: "embeddable", groupIds: [], angle: 0, frameId: null }), x: item.x, y: item.y, width: item.width, height: item.height }) as ExcalidrawElement),
    ]); setCollaborationError(null)
  }, [query.data, base, dirty, localDraft, draft, write.isPending, imageUpload.pending, modeIntent, uncertain, busy, replace])
  const previewFrozen = busy || dirty || uncertain || write.isPending || imageUpload.pending || Boolean(modeIntent)
  const authorizedSnapshot = query.data ?? base
  const modeSourceAllowed = Boolean(modeIntent && authorizedSnapshot?.can_write && authorizedSnapshot.placements.some(item => item.id === modeIntent.placement.id && item.source && item.source.page_slug === modeIntent.placement.source?.page_slug))
  useEffect(() => { if (modeIntent && authorizedSnapshot && !modeSourceAllowed && !write.isPending && !uncertain) { setModeIntent(null); write.reset() } }, [modeIntent, authorizedSnapshot, modeSourceAllowed, write, uncertain])
  const shownPlacements = useMemo(() => previewPlacements(previewSnapshot.current, authorizedSnapshot?.placements ?? [], previewFrozen), [authorizedSnapshot?.placements, previewFrozen])
  useEffect(() => { if (!previewFrozen && authorizedSnapshot) previewSnapshot.current = authorizedSnapshot.placements }, [previewFrozen, authorizedSnapshot])
  const reload = async () => {
    const response = await query.refetch()
    if (response.data && !response.error) { replace(response.data); write.reset(); setConfirmReload(false) }
  }
  if (!base || denied) return <div className="space-y-3 rounded-xl border p-5 text-sm">{query.error ? <><p role="alert">{query.error.message}</p><Button variant="outline" onClick={() => void reload()}>Retry board</Button></> : <p role="status">Loading board…</p>}</div>
  const authorization = query.data ?? base
  const selectedSavedIds = selectedIds.filter(id => base.records.some(record => record.element_id === id && !record.deleted))
  // Stable selection is shared by explicit comments and agent actions.
  return <section onKeyDownCapture={event => { if (event.key === "Escape") stopFollowing() }} className="overflow-hidden rounded-xl border bg-card" aria-label="Board">
    <BoardToolbar seq={base.seq} dirty={dirty} writable={authorization.can_write} pending={write.isPending} invalid={invalid} onSave={async () => {
      if (!batch || !dirty) return
      try {
        if (!saveAttempt.current) saveAttempt.current = { base: structuredClone(base), forward: structuredClone({ ...batch, placement_updates, placement_deletes }) }
        const attempt = saveAttempt.current
        const forward = attempt.forward
        const canonical = await write.mutateAsync({ base_seq: attempt.base.seq, operation: { kind: "batch", batch: forward } })
        { try { if (!receipt.current) throw new Error("Missing save receipt"); const command = savedBoardCommand(attempt.base, forward, receipt.current, canonical); setUndoNotice(null); setCommands(previous => { const next = [...previous, command].slice(-16); while (next.length && JSON.stringify(next).length > 4 * 1024 * 1024) next.shift(); return next }) } catch { setCommands([]); setUndoNotice("Board saved. Saved undo is unavailable because the server did not provide complete revision or expiration proof.") } }
        saveAttempt.current = null; replace(canonical)
      } catch (error) { if (error instanceof PageBoardError && error.status < 500) saveAttempt.current = null }
    }} onReload={() => { if (uncertain) { setCollaborationError("Retry the pending operation to confirm its outcome before reloading or editing."); return }; if (dirty) setConfirmReload(true); else void reload() }} onPin={() => { if (uncertain) return; write.reset(); setPinning(true) }} />
    {pageId && <div className="px-4 pb-3"><ContentPresenceStatus presence={presence} context={presenceContext} /></div>}
    {pageId && <BoardFollowControls follow={follow} names={viewerNames} onSelect={selectFollowing} />}
    {focusLabel && !follow.active && <p role="status" className="px-4 pb-3 text-xs text-muted-foreground">{focusLabel}</p>}
    {authorization.can_write && <div className="px-4 pb-3"><Button variant="outline" size="sm" disabled={dirty || write.isPending || uncertain || imageUpload.pending || busy} onClick={() => setImageDialog(true)}>Add image</Button></div>}
    {images.error && <p role="alert" className="px-4 pb-3 text-sm text-warn">{images.error}</p>}
    {collaborationError && <p role="alert" className="px-4 pb-3 text-sm text-warn">{collaborationError}</p>}
    <div className="flex flex-wrap gap-2 px-4 pb-3">
      {selectedSavedIds.length > 0 && <p className="w-full text-xs text-muted-foreground">{selectedSavedIds.length} saved drawing {selectedSavedIds.length === 1 ? "element" : "elements"} selected</p>}
      <Button size="sm" variant="outline" disabled={!authorization.can_write || dirty || busy || write.isPending || (uncertain && !undoAttempt.current) || (commands.length === 0 || (commands.at(-1)!.expiresAt <= undoNow && !undoAttempt.current))} onClick={async () => {
        const command = commands.at(-1)
        if (!command) return
        if (!undoAttempt.current) {
          const latest = await query.refetch()
          if (!latest.data || latest.error) return
          const undo = planSavedUndo(command, latest.data)
          if (undo.kind === "expired") { setUndoNotice("Saved undo expired after 24 hours. Missing targets will not be recreated."); return }
          if (undo.kind === "conflict") { setCollaborationError("This saved change can no longer be undone because connected content changed remotely."); return }
          undoAttempt.current = undo.batch
        }
        const inverse = undoAttempt.current
        try { const canonical = await write.mutateAsync({ base_seq: inverse.base_seq, operation: { kind: "batch", batch: inverse } }); undoAttempt.current = null; setCommands(previous => previous.slice(0, -1)); replace(canonical) } catch (error) { if (error instanceof PageBoardError && error.status < 500) undoAttempt.current = null }
      }}>Undo last saved change</Button>
      <p className="w-full text-xs text-muted-foreground">Saved undo expires after 24 hours and cannot recreate compacted targets.</p>
      {undoNotice && <p role="status" className="w-full text-xs text-muted-foreground">{undoNotice}</p>}
      {dirty && <Button size="sm" variant="outline" onClick={() => {
        const blob = new Blob([JSON.stringify({ schema_version: 1, base_seq: base.seq, draft: localDraft }, null, 2)], { type: "application/json" })
        const url = URL.createObjectURL(blob); const anchor = document.createElement("a"); anchor.href = url; anchor.download = "board-draft.json"; anchor.click(); setTimeout(() => URL.revokeObjectURL(url), 0)
      }}>Export unsaved draft</Button>}
    </div>
    {query.error && <p role="alert" className="px-4 pb-3 text-sm text-warn">Could not refresh board access or changes. Showing the last authorized snapshot. {query.error.message}</p>}
    {query.data && query.data.seq !== base.seq && dirty && <p role="status" className="px-4 pb-3 text-sm text-warn">New changes are available. Your drawing stays on sequence {base.seq}; saving will check for conflicts.</p>}
    {write.error && !pinning && <p role="alert" className="px-4 pb-3 text-sm text-destructive">{write.error.message} Your unsaved drawing is retained. Reload latest deliberately to discard it.</p>}
    {invalid && <p role="alert" className="px-4 pb-3 text-sm text-destructive">Only local drawing shapes, text and approved artifact placements can be saved. Remove unsupported links or embeds. Artifact pins support moving and resizing; remove rotation, groups or frame membership before saving.</p>}
    {!authorization.can_write && <p className="px-4 pb-3 text-sm text-muted-foreground">Read-only board. Your access does not allow saving changes.</p>}
    {!imageAccessLost && <Editor initialViewState={viewState.current} onViewState={rememberView} onInteraction={stopFollowing} focusPoint={follow.focusPoint ?? focusPoint} onSelection={onSelection} files={images.files} insertImage={insertImage} onImageInserted={imageInserted} scene={editorScene} onBusy={setBusy} key={generation} base={base} authorization={{ ...authorization, placements: shownPlacements }} workspaceId={workspaceId} disabled={!authorization.can_write || write.isPending || imageUpload.pending || uncertain} onChange={onChange} collaborators={collaborators} onPointer={onPointer} />}
    <div className="space-y-2 border-t p-4"><p className="text-xs text-muted-foreground">Undo applies to the current unsaved drawing. Saving or reloading resets drawing history. Remote disjoint changes merge when drawing is idle. Overlapping changes require review; saving stays explicit.</p>{previewFrozen && <p className="text-xs text-muted-foreground">Artifact previews stay on their displayed revisions while you edit.</p>}{shownPlacements.map(item => <BoardPlacementLink key={item.id} workspaceId={workspaceId} placement={item} disabled={!authorization.can_write || previewFrozen} onModeChange={authorization.can_write ? placement => { write.reset(); setModeIntent(pinModeIntent(base.seq, placement)) } : undefined} />)}</div>
    <BoardElementPermissions key={`permissions:${workspaceId}:${slug}`} workspaceId={workspaceId} slug={slug} board={authorization} selectedIds={selectedSavedIds} disabled={dirty || busy || write.isPending || uncertain || imageUpload.pending} onDirtyChange={setPermissionDirty} />
    <BoardComments key={`${workspaceId}:${slug}`} workspaceId={workspaceId} slug={slug} board={base} selectedElementId={selectedSavedIds.length === 1 ? selectedSavedIds[0] : undefined} onDirtyChange={setCommentDirty} onViewAnchor={anchor => { if (anchor.type === "board-element" && anchor.snapshot) { stopFollow(); setFocusLabel(undefined); setFocusPoint({ x: anchor.snapshot.x + anchor.snapshot.width / 2, y: anchor.snapshot.y + anchor.snapshot.height / 2, requestId: crypto.randomUUID() }) } }} />
    {pinning && <BoardPinDialog workspaceId={workspaceId} pending={write.isPending} error={write.error?.message ?? null} onClose={() => { if (uncertain) return; setPinning(false); write.reset() }} onPin={async (source_page_slug, source_revision) => {
      try { replace(await write.mutateAsync({ base_seq: base.seq, operation: { kind: "pin", source_page_slug, source_revision, x: 80 + base.placements.length * 40, y: 100 + base.placements.length * 40, width: 420, height: 300 } })); setPinning(false) } catch { /* The selected source stays available for explicit retry. */ }
    }} />}
    <BoardAgentWork onFocusWorkArea={(target, label) => { stopFollow(); setFocusLabel(`${label} · showing its saved target area, not a live agent cursor`); setFocusPoint({ x: target.x + target.width / 2, y: target.y + target.height / 2, requestId: crypto.randomUUID() }) }} files={images.files} workspaceId={workspaceId} slug={slug} board={base} selectedIds={selectedIds} disabled={dirty || uncertain || busy || write.isPending || imageUpload.pending || !authorization.can_write} onDirtyChange={setRequestDirty} />
    {modeIntent && <BoardPinModeDialog intent={modeSourceAllowed ? modeIntent : { ...modeIntent, placement: { ...modeIntent.placement, source: undefined } }} pending={write.isPending} uncertain={uncertain} error={write.error?.message ?? null} onClose={() => { if (!uncertain && !write.isPending) { setModeIntent(null); write.reset() } }} onConfirm={async () => {
      try {
        const canonical = await write.mutateAsync({ base_seq: modeIntent.base_seq, operation: { kind: "batch", batch: modeIntent.batch } })
        try { if (!receipt.current) throw new Error("Missing save receipt"); const command = savedBoardCommand(base, modeIntent.batch, receipt.current, canonical); setUndoNotice(null); setCommands(previous => { const next = [...previous, command].slice(-16); while (next.length && JSON.stringify(next).length > 4 * 1024 * 1024) next.shift(); return next }) } catch { setCommands([]); setUndoNotice("Pin mode saved. Saved undo is unavailable because its original mode or receipt could not be confirmed.") }
        setModeIntent(null); replace(canonical)
      } catch { /* Keep captured baseline, CAS and mutation identity. */ }
    }} />}
    {imageDialog && <BoardImageUploadDialog baseSeq={base.seq} pending={imageUpload.pending} error={imageUpload.error} onClose={() => setImageDialog(false)} onUpload={async intent => {
      if (dirty || uncertain || write.isPending) return null
      const result = await imageUpload.upload(intent)
      if (result) { replace(result.board); setInsertImage(result.file); setImageDialog(false) }
      return result
    }} />}
    <Dialog open={confirmReload} onOpenChange={setConfirmReload}><DialogContent><DialogHeader><DialogTitle>Discard unsaved drawing?</DialogTitle><DialogDescription>Reloading replaces your local drawing with the latest saved board and clears undo history. Your pending changes will be lost.</DialogDescription></DialogHeader><DialogFooter><Button variant="ghost" onClick={() => setConfirmReload(false)}>Keep drawing</Button><Button variant="destructive" onClick={() => void reload()}>Discard and reload</Button></DialogFooter></DialogContent></Dialog>
  </section>
}
declare global { interface Window { EXCALIDRAW_ASSET_PATH: string } }
