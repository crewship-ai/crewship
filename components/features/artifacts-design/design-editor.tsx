"use client"

import { useCallback, useEffect, useLayoutEffect, useRef, useState, type KeyboardEvent as ReactKeyboardEvent, type PointerEvent } from "react"
import dynamic from "next/dynamic"
import { ArrowDownToLine, ArrowUpToLine, ChevronDown, ChevronLeft, ChevronRight, Download, Eye, EyeOff, Frame, ImagePlus, Info, Layers, Lock, LockOpen, MessageSquare, MousePointer2, Play, Plus, Redo2, Save, Scan, SlidersHorizontal, Square, Trash2, Type, Undo2, ZoomIn, ZoomOut } from "lucide-react"
import * as Y from "yjs"
import { useArtifactAgentDispatch } from "@/hooks/use-artifact-agent-dispatch"
import { useSessionSafe } from "@/hooks/use-auth"
import { artifactDraftKey, loadArtifactDraft, writeArtifactDraft, clearArtifactDraft, artifactDraftRevision } from "@/lib/artifact-drafts"
import { artifactsDesign, artifactsDesignHideLock, artifactsDesignPrototype, artifactsDesignTextStyle } from "@/lib/feature-flags"
import { Button } from "@/components/ui/button"
import { CreateSurface, CreateSurfaceBody, CreateSurfaceField, CreateSurfaceFooter, CreateSurfaceHeader } from "@/components/layout/create-surface"
import { apiFetch } from "@/lib/api-fetch"
import { Input } from "@/components/ui/input"
import { DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuLabel, DropdownMenuTrigger } from "@/components/ui/dropdown-menu"
import { ArtifactAgentMode, ArtifactDrawerSection, ArtifactSlot, ArtifactTool, ArtifactToolSeparator, useArtifactFrame } from "@/components/features/artifacts-library/frame"
import { enterFullscreen, exitFullscreen } from "@/components/features/artifacts-library/kinds/canvas-fullscreen"
import { cn } from "@/lib/utils"
import type { DesignConnection, DesignPeer } from "./design-provider"
import { useDesign } from "./use-design"
import { readablePeerFill } from "./peer-colors"
import { designHiddenIds, designLockedIds, designNodes, DESIGN_FONT_SIZE, DESIGN_FONT_WEIGHTS, DESIGN_LINE_HEIGHT, designTextStyle, designLinkTarget, designScreenOf, removeDesignNode, reorderDesignNode, restoreDesignConflict, safeImageSource, setDesignNodeLink, upsertDesignNode, type DesignConflict, type DesignNode, type DesignNodeType, type DesignOrderMove } from "./design-document"
import { DesignNodeShape } from "./design-node-shape"
import { DesignPrototypePlayer } from "./design-prototype-player"
import { DesignExportButton } from "./design-export-button"
import { groupBox, marqueeHits, moveGroup, resizeGroup, selectionRoots, toggleSelection } from "./design-selection"
import { DRAG_THRESHOLD, fitView, focusView, panBy, pinchOf, pinchView, pressIntent, resizeBox, RESIZE_HANDLES, toCanvas, toScreen, typingTarget, wheelGesture, zoomAround, type DesignBox, type DesignView, type Point, type ResizeHandle } from "./design-viewport"

function encodedDesignState(doc: Y.Doc,maxBytes=Infinity) {
 const state=Y.encodeStateAsUpdate(doc)
 if(state.byteLength>maxBytes)throw new Error("Canvas exceeds the new Design size limit")
 let binary=""
 for(let index=0;index<state.length;index+=8192) binary+=String.fromCharCode(...state.subarray(index,index+8192))
 return btoa(binary)
}

const DesignAccessDialog = dynamic(() => import("./design-access-dialog").then(module => module.DesignAccessDialog), { ssr: false })

/** What the editor reports to the artifact frame around it: the chip, presence and who may edit. */
export interface DesignEditorFrameState {
 ready: boolean
 version?: number
 connection: DesignConnection
 canEdit: boolean
 editable: boolean
 unsynced: boolean
 checkpointPending: boolean
 checkpointVersion?: number
 historyFull: boolean
 peers: Pick<DesignPeer, "clientId" | "name" | "color">[]
}

const ZOOM_STEPS = [0.1, 0.25, 0.5, 0.75, 1, 1.5, 2, 4]
const FIT_PAD = 48

/**
 * The Design canvas. Standalone (the `?design=1` route) it draws its own
 * header, side panels and toolbar. Inside the ArtifactFrame (Harbor 2 v87) the
 * same controls move into the frame: view (artboard picker, Layers), output
 * (Play, Export), tools (only while editing), viewport (zoom, Fit), nav
 * (previous and next artboard), panel (Layers), and the drawer tabs
 * Properties, Comments and History. A phone reviews only: fit to width,
 * comments allowed, no editing.
 */
export function DesignEditor({ workspaceId, slug, initialVersion, onFrameState }: { workspaceId: string; slug: string; initialVersion?: number; onFrameState?: (state: DesignEditorFrameState) => void }) {
 const artifactFrame = useArtifactFrame()
 const framed = artifactFrame !== null
 const phone = artifactFrame?.layout === "phone"
 const [accessOpen, setAccessOpen] = useState(false)
 const [version, setVersion] = useState(initialVersion)
 const design = useDesign(workspaceId, slug, version)
 // Address changes select another snapshot without remounting the live editor.
 // Wait for confirmation of local writes; a restore's internal return to live
 // must not reapply an already consumed address pin.
 const [addressVersion, setAddressVersion] = useState(initialVersion)
 const addressPending = addressVersion !== initialVersion
 useLayoutEffect(() => {
  if (addressVersion === initialVersion || design.unsynced || design.draftError || design.save.isPending || design.restore.isPending) return
  setAddressVersion(initialVersion)
  setVersion(initialVersion)
 }, [initialVersion, addressVersion, design.unsynced, design.draftError, design.save.isPending, design.restore.isPending])
 const session = useSessionSafe()
 const commentPageId = design.query.data?.page_id
 const commentKey = artifactDraftKey("design-comment", session?.data?.user?.id, workspaceId, !design.query.isError ? commentPageId : null)
 const [draftState, setDraftState] = useState<{ key: string | null; text: string }>({ key: null, text: "" })
 const draft = draftState.key === commentKey ? draftState.text : ""
 const currentDraft = useRef({ key: commentKey, text: draft }); currentDraft.current = { key: commentKey, text: draft }
 const [delivery, setDelivery] = useState<Record<string, string>>({})
 const [commentError, setCommentError] = useState("")
 const [downloadError, setDownloadError] = useState("")
 const [continueOpen,setContinueOpen]=useState(false)
 const [continueName,setContinueName]=useState("")
 const [continueSlug,setContinueSlug]=useState("")
 const [continueError,setContinueError]=useState("")
 const [continuing,setContinuing]=useState(false)
 const [continued,setContinued]=useState<{scope:string;page:string}|null>(null)
 const continueEpoch=useRef(0)
 const human=session?.data?.user?.id
 const continueScope=JSON.stringify([human,workspaceId,commentPageId])
 useEffect(()=>{continueEpoch.current++;setContinueOpen(false);setContinuing(false);setContinued(null);setContinueError("");return()=>{continueEpoch.current++}},[continueScope])
 const agent = useArtifactAgentDispatch(workspaceId, design.query.data?.page_id, design.query.data?.origin, Boolean(design.query.data?.can_edit && !design.query.isError), "design")
 useEffect(() => {
  let active = true
  if (commentKey) void loadArtifactDraft(commentKey).then(text => { if (active) setDraftState({ key: commentKey, text }) }, () => { if (active) setCommentError("Private draft could not be opened. Your stored copy is retained; reconnect and reopen.") })
  return () => { active = false }
 }, [commentKey])
 const changeDraft = (value: string) => {
  if (!commentKey || draftState.key !== commentKey) return
  setDraftState({ key: commentKey, text: value })
  void writeArtifactDraft(commentKey, value).catch(() => setCommentError("Draft storage failed. Keep this tab open."))
 }
 // One or more selected layers (#5217): Shift or Ctrl/⌘ click and a marquee add to it.
 const [selectedIds, setSelectedIds] = useState<string[]>([])
 const setSelectedId = (id: string | null) => setSelectedIds(id ? [id] : [])
 const [agentChat, setAgentChat] = useState("")
 const [imageURL, setImageURL] = useState("")
 const [panel, setPanel] = useState<"properties" | "history">("properties")
 const canvas = useRef<SVGSVGElement>(null)
 const prototype = artifactsDesignPrototype()
 const [playing, setPlaying] = useState<string | null>(null)
 const playButton = useRef<HTMLButtonElement>(null)
 const wasPlaying = useRef(false)
 useEffect(() => { if (wasPlaying.current && !playing) playButton.current?.focus(); wasPlaying.current = Boolean(playing) }, [playing])
 const chosen = selectedIds.filter(id => design.nodes.some(node => node.id === id))
 const selectedId = chosen.length === 1 ? chosen[0] : null
 const selected = selectedId ? design.nodes.find(node => node.id === selectedId) : undefined
 const many = chosen.length > 1
 // Hidden layers are not drawn; locked ones cannot be picked, moved or deleted on the canvas (#5218).
 const hiddenIds = designHiddenIds(design.nodes)
 const lockedIds = designLockedIds(design.nodes)
 const movable = chosen.filter(id => !lockedIds.has(id) && !hiddenIds.has(id))
 // Undo and redo take back only this person's own edits (Ctrl/⌘+Z,
 // Shift+Ctrl/⌘+Z or Ctrl+Y); a text field keeps its own undo.
 const historyKeys = useRef({ undo: design.undo, redo: design.redo }); historyKeys.current = { undo: design.undo, redo: design.redo }
 useEffect(() => {
  if (!design.editable) return
  const onKey = (event: KeyboardEvent) => {
   if (!(event.ctrlKey || event.metaKey) || event.altKey) return
   const target = event.target as HTMLElement | null
   if (target && (target.isContentEditable || /^(INPUT|TEXTAREA|SELECT)$/.test(target.tagName))) return
   const key = event.key.toLowerCase()
   const redo = (key === "z" && event.shiftKey) || (key === "y" && !event.metaKey)
   if (key !== "z" && !redo) return
   event.preventDefault()
   if (redo) historyKeys.current.redo?.(); else historyKeys.current.undo?.()
  }
  window.addEventListener("keydown", onKey)
  return () => window.removeEventListener("keydown", onKey)
 }, [design.editable])
 // Others see which layer this person has selected on the live canvas (awareness
 // only, never the document). A reader or a History view shares nothing.
 const liveSelection = design.editable && !version
 const shareSelection = useRef(design.setSelection); shareSelection.current = design.setSelection
 const chosenKey = JSON.stringify(chosen)
 const frames = design.nodes.filter(node => node.type === "frame")
 const selectedFrame = selected?.type === "frame" ? selected.id : selected?.parentId
 const position = (node: DesignNode, seen = new Set<string>()): { x: number; y: number } => {
  if (seen.has(node.id)) return { x: node.x || 0, y: node.y || 0 }
  seen.add(node.id)
  const parent = design.nodes.find(item => item.id === node.parentId)
  const offset = parent ? position(parent, seen) : { x: 0, y: 0 }
  return { x: offset.x + (Number(node.x) || 0), y: offset.y + (Number(node.y) || 0) }
 }
 // A phone reviews: it may select and comment, never change the canvas (Harbor 2 v87).
 const reviewOnly = framed && phone && design.editable
 const canChange = design.editable && !reviewOnly
 // Read current CRDT flags at mutation time, including inherited parent flags.
 const currentEditableIds = useCallback((visibleOnly = false) => {
  const nodes = design.doc ? designNodes(design.doc) : []
  const locked = designLockedIds(nodes)
  const hidden = visibleOnly ? designHiddenIds(nodes) : new Set<string>()
  return new Set(nodes.filter(node => !locked.has(node.id) && !hidden.has(node.id)).map(node => node.id))
 }, [design.doc])
 const canEditSelected = canChange && Boolean(selected) && !lockedIds.has(selected!.id)
 const patch = (values: Partial<DesignNode>) => {
  if (selected && design.doc && canEditSelected && currentEditableIds().has(selected.id)) upsertDesignNode(design.doc, { id: selected.id, ...values })
 }
 const order = (move: DesignOrderMove) => { if (selected && design.doc && canEditSelected && currentEditableIds().has(selected.id)) reorderDesignNode(design.doc, selected.id, move) }
 const add = (type: DesignNodeType) => {
  if (!design.doc || !canChange) return
  const id = crypto.randomUUID()
  const frame = type !== "frame" ? selectedFrame ?? frames[0]?.id : undefined
  const count = design.nodes.filter(node => node.parentId === frame && node.type !== "frame").length
  const node: DesignNode = {
   id, type, name: type === "frame" ? `Screen ${frames.length + 1}` : type === "text" ? "Text" : type === "image" ? "Image" : "Rectangle",
   x: type === "frame" ? 64 + frames.length * 424 : 24,
   y: type === "frame" ? 64 : 32 + count * 56,
   width: type === "frame" ? 360 : type === "text" ? 280 : 200,
   height: type === "frame" ? 640 : type === "text" ? 48 : type === "image" ? 160 : 48,
   fill: type === "frame" ? "#ffffff" : type === "text" ? "#172554" : "#2563eb",
   ...(frame ? { parentId: frame } : {}),
   ...(type === "text" ? { text: "Your text here" } : {}),
   ...(type === "image" ? { src: safeImageSource(imageURL) ?? "" } : {}),
  }
  upsertDesignNode(design.doc, node)
  setSelectedId(id)
  setPanel("properties")
  if (type === "frame") setFocus(frames.length)
 }
 // The viewport (design-viewport.ts): pan and zoom are this browser's view only,
 // never written into the document, history or awareness.
 const viewport = useRef<HTMLDivElement | null>(null)
 const [viewportElement, setViewportElement] = useState<HTMLDivElement | null>(null)
 const attachViewport = useCallback((element: HTMLDivElement | null) => { viewport.current = element; setViewportElement(element) }, [])
 const [placed, setPlaced] = useState<{ fit: true } | { fit: false; view: DesignView }>(() => framed ? { fit: true } : { fit: false, view: { scale: 1, x: 0, y: 0 } })
 const [viewportSize, setViewportSize] = useState({ width: 0, height: 0 })
 const [focus, setFocus] = useState(0)
 const [layersOpen, setLayersOpen] = useState(false)
 const focusIndex = Math.min(focus, Math.max(0, frames.length - 1))
 const focused = frames[focusIndex]
 useLayoutEffect(() => {
  const element = viewportElement
  if (!element) return
  const measure = () => setViewportSize(previous => previous.width === element.clientWidth && previous.height === element.clientHeight ? previous : { width: element.clientWidth, height: element.clientHeight })
  measure()
  if (typeof ResizeObserver === "undefined") return
  const observer = new ResizeObserver(measure)
  observer.observe(element)
  return () => observer.disconnect()
 }, [viewportElement])
 const box = (nodes: DesignNode[]) => {
  if (!nodes.length) return null
  const places = nodes.map(node => ({ ...position(node), width: Math.max(1, Number(node.width) || 1), height: Math.max(1, Number(node.height) || 1) }))
  const left = Math.min(...places.map(place => place.x)), top = Math.min(...places.map(place => place.y))
  return { x: left, y: top, width: Math.max(...places.map(place => place.x + place.width)) - left, height: Math.max(...places.map(place => place.y + place.height)) - top }
 }
 // The frame's toolbars float over the canvas: keep the fitted area clear of them.
 const fitOptions = !framed ? { top: 24, bottom: 24, side: 24 } : phone ? { top: 88, bottom: 0, side: 12, widthOnly: true } : { top: 80, bottom: 64, side: FIT_PAD }
 // Fit: every artboard and loose layer on a wide screen; the focused artboard's width on a phone.
 const fitTarget = phone && focused ? box([focused]) : box(design.nodes.filter(node => !node.parentId || !design.nodes.some(parent => parent.id === node.parentId)))
 const fitted: DesignView = fitTarget && viewportSize.width ? fitView(fitTarget, viewportSize, fitOptions) : { scale: 1, x: fitOptions.side, y: fitOptions.top }
 const view: DesignView = placed.fit ? fitted : placed.view
 const scale = view.scale
 const fittedNow = useRef(fitted); fittedNow.current = fitted
 const viewNow = useRef(view); viewNow.current = view
 const setView = (next: DesignView) => setPlaced({ fit: false, view: next })
 /** Changes the view from whatever it is when React applies the change, so quick wheel events add up. */
 const changeView = useCallback((change: (view: DesignView) => DesignView) => setPlaced(previous => ({ fit: false, view: change(previous.fit ? fittedNow.current : previous.view) })), [])
 const center = (): Point => ({ x: viewportSize.width / 2, y: (viewportSize.height + fitOptions.top - fitOptions.bottom) / 2 })
 const step = (direction: 1 | -1) => {
  const next = direction > 0 ? ZOOM_STEPS.find(value => value > scale + 0.001) : [...ZOOM_STEPS].reverse().find(value => value < scale - 0.001)
  setView(zoomAround(view, center(), next ?? (direction > 0 ? ZOOM_STEPS.at(-1)! : ZOOM_STEPS[0])))
 }
 const local = (event: { clientX: number; clientY: number }): Point => {
  const bounds = viewport.current?.getBoundingClientRect()
  return { x: event.clientX - (bounds?.left ?? 0), y: event.clientY - (bounds?.top ?? 0) }
 }
 // One gesture at a time. Escape, pointercancel and window blur put back where it started.
 type Gesture =
  | { kind: "pan"; pointerId: number; start: Point; view: DesignView; moved: boolean; tapNode?: string; touch: boolean; button: number }
  | { kind: "move"; pointerId: number; start: Point; id: string; x: number; y: number; moved: boolean; touch: boolean }
  | { kind: "resize"; pointerId: number; start: Point; id: string; handle: ResizeHandle; box: DesignBox; moved: boolean; touch: boolean }
  // Several selected layers move or resize together; `nodes` is the canvas when the drag began.
  | { kind: "group"; pointerId: number; start: Point; ids: string[]; nodes: DesignNode[]; moved: boolean; touch: boolean; tapNode: string }
  | { kind: "group-resize"; pointerId: number; start: Point; ids: string[]; nodes: DesignNode[]; handle: ResizeHandle; box: DesignBox; moved: boolean; touch: boolean }
  // A left drag on the empty canvas selects the layers wholly inside it (Shift: adds to the selection).
  | { kind: "marquee"; pointerId: number; start: Point; base: string[]; additive: boolean; moved: boolean; touch: boolean }
  | { kind: "pinch"; ids: [number, number]; view: DesignView; from: ReturnType<typeof pinchOf> }
  // Fingers still down after a pinch ended or was cancelled start nothing until all are up.
  | { kind: "spent" }
 const gesture = useRef<Gesture | null>(null)
 const [marquee, setMarquee] = useState<{ a: Point; b: Point } | null>(null)
 // While a marquee is drawn the selection changes on every move; peers get the result when it ends.
 const marqueeOpen = marquee !== null
 useEffect(() => { if (!marqueeOpen) shareSelection.current?.(liveSelection ? JSON.parse(chosenKey) as string[] : []) }, [chosenKey, liveSelection, marqueeOpen])
 /** Writes several layers in one change, so peers and undo see one step. */
 const writeAll = (patches: (Partial<DesignNode> & { id: string })[]) => {
  const doc = design.doc
  if (!doc || !canChange || !patches.length) return
  const allowed = currentEditableIds(true)
  const valid = patches.filter(patch => allowed.has(patch.id))
  if (!valid.length) return
  doc.transact(() => { for (const patch of valid) upsertDesignNode(doc, patch) }, "editor")
 }
 /** Puts every layer of a group gesture back where the drag began. */
 const restoreGroup = (current: Extract<Gesture, { kind: "group" | "group-resize" }>) => writeAll(selectionRoots(current.nodes, current.ids).map(id => {
  const node = current.nodes.find(item => item.id === id)!
  const position = { id, x: Number(node.x) || 0, y: Number(node.y) || 0 }
  // A move never changed dimensions: cancellation must keep a peer's resize.
  return current.kind === "group" ? position : { ...position, width: Number(node.width) || 0, height: Number(node.height) || 0 }
 }))
 const touches = useRef(new Map<number, Point>())
 const [panning, setPanning] = useState(false)
 const [space, setSpace] = useState(false)
 const spaceHeld = useRef(false)
 const pointerInside = useRef(false)
 /** A right-button pan that ends outside the canvas must not open the browser menu there (Windows opens it on release). */
 const menuAfterPan = useRef(0)
 const cancelGesture = () => {
  const current = gesture.current
  // Fingers still down after a cancel start nothing until they are all up.
  gesture.current = touches.current.size ? { kind: "spent" } : null
  setPanning(false)
  if (!current || current.kind === "spent") return false
  if (current.kind === "move" && current.moved && design.doc && canChange) writeAll([{ id: current.id, x: current.x, y: current.y }])
  if (current.kind === "resize" && current.moved && design.doc && canChange) writeAll([{ id: current.id, ...current.box }])
  if ((current.kind === "group" || current.kind === "group-resize") && current.moved) restoreGroup(current)
  if (current.kind === "marquee") { setMarquee(null); setSelectedIds(current.base) }
  if (current.kind === "pan" || current.kind === "pinch") setView(current.view)
  return true
 }
 const cancelNow = useRef(cancelGesture); cancelNow.current = cancelGesture
 // Retire the whole gesture when a live update makes one of its targets
 // unavailable. A later unlock in the same press must not revive stale boxes.
 useEffect(() => {
  const doc = design.doc
  if (!doc) return
  const retire = () => {
   const current = gesture.current
   if (!current) return
   const ids = current.kind === "move" || current.kind === "resize" ? [current.id]
    : current.kind === "group" || current.kind === "group-resize" ? current.ids : []
   if (!ids.length) return
   const allowed = currentEditableIds(true)
   if (ids.some(id => !allowed.has(id))) cancelNow.current()
  }
  doc.on("update", retire)
  return () => { doc.off("update", retire) }
 }, [design.doc, currentEditableIds])
 const selectNode = (node: DesignNode) => {
  setSelectedId(node.id)
  setPanel("properties")
  if (framed && !phone && artifactFrame?.drawerTab === null) artifactFrame.openDrawer("properties")
 }
 const capture = (event: PointerEvent<HTMLDivElement>) => { try { event.currentTarget.setPointerCapture(event.pointerId) } catch { /* capture is best effort */ } }
 const pressDown = (event: PointerEvent<HTMLDivElement>) => {
  const at = local(event)
  if (event.button === 1) event.preventDefault() // no middle-button autoscroll
  menuAfterPan.current = 0
  if (event.pointerType === "touch") {
   // The first finger of a new touch: nothing else is down, whatever a lost pointerup left behind.
   // A touch gesture still open then lost its pointerup; it ends where it is.
   if (event.isPrimary) {
    touches.current.clear()
    const open = gesture.current
    if (open && (open.kind === "pinch" || open.kind === "spent" || open.touch)) { gesture.current = null; setPanning(false) }
   }
   touches.current.set(event.pointerId, at)
   if (gesture.current?.kind === "spent" || gesture.current?.kind === "pinch") return
   if (touches.current.size >= 2) {
    // A second finger turns whatever the first one started into a pinch from the current view.
    const started = gesture.current
    if (started?.kind === "move" && started.moved && design.doc && canChange) writeAll([{ id: started.id, x: started.x, y: started.y }])
    if (started?.kind === "resize" && started.moved && design.doc && canChange) writeAll([{ id: started.id, ...started.box }])
    if ((started?.kind === "group" || started?.kind === "group-resize") && started.moved) restoreGroup(started)
    if (started?.kind === "marquee") { setMarquee(null); setSelectedIds(started.base) }
    const ids = [...touches.current.keys()].slice(-2) as [number, number]
    gesture.current = { kind: "pinch", ids, view, from: pinchOf(touches.current.get(ids[0])!, touches.current.get(ids[1])!) }
    setPanning(true)
    return
   }
  }
  if (gesture.current) return
  const handle = (event.target as Element | null)?.closest?.("[data-design-handle]")?.getAttribute("data-design-handle") as ResizeHandle | null | undefined
  if (handle && event.button === 0 && !spaceHeld.current && canChange && movable.length > 1 && (event.pointerType !== "touch" || event.isPrimary)) {
   const box = groupBox(design.nodes, movable)
   if (box) {
    gesture.current = { kind: "group-resize", pointerId: event.pointerId, start: at, ids: movable, nodes: design.nodes, handle, box, moved: false, touch: event.pointerType === "touch" }
    viewport.current?.focus({ preventScroll: true })
    capture(event)
    return
   }
  }
  if (handle && event.button === 0 && !spaceHeld.current && canChange && selected && (event.pointerType !== "touch" || event.isPrimary)) {
   gesture.current = { kind: "resize", pointerId: event.pointerId, start: at, id: selected.id, handle, box: { x: Number(selected.x) || 0, y: Number(selected.y) || 0, width: Math.max(1, Number(selected.width) || 1), height: Math.max(1, Number(selected.height) || 1) }, moved: false, touch: event.pointerType === "touch" }
   viewport.current?.focus({ preventScroll: true })
   capture(event)
   return
  }
  const nodeId = (event.target as Element | null)?.closest?.("[data-design-node]")?.getAttribute("data-design-node")
  const node = nodeId ? design.nodes.find(item => item.id === nodeId) : undefined
  const intent = pressIntent({ button: event.button, pointerType: event.pointerType, space: spaceHeld.current, onNode: Boolean(node), canMove: canChange })
  if (intent === "none") return
  // Shift or Ctrl/⌘ click adds a layer to the selection or takes it out.
  if (node && (intent === "select" || intent === "move") && (event.shiftKey || event.ctrlKey || event.metaKey) && event.pointerType !== "touch") {
   setSelectedIds(toggleSelection(chosen, node.id))
   viewport.current?.focus({ preventScroll: true })
   return
  }
  if (intent === "deselect") {
   viewport.current?.focus({ preventScroll: true })
   if (event.pointerType === "touch") { setSelectedId(null); return }
   gesture.current = { kind: "marquee", pointerId: event.pointerId, start: at, base: event.shiftKey ? chosen : [], additive: event.shiftKey, moved: false, touch: false }
   capture(event)
   return
  }
  // Dragging one of several selected layers moves them all.
  if (node && intent === "move" && movable.length > 1 && movable.includes(node.id)) {
   viewport.current?.focus({ preventScroll: true })
   gesture.current = { kind: "group", pointerId: event.pointerId, start: at, ids: movable, nodes: design.nodes, moved: false, touch: event.pointerType === "touch", tapNode: node.id }
   capture(event)
   return
  }
  if (node && (intent === "select" || intent === "move")) selectNode(node)
  viewport.current?.focus({ preventScroll: true })
  if (intent === "select") return
  if (intent === "move" && node) gesture.current = { kind: "move", pointerId: event.pointerId, start: at, id: node.id, x: Number(node.x) || 0, y: Number(node.y) || 0, moved: false, touch: event.pointerType === "touch" }
  else if (intent === "pan") {
   gesture.current = { kind: "pan", pointerId: event.pointerId, start: at, view, moved: false, touch: event.pointerType === "touch", button: event.button, ...(node ? { tapNode: node.id } : {}) }
   setPanning(true)
  }
  capture(event)
 }
 const pressMove = (event: PointerEvent<HTMLDivElement>) => {
  const at = local(event)
  design.setCursor(toCanvas(view, at))
  if (event.pointerType === "touch" && touches.current.has(event.pointerId)) touches.current.set(event.pointerId, at)
  const current = gesture.current
  if (!current || current.kind === "spent") return
  if (current.kind === "pinch") {
   const [a, b] = current.ids.map(id => touches.current.get(id))
   if (a && b) setView(pinchView(current.view, current.from, pinchOf(a, b)))
   return
  }
  if (current.pointerId !== event.pointerId) return
  const dx = at.x - current.start.x, dy = at.y - current.start.y
  if (!current.moved && Math.hypot(dx, dy) < DRAG_THRESHOLD) return
  if (!current.moved && placed.fit) setView(view) // a drag holds the view still, even if the fitted area changes
  current.moved = true
  if (current.kind === "pan") setView(panBy(current.view, dx, dy))
  else if (current.kind === "marquee") {
   const a = toCanvas(view, current.start), b = toCanvas(view, at)
   setMarquee({ a, b })
   const hits = marqueeHits(design.nodes, a, b).filter(id => !hiddenIds.has(id) && !lockedIds.has(id))
   setSelectedIds(current.additive ? [...current.base, ...hits.filter(id => !current.base.includes(id))] : hits)
  }
  else if (current.kind === "group") writeAll(moveGroup(current.nodes, current.ids, dx / scale, dy / scale))
  else if (current.kind === "group-resize") writeAll(resizeGroup(current.nodes, current.ids, current.box, resizeBox(current.box, current.handle, dx / scale, dy / scale)))
  else if (current.kind === "resize") { if (design.doc && canChange) writeAll([{ id: current.id, ...resizeBox(current.box, current.handle, dx / scale, dy / scale) }]) }
  else if (design.doc && canChange) writeAll([{ id: current.id, x: Math.round(current.x + dx / scale), y: Math.round(current.y + dy / scale) }])
 }
 const pressUp = (event: PointerEvent<HTMLDivElement>) => {
  touches.current.delete(event.pointerId)
  const current = gesture.current
  if (!current) return
  if (current.kind === "pinch" || current.kind === "spent") {
   // Lifting one finger of the pinch ends it; the fingers left start nothing.
   if (current.kind === "spent" || current.ids.includes(event.pointerId)) { gesture.current = touches.current.size ? { kind: "spent" } : null; setPanning(false) }
   return
  }
  if (current.pointerId !== event.pointerId) return
  gesture.current = null
  setPanning(false)
  if (current.kind === "pan" && current.button === 2 && current.moved) menuAfterPan.current = Date.now()
  // A click on the empty canvas clears the selection; a drawn marquee keeps what it holds.
  if (current.kind === "marquee") { setMarquee(null); if (!current.moved && !current.additive) setSelectedId(null) }
  // A plain click on one of several selected layers selects just that layer.
  if (current.kind === "group" && !current.moved) { const node = design.nodes.find(item => item.id === current.tapNode); if (node) selectNode(node) }
  // A tap with a finger selects or clears like a click; a right or middle click changes nothing.
  if (current.kind === "pan" && current.touch && !current.moved) {
   const node = current.tapNode ? design.nodes.find(item => item.id === current.tapNode) : undefined
   if (node) selectNode(node); else setSelectedId(null)
  }
 }
 const pressCancel = (event: PointerEvent<HTMLDivElement>) => {
  const current = gesture.current
  if (current && (current.kind === "pinch" ? current.ids.includes(event.pointerId) : current.kind !== "spent" && current.pointerId === event.pointerId)) cancelGesture()
  touches.current.delete(event.pointerId)
  if (gesture.current?.kind === "spent" && touches.current.size === 0) gesture.current = null
 }
 const viewKey = (event: ReactKeyboardEvent<HTMLDivElement>) => {
  if (typingTarget(event.target)) return
  if (event.key === "Escape") { if (!cancelGesture()) setSelectedId(null); event.preventDefault(); return }
  // With a layer selected, arrows nudge it (Shift: by 10), Delete removes it and
  // [ ] change its order (with Ctrl/⌘: to the back or front).
  if (many && canChange && design.doc && !gesture.current) {
   const nudge: Record<string, [number, number]> = { ArrowLeft: [-1, 0], ArrowRight: [1, 0], ArrowUp: [0, -1], ArrowDown: [0, 1] }
   const by = event.shiftKey ? 10 : 1
   if (nudge[event.key] && !event.altKey && !event.ctrlKey && !event.metaKey) { writeAll(moveGroup(design.nodes, movable, nudge[event.key][0] * by, nudge[event.key][1] * by)); event.preventDefault(); return }
   if (event.key === "Delete" || event.key === "Backspace") {
    // Screens stay: Delete on the keyboard removes layers only, as for one layer.
    const doc = design.doc
    const layers = movable.filter(id => design.nodes.find(node => node.id === id)?.type !== "frame")
    doc.transact(() => { for (const id of layers) removeDesignNode(doc, id) }, "editor")
    setSelectedIds(chosen.filter(id => !layers.includes(id)))
    event.preventDefault(); return
   }
  }
  if (selected && canChange && design.doc && !gesture.current && !lockedIds.has(selected.id)) {
   const nudge: Record<string, [number, number]> = { ArrowLeft: [-1, 0], ArrowRight: [1, 0], ArrowUp: [0, -1], ArrowDown: [0, 1] }
   const by = event.shiftKey ? 10 : 1
   if (nudge[event.key] && !event.altKey && !event.ctrlKey && !event.metaKey) { writeAll([{ id: selected.id, x: (Number(selected.x) || 0) + nudge[event.key][0] * by, y: (Number(selected.y) || 0) + nudge[event.key][1] * by }]); event.preventDefault(); return }
   if ((event.key === "Delete" || event.key === "Backspace") && selected.type !== "frame") { removeDesignNode(design.doc, selected.id); setSelectedId(null); event.preventDefault(); return }
   if (event.key === "]" || event.key === "[") { const toEnd = (event.ctrlKey || event.metaKey) && !event.altKey; order(event.key === "]" ? (toEnd ? "front" : "forward") : (toEnd ? "back" : "backward")); event.preventDefault(); return }
  }
  const pan: Record<string, [number, number]> = { ArrowLeft: [40, 0], ArrowRight: [-40, 0], ArrowUp: [0, 40], ArrowDown: [0, -40] }
  if (pan[event.key]) { setView(panBy(view, ...pan[event.key])); event.preventDefault() }
  else if (event.key === "+" || event.key === "=") { step(1); event.preventDefault() }
  else if (event.key === "-") { step(-1); event.preventDefault() }
  else if (event.shiftKey && (event.key === "!" || event.code === "Digit1")) { setPlaced({ fit: true }); event.preventDefault() }
  else if (event.shiftKey && (event.key === ")" || event.code === "Digit0")) { setView(zoomAround(view, center(), 1)); event.preventDefault() }
 }
 // Wheel and Safari's pinch need non-passive listeners to keep the page itself still.
 useEffect(() => {
  const element = viewportElement
  if (!element) return
  const at = (event: { clientX: number; clientY: number }) => { const bounds = element.getBoundingClientRect(); return { x: event.clientX - bounds.left, y: event.clientY - bounds.top } }
  const wheel = (event: WheelEvent) => {
   event.preventDefault()
   // A drag in progress owns the view; a wheel would be undone by its next move.
   if (gesture.current) return
   const result = wheelGesture(event, element.clientHeight)
   const anchor = at(event)
   changeView(current => result.kind === "zoom" ? zoomAround(current, anchor, current.scale * result.factor) : panBy(current, result.dx, result.dy))
  }
  // Safari reports a trackpad pinch as gesture events. On a touch screen it sends
  // them as well as the touch pointers, which already pinch: ignore them there.
  let pinchStart: DesignView | null = null
  type SafariGesture = Event & { scale: number; clientX: number; clientY: number }
  const gestureStart = (event: Event) => { event.preventDefault(); pinchStart = touches.current.size || gesture.current ? null : viewNow.current }
  const gestureChange = (event: Event) => {
   event.preventDefault()
   const pinch = event as SafariGesture
   if (pinchStart && !touches.current.size && !gesture.current) setPlaced({ fit: false, view: zoomAround(pinchStart, at(pinch), pinchStart.scale * pinch.scale) })
  }
  const gestureEnd = (event: Event) => { event.preventDefault(); pinchStart = null }
  element.addEventListener("wheel", wheel, { passive: false })
  element.addEventListener("gesturestart", gestureStart, { passive: false } as AddEventListenerOptions)
  element.addEventListener("gesturechange", gestureChange, { passive: false } as AddEventListenerOptions)
  element.addEventListener("gestureend", gestureEnd, { passive: false } as AddEventListenerOptions)
  return () => {
   element.removeEventListener("wheel", wheel)
   element.removeEventListener("gesturestart", gestureStart)
   element.removeEventListener("gesturechange", gestureChange)
   element.removeEventListener("gestureend", gestureEnd)
  }
 }, [viewportElement, changeView])
 // Space + drag pans with the canvas focused, or with the pointer over it while
 // nothing else has focus; a focused button, menu or text field keeps its Space.
 // Leaving the window (blur) ends any gesture where it started.
 useEffect(() => {
  const down = (event: KeyboardEvent) => {
   const target = event.target as Node | null
   const free = target === document.body || target === document.documentElement || target === null
   if (event.key === " " && !typingTarget(event.target) && (viewport.current?.contains(target) || (free && pointerInside.current))) {
    event.preventDefault()
    if (!spaceHeld.current) { spaceHeld.current = true; setSpace(true) }
   } else if (event.key === "Escape" && gesture.current && !viewport.current?.contains(target)) cancelNow.current()
  }
  const up = (event: KeyboardEvent) => { if (event.key === " ") { spaceHeld.current = false; setSpace(false) } }
  const blur = () => { spaceHeld.current = false; setSpace(false); cancelNow.current(); touches.current.clear(); gesture.current = null }
  const menu = (event: MouseEvent) => { if (Date.now() - menuAfterPan.current < 600) { event.preventDefault(); menuAfterPan.current = 0 } }
  window.addEventListener("keydown", down)
  window.addEventListener("keyup", up)
  window.addEventListener("blur", blur)
  window.addEventListener("contextmenu", menu, true)
  return () => { window.removeEventListener("keydown", down); window.removeEventListener("keyup", up); window.removeEventListener("blur", blur); window.removeEventListener("contextmenu", menu, true) }
 }, [])
 // Links are edited only through the live document, and only by editors.
 const setLink = (target: string) => { if (selected && design.doc && canEditSelected && currentEditableIds().has(selected.id)) setDesignNodeLink(design.doc, selected.id, target || null) }
 const play = () => {
  const start = selected ? designScreenOf(design.nodes, selected) : undefined
  const first = start ?? (framed ? focused?.id : undefined) ?? frames[0]?.id
  if (prototype && first) { setPlaying(first); if (framed) enterFullscreen() }
 }
 const stopPlaying = () => { setPlaying(null); if (framed) exitFullscreen() }
 // The frame's chip and presence come from here: the socket, the checkpoint and the server's can_edit.
 const reportData = design.query.data
 const checkpointVersion = design.save.isSuccess && !design.dirty ? (design.save.data as { version?: number } | undefined)?.version : undefined
 const historyFullNow = Boolean(reportData?.can_edit && [design.save.error, design.restore.error].some(error => (error as (Error & { status?: number }) | null)?.status === 413))
 const frameState: DesignEditorFrameState = {
  ready: Boolean(reportData) && !design.query.isError && !design.readRevoked, ...(version ? { version } : {}), connection: design.connection,
  canEdit: reportData?.can_edit === true, editable: design.editable, unsynced: Boolean(design.unsynced), checkpointPending: design.save.isPending,
  ...(checkpointVersion ? { checkpointVersion } : {}), historyFull: historyFullNow,
  peers: design.peers.map(peer => ({ clientId: peer.clientId, name: peer.name, color: peer.color })),
 }
 const frameKey = JSON.stringify(frameState)
 const report = useRef(onFrameState); report.current = onFrameState
 // eslint-disable-next-line react-hooks/exhaustive-deps -- the key is the whole state
 useEffect(() => { report.current?.(frameState) }, [frameKey])
 if (!artifactsDesign()) return <p className="p-6" role="status">Design is not enabled.</p>
 if (design.readRevoked) return <p className="p-6" role="alert">{design.everRead
  ? "Page read access was removed. The local draft has been deleted where this browser could reach it."
  : "This design is not available to you. Its owner may not have shared a version with you yet, or your access changed."}</p>
 if (design.query.isError) return <p className="p-6 text-destructive" role="alert">{design.query.error.message}</p>
 if (!design.query.data) return <p className="p-6" role="status">Opening Design…</p>
 // The drawn canvas starts at 0,0 unless layers sit at negative coordinates.
 const placesNow = design.nodes.map(node => ({ ...position(node), width: Number(node.width) || 0, height: Number(node.height) || 0 }))
 const left = Math.min(0, ...placesNow.map(place => place.x < 0 ? place.x - 100 : 0))
 const top = Math.min(0, ...placesNow.map(place => place.y < 0 ? place.y - 100 : 0))
 const width = Math.max(1200, ...placesNow.map(place => place.x + place.width + 100)) - left
 const height = Math.max(840, ...placesNow.map(place => place.y + place.height + 100)) - top
 const origin = toScreen(view, { x: left, y: top })
 const data = design.query.data
 const failure = design.save.error ?? design.restore.error ?? design.share.error ?? design.comment.error ?? design.connectAgent.error
 const viewedVersion = version ?? data.latest_version ?? data.version
 const historyFull = Boolean(data.can_edit && [design.save.error, design.restore.error].some(error => (error as (Error & { status?: number }) | null)?.status === 413))
 const downloadDraft = () => {
  if (!design.doc || version) return
  setDownloadError("")
  let url: string | undefined
  try {
   const draftFile = { format: "crewship-design-draft-v1", workspace_id: workspaceId, page_id: data.page_id, base_version: viewedVersion, nodes: design.nodes, yjs_state: encodedDesignState(design.doc), feedback_draft: { text: draft, node_id: selectedId } }
   url = URL.createObjectURL(new Blob([JSON.stringify(draftFile)], { type: "application/json" }))
   const anchor = document.createElement("a")
   anchor.href = url
   anchor.download = `design-${data.page_id}-draft.json`
   document.body.appendChild(anchor)
   try { anchor.click() } finally { anchor.remove() }
  } catch {
   setDownloadError("The draft download could not start. Your work remains in this editor.")
  } finally {
   if (url) {
    const revoke = URL.revokeObjectURL.bind(URL)
    const downloadedURL = url
    setTimeout(() => revoke(downloadedURL), 0)
   }
  }
 }
 const continueDesign=async()=>{
  if(!human||!commentPageId||!design.doc||!data.can_edit||version||!continueName.trim()||continuing)return
  const epoch=++continueEpoch.current
  setContinuing(true);setContinueError("")
  try{
   const response=await apiFetch(`/api/v1/pages/design?workspace_id=${encodeURIComponent(workspaceId)}`,{method:"POST",skipRefresh:true,headers:{"Content-Type":"application/json"},body:JSON.stringify({name:continueName.trim(),slug:continueSlug,expected_human:human,source_page_id:commentPageId,initial_state:encodedDesignState(design.doc,4*1024*1024)})})
   if(response.status!==201)throw new Error("copy refused")
   const created:unknown=await response.json()
   if(!created||typeof created!=="object"||!("page_id" in created)||typeof created.page_id!=="string"||!/^[A-Za-z0-9_-]{1,128}$/.test(created.page_id)||!("version" in created)||created.version!==1)throw new Error("copy not confirmed")
   if(continueEpoch.current!==epoch)return
   setContinued({scope:continueScope,page:created.page_id});setContinueOpen(false)
  }catch{if(continueEpoch.current===epoch)setContinueError("The new Design could not be confirmed. Your canvas and unsent feedback remain here.")}
  finally{if(continueEpoch.current===epoch)setContinuing(false)}
 }
 const sendComment = async () => {
  if (!selected || !draft.trim() || !commentKey || draftState.key !== commentKey) return
  const submitted = { key: commentKey, text: draft, revision: artifactDraftRevision(commentKey) }
  setCommentError("")
  try {
   const checkpoint = design.editable ? await design.save.mutateAsync("Checkpoint before feedback") : undefined
   const posted = await design.comment.mutateAsync({ version: checkpoint?.version ?? viewedVersion, path: "design.json", selector: selected.id, quote: String(selected.text ?? selected.name ?? "").length <= 2000 ? String(selected.text ?? selected.name ?? "") : "", text: draft.trim(), send_to_agent: Boolean(data?.origin && data?.can_edit) })
   if (currentDraft.current.key === submitted.key && currentDraft.current.text === submitted.text && artifactDraftRevision(submitted.key) === submitted.revision) {
    void clearArtifactDraft(submitted.key, submitted.text, submitted.revision).catch(() => setCommentError("Comment saved; local draft cleanup failed."))
    setDraftState({ key: submitted.key, text: "" })
   }
   if (data?.origin && data?.can_edit) {
    setDelivery(previous => ({ ...previous, [posted.id]: "Sending to agent…" }))
    const accepted = await agent.dispatch(posted)
    setDelivery(previous => ({ ...previous, [posted.id]: accepted ? "Sent to agent" : agent.refusal?.(posted.id) === "busy" ? "Comment saved. The agent is busy; retry when it finishes." : "Comment saved. Retry when the source chat is connected." }))
   }
  } catch (error) { setCommentError(error instanceof Error ? error.message : "Comment could not be saved.") }
 }
 const statusText = version ? `Version ${version}` : historyFull ? "History full · changes kept in this browser" : design.unsynced && data.can_edit === false ? "Read only · unsent changes stay in this browser and come back when you can edit again" : design.connection === "connected" ? design.unsynced ? "Live · sending changes…" : !design.editable ? "Connected · read only" : design.dirty ? "Live · changes saved as you work" : "Live · up to date" : design.connection === "denied" ? "Access changed · reopen to continue" : design.connection === "rejected" ? "The server refused changes made in this browser (too large or invalid) · they will not sync" : design.connection === "connecting" ? "Connecting…" : "Offline · changes kept in this browser"
 const sendsToAgent = Boolean(data.origin && data.can_edit)
 const banners = <>
  {historyFull && <div role="alert" className="space-y-2 border-b px-4 py-3 text-sm"><p><b>History capacity is full.</b> Another version could not be saved. Existing versions are retained and your pending work stays in this browser.</p><p>Download your local canvas and unsent feedback as a draft file to keep a copy. History is not deleted automatically.</p>{version && <Button variant="outline" onClick={() => setVersion(undefined)}>Return to live canvas</Button>}<Button variant="outline" disabled={!design.doc || Boolean(version)} onClick={downloadDraft}><Download className="size-4" />Download draft</Button><Button variant="outline" disabled={!design.doc||Boolean(version)||!human||continuing} onClick={()=>{setContinueName(`${slug} (continued)`);setContinueSlug(`design-${crypto.randomUUID().replaceAll("-","").slice(0,24)}`);setContinueError("");setContinueOpen(true)}}>Continue in a new Design</Button>{downloadError && <p className="text-destructive">{downloadError}</p>}</div>}
  {continued?.scope===continueScope&&<div role="status" className="border-b px-4 py-3 text-sm"><p>A new Design contains the copied canvas. This original canvas, its history and your unsent feedback remain here.</p><a className="underline" href={`/artifacts/${encodeURIComponent(continued.page)}?workspace_id=${encodeURIComponent(workspaceId)}`} target="_blank" rel="noopener noreferrer">Open new Design</a></div>}
  {failure && !historyFull && <p role="alert" className="border-b px-4 py-2 text-sm text-destructive">{failure.message}</p>}
  {design.connection==="quota"&&!historyFull&&<p role="alert" className="border-b px-4 py-2 text-sm">Storage capacity is full. Your changes are kept in this browser and nothing was deleted. Save a version to open the recovery choices.</p>}
  {design.conflicts.map((conflict, index) => <DesignConflictNotice key={`${conflict.id}-${index}`} conflict={conflict} canRestore={canChange && Boolean(design.doc)} onRestore={() => { if (design.doc) restoreDesignConflict(design.doc, conflict); design.dismissConflict(conflict) }} onDismiss={() => design.dismissConflict(conflict)} />)}
  {addressPending && <p role="status" className="border-b px-4 py-2 text-sm">The requested version will open when your local changes are saved. Keep this canvas open while they are pending.</p>}
  {design.draftError && <p role="alert" className="border-b px-4 py-2 text-sm">Browser draft storage is unavailable or full. Keep this window open until connected and save a version.</p>}
 </>
 const hasBanners = addressPending || historyFull || continued?.scope === continueScope || Boolean(failure) || design.connection === "quota" || design.conflicts.length > 0 || design.draftError
 const hideLock = artifactsDesignHideLock()
 const toggleFlag = (node: DesignNode, flag: "hidden" | "locked") => {
  if (!design.doc || !canChange) return
  upsertDesignNode(design.doc, { id: node.id, [flag]: !node[flag] })
  if (flag === "hidden" && !node.hidden) setSelectedIds(chosen.filter(id => id !== node.id))
 }
 const layerList = design.nodes.map(node => <div key={node.id} role="group" aria-label={`Layer ${node.name || node.text || node.type}`} className="flex items-center">
  <button aria-pressed={chosen.includes(node.id)} onClick={event => { if (event.shiftKey || event.ctrlKey || event.metaKey) { setSelectedIds(toggleSelection(chosen, node.id)); return } setSelectedId(node.id); if (node.type === "frame") setFocus(frames.indexOf(node)) }} className={`flex min-h-9 min-w-0 flex-1 items-center gap-2 px-3 text-left text-xs coarse:min-h-[44px] ${chosen.includes(node.id) ? "bg-accent text-accent-foreground" : "hover:bg-muted"} ${node.parentId ? "pl-7" : "font-medium"}`}>
  {node.type === "frame" ? <Frame className="size-3.5 shrink-0" /> : node.type === "text" ? <Type className="size-3.5 shrink-0" /> : <Square className="size-3.5 shrink-0" />}<span className={cn("truncate", hiddenIds.has(node.id) && "opacity-50")}>{node.name || node.text || node.type}</span>
 </button>
  {hideLock && canChange ? <>
   <button type="button" aria-label={node.hidden ? "Show layer" : "Hide layer"} aria-pressed={Boolean(node.hidden)} title={node.hidden ? "Show" : "Hide"} onClick={() => toggleFlag(node, "hidden")} className="flex size-8 shrink-0 items-center justify-center rounded text-muted-foreground hover:bg-muted coarse:size-[44px]">{node.hidden ? <EyeOff className="size-3.5" /> : <Eye className="size-3.5" />}</button>
   <button type="button" aria-label={node.locked ? "Unlock layer" : "Lock layer"} aria-pressed={Boolean(node.locked)} title={node.locked ? "Unlock" : "Lock"} onClick={() => toggleFlag(node, "locked")} className="flex size-8 shrink-0 items-center justify-center rounded text-muted-foreground hover:bg-muted coarse:size-[44px]">{node.locked ? <Lock className="size-3.5" /> : <LockOpen className="size-3.5" />}</button>
  </> : <>
   {node.hidden && <EyeOff aria-label="Hidden" className="mx-1 size-3.5 shrink-0 text-muted-foreground" />}
   {node.locked && <Lock aria-label="Locked" className="mx-1 size-3.5 shrink-0 text-muted-foreground" />}
  </>}
 </div>)
 const canvasView = <svg ref={canvas} role="img" aria-label="Design canvas" width={width * scale} height={height * scale} viewBox={`${left} ${top} ${width} ${height}`} className="absolute left-0 top-0 max-w-none" style={{ transform: `translate(${origin.x}px, ${origin.y}px)` }}>
  <defs><pattern id="design-dots" width="20" height="20" patternUnits="userSpaceOnUse"><circle cx="1" cy="1" r="1" fill="currentColor" className="text-border" /></pattern></defs>
  <rect x={left} y={top} width={width} height={height} fill="url(#design-dots)" />
  {[...frames, ...design.nodes.filter(node => node.type !== "frame")].filter(node => !hiddenIds.has(node.id)).map(node => {
   const { x, y } = position(node)
   const locked = lockedIds.has(node.id)
   const w = Math.max(1, Number(node.width) || 1), h = Math.max(1, Number(node.height) || 1)
   return <g key={node.id} data-design-node={node.id} data-node-type={node.type} data-node-name={node.name} transform={`translate(${x},${y})`} {...(locked ? { "data-locked": "", pointerEvents: "none" } : {})} style={{ cursor: space || panning || locked ? undefined : canChange ? "move" : "pointer" }}>
    {node.type === "frame" && <text x="0" y="-10" fontSize={12 / Math.min(1, scale)} fill="currentColor" className="text-muted-foreground">{node.name || "Screen"}</text>}
    <DesignNodeShape node={node} width={w} height={h} />
    {chosen.includes(node.id) && <rect width={w} height={h} fill="none" stroke="#2563eb" strokeWidth={2 / scale} pointerEvents="none" />}
    {selectedId === node.id && canChange && !locked && RESIZE_HANDLES.map(handle => {
     const size = 9 / scale
     const hx = handle.includes("w") ? 0 : handle.includes("e") ? w : w / 2, hy = handle.includes("n") ? 0 : handle.includes("s") ? h : h / 2
     return <rect key={handle} data-design-handle={handle} aria-hidden x={hx - size / 2} y={hy - size / 2} width={size} height={size} fill="white" stroke="#3b82f6" strokeWidth={1.5 / scale} style={{ cursor: `${handle}-resize` }} />
    })}
   </g>
  })}
  {movable.length > 1 && canChange && !version && (() => {
   const group = groupBox(design.nodes, movable)
   if (!group) return null
   return <g data-design-group={movable.length} transform={`translate(${group.x},${group.y})`}>
    <rect width={group.width} height={group.height} fill="none" stroke="#2563eb" strokeWidth={1 / scale} strokeDasharray={`${4 / scale} ${3 / scale}`} pointerEvents="none" />
    {RESIZE_HANDLES.map(handle => {
     const size = 9 / scale
     const hx = handle.includes("w") ? 0 : handle.includes("e") ? group.width : group.width / 2, hy = handle.includes("n") ? 0 : handle.includes("s") ? group.height : group.height / 2
     return <rect key={handle} data-design-handle={handle} aria-hidden x={hx - size / 2} y={hy - size / 2} width={size} height={size} fill="white" stroke="#3b82f6" strokeWidth={1.5 / scale} style={{ cursor: `${handle}-resize` }} />
    })}
   </g>
  })()}
  {marquee && <rect data-design-marquee="" x={Math.min(marquee.a.x, marquee.b.x)} y={Math.min(marquee.a.y, marquee.b.y)} width={Math.abs(marquee.b.x - marquee.a.x)} height={Math.abs(marquee.b.y - marquee.a.y)} fill="#3b82f6" fillOpacity={0.08} stroke="#3b82f6" strokeWidth={1 / scale} pointerEvents="none" />}
  {!version && design.peers.flatMap(peer => (peer.selection ?? []).map((id, index) => ({ peer, id, index }))).map(({ peer, id, index }) => {
   const node = design.nodes.find(item => item.id === id)
   if (!node || hiddenIds.has(node.id)) return null
   const { x, y } = position(node)
   const w = Math.max(1, Number(node.width) || 1), h = Math.max(1, Number(node.height) || 1)
   const label = peer.name.slice(0, 22)
   return <g key={`selection-${peer.clientId}-${node.id}`} data-remote-selection={node.id} data-remote-peer={peer.clientId} transform={`translate(${x},${y})`} pointerEvents="none" aria-label={`${peer.name} selected ${node.name || node.type}`}>
    <rect width={w} height={h} fill="none" stroke={peer.color} strokeWidth={2 / scale} strokeDasharray={`${6 / scale} ${4 / scale}`} />
    {index === 0 && <g transform={`translate(0,${-22 / scale - 2 / scale})${scale === 1 ? "" : ` scale(${1 / scale})`}`}><rect width={Math.min(160, label.length * 7 + 12)} height="18" rx="3" fill={readablePeerFill(peer.color)} /><text x="6" y="13" fontSize="11" fill="white">{label}</text></g>}
   </g>
  })}
  {design.peers.filter(peer => peer.cursor).map(peer => <g key={peer.clientId} transform={`translate(${peer.cursor!.x},${peer.cursor!.y})${scale === 1 ? "" : ` scale(${1 / scale})`}`} pointerEvents="none" aria-label={`${peer.name} cursor`}><path d="M0 0 L0 20 L5 15 L9 24 L13 22 L9 13 L17 13 Z" fill={peer.color} stroke="white" /><rect x="16" y="18" width={Math.min(160, peer.name.length * 7 + 16)} height="22" rx="4" fill={readablePeerFill(peer.color)} /><text x="24" y="33" fontSize="12" fill="white">{peer.name.slice(0, 22)}</text></g>)}
 </svg>
 // Right, middle or Space + left drag pans; Ctrl/Cmd + wheel or a pinch zooms around the pointer.
 // The browser menu is held back only here, so a right-drag ends without one.
 const viewportView = (className: string) => <div ref={attachViewport} role="group" tabIndex={0} aria-label="Design canvas viewport"
  aria-keyshortcuts="Escape + - Shift+1 Shift+0 ArrowLeft ArrowRight ArrowUp ArrowDown Delete Backspace [ ]"
  data-design-view={`${Math.round(scale * 10000) / 10000} ${Math.round(view.x * 100) / 100} ${Math.round(view.y * 100) / 100}`}
  className={cn("relative min-h-0 flex-1 touch-none select-none overflow-hidden outline-none focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-inset", panning ? "cursor-grabbing" : space && "cursor-grab", className)}
  onPointerDown={pressDown} onPointerMove={pressMove} onPointerUp={pressUp} onPointerCancel={pressCancel} onLostPointerCapture={event => { if (event.pointerType === "touch" && touches.current.has(event.pointerId)) pressUp(event) }}
  onPointerEnter={() => { pointerInside.current = true }} onPointerLeave={() => { pointerInside.current = false; design.setCursor(null) }}
  onMouseDown={event => { if (event.button === 1) event.preventDefault() }} onAuxClick={event => event.preventDefault()}
  onContextMenu={event => event.preventDefault()} onKeyDown={viewKey}>{canvasView}</div>
 const historyPanel = <>
  {!framed && <h2 className="mb-3 text-sm font-semibold">History</h2>}
  {framed && design.editable && <Button className="mb-3 w-full" variant="outline" disabled={design.save.isPending || design.connection !== "connected"} onClick={() => design.save.mutate("Saved from Design editor")}><Save className="size-4" /> Save version</Button>}
  {Boolean(design.versions.data?.shared_version) && <a className="mb-3 inline-block text-xs underline" target="_blank" rel="noopener noreferrer" href={`/artifacts?design=1&item=${encodeURIComponent(slug)}&version=${design.versions.data!.shared_version}&workspace_id=${encodeURIComponent(workspaceId)}`}>Open shared version {design.versions.data!.shared_version}</a>}
  <Button className="mb-3 w-full" variant={version ? "outline" : "secondary"} onClick={() => setVersion(undefined)}>{data.can_edit ? "Live canvas" : "Current view"}</Button>
  {design.versions.isError && <p role="alert" className="text-sm text-destructive">{design.versions.error.message}</p>}
  {design.versions.data?.versions.map(item => <div key={item.version} className="mb-3 rounded border p-3 text-xs"><button className="w-full text-left" onClick={() => setVersion(item.version)}><span className="font-medium">Version {item.version}</span>{item.version === design.versions.data?.shared_version && <span className="ml-2 text-muted-foreground">Shared</span>}<p className="mt-1 text-muted-foreground">{item.message || "Design checkpoint"}</p>{item.created_at && <p className="mt-1 text-muted-foreground">{new Date(item.created_at).toLocaleString()}</p>}</button>{data.can_edit && <div className="mt-2 flex gap-2"><Button size="sm" variant="outline" disabled={design.restore.isPending} onClick={() => design.restore.mutate(item.version, { onSuccess: () => setVersion(undefined) })}>Restore</Button>{data.can_share === true && <Button size="sm" variant="ghost" disabled={design.share.isPending} onClick={() => design.share.mutate(item.version)}>Share</Button>}</div>}</div>)}
 </>
 const propertiesPanel = <>
  {!framed && <h2 className="mb-4 text-sm font-semibold">Properties</h2>}
  {reviewOnly && <p className="mb-3 flex items-start gap-1.5 text-xs text-muted-foreground"><Info aria-hidden className="mt-px size-3.5 shrink-0" />Review only on a phone. Edit on a larger screen.</p>}
  {selected ? <fieldset disabled={!canEditSelected} className="min-w-0 space-y-3 border-0 p-0">
   <Field label="Name"><Input aria-label="Layer name" value={selected.name ?? ""} disabled={!canEditSelected} onChange={event => patch({ name: event.target.value })} /></Field>
   <div className="grid grid-cols-2 gap-2">{(["x", "y", "width", "height"] as const).map(field => <Field key={field} label={field === "width" ? "W" : field === "height" ? "H" : field.toUpperCase()}><Input type="number" aria-label={field} value={selected[field] ?? 0} disabled={!canEditSelected} min={field === "width" || field === "height" ? 1 : undefined} onChange={event => { const value = Number(event.target.value); if (Number.isFinite(value) && Math.abs(value) <= 1000000 && (!(field === "width" || field === "height") || value >= 1)) patch({ [field]: value }) }} /></Field>)}</div>
   <Field label={selected.type === "text" ? "Text color" : "Fill"}><div className="flex gap-2"><input aria-label="Fill color" type="color" className="h-9 w-10 rounded border bg-background p-1 coarse:h-[44px]" value={/^#[0-9a-f]{6}$/i.test(selected.fill) ? selected.fill : "#ffffff"} disabled={!canEditSelected} onChange={event => patch({ fill: event.target.value })} /><Input aria-label="Fill value" value={selected.fill ?? ""} disabled={!canEditSelected} onChange={event => patch({ fill: event.target.value })} /></div></Field>
   {selected.type === "text" && <Field label="Text"><textarea aria-label="Text content" maxLength={100000} className="min-h-24 w-full rounded border bg-background p-2 text-sm" value={selected.text ?? ""} disabled={!canEditSelected} onChange={event => patch({ text: event.target.value })} /></Field>}
   {selected.type === "text" && artifactsDesignTextStyle() && <DesignTextStyleFields node={selected} disabled={!canEditSelected} onChange={patch} />}
   {selected.type === "image" && <Field label="Image URL"><Input aria-label="Image URL" placeholder="https://…" value={selected.src ?? ""} disabled={!canEditSelected} onChange={event => { setImageURL(event.target.value); patch({ src: event.target.value }) }} /><p className="mt-1 text-xs text-muted-foreground">HTTPS image or embedded PNG, JPEG, WebP, GIF.</p></Field>}
   {selected.type !== "frame" && <Field label="Screen"><select aria-label="Parent screen" className="h-9 w-full rounded border bg-background px-2 text-sm coarse:h-[44px]" value={selected.parentId ?? ""} disabled={!canEditSelected} onChange={event => patch({ parentId: event.target.value })}><option value="">On canvas</option>{frames.map(frame => <option key={frame.id} value={frame.id}>{frame.name || "Screen"}</option>)}</select></Field>}
   {prototype && selected.type !== "frame" && (() => {
    const link = designLinkTarget(design.nodes, selected)
    return <Field label="Link to screen"><select aria-label="Link to screen" className="h-9 w-full rounded border bg-background px-2 text-sm coarse:h-[44px]" value={link.kind === "none" ? "" : link.kind === "screen" ? link.screen.id : link.target} disabled={!canEditSelected} onChange={event => setLink(event.target.value)}><option value="">No link</option>{frames.map(frame => <option key={frame.id} value={frame.id}>{frame.name || "Screen"}</option>)}{link.kind === "missing" && <option value={link.target} disabled>Deleted screen</option>}</select>{link.kind === "missing" && <p className="mt-1 text-xs text-warn">The linked screen was deleted. In Play this layer does nothing{canChange ? "; choose another screen or No link." : "."}</p>}{link.kind !== "missing" && <p className="mt-1 text-xs text-muted-foreground">In Play, clicking this layer opens the screen.</p>}</Field>
   })()}
   <Field label="Order"><div className="grid grid-cols-4 gap-1" role="group" aria-label="Layer order">
    <Button variant="outline" size="sm" aria-label="Send to back" title="Send to back (Ctrl+[)" disabled={!canEditSelected} onClick={() => order("back")}><ArrowDownToLine className="size-4" /></Button>
    <Button variant="outline" size="sm" aria-label="Send backward" title="Send backward ([)" disabled={!canEditSelected} onClick={() => order("backward")}><ChevronDown className="size-4" /></Button>
    <Button variant="outline" size="sm" aria-label="Bring forward" title="Bring forward (])" disabled={!canEditSelected} onClick={() => order("forward")}><ChevronDown className="size-4 rotate-180" /></Button>
    <Button variant="outline" size="sm" aria-label="Bring to front" title="Bring to front (Ctrl+])" disabled={!canEditSelected} onClick={() => order("front")}><ArrowUpToLine className="size-4" /></Button>
   </div></Field>
   {canChange && <Button variant="ghost" className="w-full text-destructive" disabled={lockedIds.has(selected.id)} title={lockedIds.has(selected.id) ? "Unlock the layer to delete it" : undefined} onClick={() => { if (design.doc) removeDesignNode(design.doc, selected.id); setSelectedId(null) }}><Trash2 className="size-4" />Delete {selected.type === "frame" ? "screen and layers" : "layer"}</Button>}
  </fieldset> : many ? <div className="space-y-3">
   <p className="text-sm">{chosen.length} layers selected</p>
   <p className="text-xs text-muted-foreground">Drag one of them, its handles or the arrow keys to move or resize them together. Escape clears the selection.</p>
   {canChange && (() => {
    // The same as Delete on the keyboard: the selected layers go, screens stay.
    const layers = movable.filter(id => design.nodes.find(node => node.id === id)?.type !== "frame")
    return layers.length > 0 && <Button variant="ghost" className="w-full text-destructive" onClick={() => { const doc = design.doc; if (doc) doc.transact(() => { for (const id of layers) removeDesignNode(doc, id) }, "editor"); setSelectedIds(chosen.filter(id => !layers.includes(id))) }}><Trash2 className="size-4" />Delete {layers.length} {layers.length === 1 ? "layer" : "layers"}</Button>
   })()}
  </div> : <p className="text-sm text-muted-foreground">Select a screen or layer to see its properties.{canChange && " Add your first screen using the toolbar."}</p>}
 </>
 const composer = (label: string) => <div className="mb-4 space-y-2">
  <p className="text-xs text-muted-foreground">{selected ? `On ${selected.name || selected.type}` : "Select a layer on the canvas to leave precise feedback."}</p>
  <textarea disabled={!commentKey || draftState.key !== commentKey} aria-label={label} className="min-h-20 w-full rounded border bg-background p-2 text-sm" placeholder="What should change?" value={draft} onChange={event => changeDraft(event.target.value)} />
  <Button size="sm" disabled={!selected || !draft.trim() || design.comment.isPending || design.save.isPending} onClick={() => { void sendComment() }}>{sendsToAgent ? "Send to agent" : "Save comment"}</Button>
  {commentError && <p role="alert" className="text-xs text-destructive">{commentError}</p>}
 </div>
 const mayComment = data.can_comment !== false && Boolean(data.can_comment || data.can_edit)
 const commentsPanel = <>
  {!framed && <h2 className="mb-3 text-sm font-semibold">Comments</h2>}
  {design.comments.isError && <p role="alert" className="mb-3 text-xs text-destructive">{design.comments.error.message}</p>}
  {data.can_edit && !data.origin && <div className="mb-4 space-y-2 rounded border p-3">
   <p className="text-xs text-muted-foreground">Connect a private agent chat to send feedback directly to your agent.</p>
   <Input aria-label="Agent chat ID" placeholder="Chat ID" value={agentChat} onChange={event => setAgentChat(event.target.value)} />
   <Button size="sm" variant="outline" disabled={!agentChat.trim() || design.connectAgent.isPending} onClick={() => design.connectAgent.mutate(agentChat.trim())}>Connect agent chat</Button>
  </div>}
  {data.origin && <a className="mb-3 inline-block text-xs underline" href={`/chat/${encodeURIComponent(data.origin.chat_id)}?workspace_id=${encodeURIComponent(workspaceId)}`}>Open agent chat</a>}
  {mayComment && composer("Comment for agent")}
  {(design.comments.data?.comments ?? []).map(comment => <article key={comment.id} className="mb-3 rounded border p-3 text-xs">
   <button className="text-left text-muted-foreground" onClick={() => setSelectedId(comment.selector)}>v{comment.version} · {comment.quote || comment.selector}</button>{comment.selector && !version && !design.nodes.some(node => node.id === comment.selector) && <p className="mt-1 text-warn">This layer is no longer on the canvas. The comment is kept; open v{comment.version} in History to see it.</p>}<p className="mt-2 whitespace-pre-wrap">{comment.text}</p>
   {comment.task_id && <p className="mt-2 text-muted-foreground">{comment.replies?.length ? `Done · v${comment.replies.at(-1)?.version}` : delivery[comment.id] ?? "Waiting for agent"}</p>}
   {comment.task_id && !comment.replies?.length && data.origin && data.can_edit && <Button size="sm" variant="ghost" disabled={!agent.ready} onClick={async () => { const accepted = await agent.dispatch(comment); setDelivery(previous => ({ ...previous, [comment.id]: accepted ? "Sent to agent" : agent.refusal?.(comment.id) === "busy" ? "Comment saved. The agent is busy; retry when it finishes." : "Comment saved. Agent delivery could not be confirmed." })) }}>Retry delivery</Button>}
   {comment.replies?.map((reply, index) => <p key={index} className="mt-2 border-t pt-2">v{reply.version}: {reply.text}</p>)}
  </article>)}
 </>
 const overlays = <>
  <CreateSurface open={continueOpen} onOpenChange={setContinueOpen} size="sm" onSubmit={()=>{if(!continuing&&continueName.trim())void continueDesign()}}><CreateSurfaceHeader title="Continue in a new Design" description="Create a copy of this canvas with fresh history. The original canvas and unsent feedback stay in this tab." onClose={()=>setContinueOpen(false)} /><CreateSurfaceBody><CreateSurfaceField label="New Design name" htmlFor="continued-design-name"><Input id="continued-design-name" value={continueName} maxLength={200} onChange={event=>setContinueName(event.target.value)} /></CreateSurfaceField>{continueError&&<p role="alert" className="text-destructive">{continueError}</p>}</CreateSurfaceBody><CreateSurfaceFooter onCancel={()=>setContinueOpen(false)} guardCancel={false} primaryLabel={continuing?"Creating…":"Create new Design"} onPrimary={()=>{void continueDesign()}} primaryDisabled={continuing||!continueName.trim()} /></CreateSurface>
  {prototype && playing && <DesignPrototypePlayer key={playing} nodes={design.nodes.filter(node => !hiddenIds.has(node.id))} startScreenId={playing} onClose={stopPlaying} />}
  {accessOpen && <DesignAccessDialog workspaceId={workspaceId} slug={slug} onClose={() => setAccessOpen(false)} />}
 </>

 if (framed) {
  const zoomLabel = placed.fit ? `Fit · ${Math.round(scale * 100)} %` : `${Math.round(scale * 100)} %`
  // Next and previous bring the artboard into view; a phone keeps fitting its width.
  const goTo = (index: number) => {
   const next = Math.max(0, Math.min(frames.length - 1, index))
   setFocus(next)
   const target = frames[next] && box([frames[next]])
   if (target && !(phone && placed.fit)) setView(focusView(view, target, fitOptions))
  }
  return <section aria-label="Design editor" className="relative flex min-h-0 flex-1 flex-col bg-muted/40 text-foreground" data-design-connection={design.connection} data-design-review={reviewOnly ? "phone" : undefined}>
   {hasBanners && <div className={cn("relative z-[11] shrink-0 bg-background", phone ? "mt-14" : "mt-16")}>{banners}</div>}
   {viewportView("")}
   {/* view: what am I looking at. Canvas | Library appears only for a file that publishes a library; none does yet. */}
   <ArtifactSlot name="view" label="View">
    {frames.length > 0 ? <DropdownMenu>
     <DropdownMenuTrigger asChild>
      <button type="button" aria-label={`Artboard: ${focused?.name || "Screen"}`} className="inline-flex h-[30px] items-center gap-1.5 rounded-[var(--radius-row)] px-2 text-sm hover:bg-accent coarse:min-h-[44px]">
       <Layers aria-hidden className="size-3.5 text-muted-foreground" /><b className="max-w-40 truncate font-semibold">{focused?.name || "Screen"}</b>
       <span className="font-mono text-xs text-muted-foreground">{phone ? `${focusIndex + 1}/${frames.length}` : `${frames.length} ${frames.length === 1 ? "artboard" : "artboards"}`}</span>
       <ChevronDown aria-hidden className="size-3 text-muted-foreground" />
      </button>
     </DropdownMenuTrigger>
     <DropdownMenuContent align="start" className="w-60">
      <DropdownMenuLabel className="text-xs text-muted-foreground">{frames.length} {frames.length === 1 ? "artboard" : "artboards"}</DropdownMenuLabel>
      {frames.map((frame, index) => <DropdownMenuItem key={frame.id} onSelect={() => goTo(index)} aria-current={index === focusIndex ? "true" : undefined}>
       <Frame aria-hidden /><span className="min-w-0 flex-1 truncate">{frame.name || "Screen"}</span><span className="font-mono text-xs text-muted-foreground">{Math.round(Number(frame.width) || 0)} × {Math.round(Number(frame.height) || 0)}</span>
      </DropdownMenuItem>)}
     </DropdownMenuContent>
    </DropdownMenu> : <span className="px-2 text-sm text-muted-foreground">No artboards yet</span>}
    {!phone && <ArtifactTool icon={Layers} label="Layers" on={layersOpen} onClick={() => setLayersOpen(open => !open)} />}
   </ArtifactSlot>
   <ArtifactSlot name="output" label="Output">
    {prototype && frames.length > 0 && <ArtifactTool ref={playButton} icon={Play} label="Play prototype" text={phone ? undefined : "Play"} onClick={play} />}
    {!phone && <DesignExportButton workspaceId={workspaceId} pageId={data.page_id} version={viewedVersion} variant="tool" />}
   </ArtifactSlot>
   {canChange && <ArtifactSlot name="tools" label="Design tools">
    <ArtifactTool icon={MousePointer2} label="Select and move" on />
    <ArtifactToolSeparator />
    <ArtifactTool icon={Frame} label="Screen" onClick={() => add("frame")} />
    <ArtifactTool icon={Square} label="Rectangle" onClick={() => add("rect")} />
    <ArtifactTool icon={Type} label="Text" onClick={() => add("text")} />
    <ArtifactTool icon={ImagePlus} label="Image" onClick={() => add("image")} />
    <ArtifactToolSeparator />
    <ArtifactTool icon={Undo2} label="Undo" disabled={!design.canUndo} onClick={() => design.undo?.()} />
    <ArtifactTool icon={Redo2} label="Redo" disabled={!design.canRedo} onClick={() => design.redo?.()} />
   </ArtifactSlot>}
   {reviewOnly && <ArtifactSlot name="tools" label="Review">
    {mayComment && <ArtifactTool icon={MessageSquare} label="Comment" text="Comment" on={artifactFrame?.drawerTab === "comments"} onClick={() => artifactFrame?.openDrawer("comments")} />}
    <span role="note" className="inline-flex items-center gap-1.5 px-2 text-xs text-muted-foreground"><Info aria-hidden className="size-3.5 shrink-0" />Review only. Edit on a larger screen.</span>
   </ArtifactSlot>}
   <ArtifactSlot name="viewport" label="Zoom">
    <ArtifactTool icon={ZoomOut} label="Zoom out" onClick={() => step(-1)} />
    <button type="button" aria-label="Zoom level" onClick={() => setPlaced({ fit: true })} className="h-[30px] min-w-16 rounded-[var(--radius-row)] px-1.5 font-mono text-xs text-muted-foreground hover:bg-accent hover:text-foreground">{zoomLabel}</button>
    <ArtifactTool icon={ZoomIn} label="Zoom in" onClick={() => step(1)} />
    <ArtifactToolSeparator />
    <ArtifactTool icon={Scan} label="Fit" on={placed.fit} onClick={() => setPlaced({ fit: true })} />
   </ArtifactSlot>
   {frames.length > 0 && <ArtifactSlot name="nav" label="Artboards">
    <ArtifactTool icon={ChevronLeft} label="Previous artboard" disabled={focusIndex === 0} onClick={() => goTo(focusIndex - 1)} />
    <span className="max-w-44 truncate px-1 text-sm font-semibold">{focused?.name || "Screen"}</span>
    <span className="px-1 font-mono text-xs text-muted-foreground">{focusIndex + 1} / {frames.length}</span>
    <ArtifactTool icon={ChevronRight} label="Next artboard" disabled={focusIndex >= frames.length - 1} onClick={() => goTo(focusIndex + 1)} />
   </ArtifactSlot>}
   {layersOpen && !phone && <ArtifactSlot name="panel" label="Layers">
    <div className="flex items-center gap-2 border-b px-3 py-2 text-xs font-semibold text-muted-foreground">Layers <span className="ml-auto font-normal">{design.nodes.length}</span></div>
    <div className="flex min-h-0 flex-1 flex-col overflow-auto py-1">
     {frames.length === 0 && <p className="px-3 py-2 text-xs text-muted-foreground">Add a screen to start designing.</p>}
     {layerList}
    </div>
   </ArtifactSlot>}
   <ArtifactDrawerSection tab="properties" label="Properties" icon={SlidersHorizontal} order={0}><div className="p-3.5">{propertiesPanel}</div></ArtifactDrawerSection>
   <ArtifactDrawerSection tab="comments" label="Comments"><div className="p-3.5">{commentsPanel}</div></ArtifactDrawerSection>
   <ArtifactDrawerSection tab="history" label="History"><div className="p-3.5">{historyPanel}</div></ArtifactDrawerSection>
   {/* Propose: a comment on a layer goes to the agent's own chat; the agent answers with a new version. Only where that path exists. */}
   {sendsToAgent && mayComment && <ArtifactAgentMode mode="propose"><div className="space-y-2">
    <p className="text-xs text-muted-foreground">Select a layer and describe the change. The agent answers in its chat with a new version; History keeps every version.</p>
    {composer("Request for the agent")}
   </div></ArtifactAgentMode>}
   {overlays}
  </section>
 }

 return <section aria-label="Design editor" className="flex h-full min-h-[600px] flex-col bg-background text-foreground" data-design-connection={design.connection}>
  <header className="flex flex-wrap items-center gap-3 border-b px-4 py-3">
   <Frame className="size-5" aria-hidden="true" />
   <h1 className="mr-auto truncate font-semibold">{slug} <span className="ml-2 text-xs font-normal text-muted-foreground">Design</span></h1>
   <span role="status" className="text-xs text-muted-foreground">{statusText}</span>
   {!design.editable && <span className="rounded bg-muted px-2 py-1 text-xs">{version ? "History" : "Read only"}</span>}
   <div className="flex -space-x-1" aria-label="People on this canvas">{design.peers.map(peer => <span key={peer.clientId} title={peer.name} className="flex size-7 items-center justify-center rounded-full border-2 border-background text-xs font-medium text-white" style={{ backgroundColor: readablePeerFill(peer.color) }}>{peer.name.slice(0, 2).toUpperCase()}</span>)}</div>
   {prototype && frames.length > 0 && <Button ref={playButton} variant="outline" onClick={play}><Play className="size-4" />Play</Button>}
   <Button variant="outline" onClick={() => setAccessOpen(true)}>Access</Button>
   <Button variant="outline" onClick={() => setPanel(panel === "history" ? "properties" : "history")}>History{viewedVersion ? ` · v${viewedVersion}` : ""}</Button>
   <DesignExportButton workspaceId={workspaceId} pageId={data.page_id} version={viewedVersion} />
   {design.editable && <Button disabled={design.save.isPending || design.connection !== "connected"} onClick={() => design.save.mutate("Saved from Design editor")}><Save className="size-4" /> Save version</Button>}
  </header>
  {banners}
  <div className="flex min-h-0 flex-1 flex-col md:flex-row">
   <aside className="flex max-h-40 shrink-0 flex-col overflow-auto border-b md:max-h-none md:w-48 md:border-b-0 md:border-r" aria-label="Layers">
    <div className="flex items-center gap-2 px-3 py-3 text-xs font-semibold uppercase tracking-wide text-muted-foreground">Screens <span className="ml-auto">{frames.length}</span></div>
    {frames.length === 0 && <p className="px-3 pb-3 text-xs text-muted-foreground">Add a screen to start designing.</p>}
    {layerList}
   </aside>
   <div className="relative flex min-h-96 min-w-0 flex-1 flex-col">
    <div className="z-10 flex flex-wrap items-center gap-1 border-b bg-background px-3 py-2" aria-label="Design tools">
     <Button variant="ghost" size="sm" title="Select and move"><MousePointer2 className="size-4" /><span className="sr-only">Select and move</span></Button>
     {design.editable && <><Button variant="outline" size="sm" onClick={() => add("frame")}><Plus className="size-4" />Screen</Button><Button variant="ghost" size="sm" onClick={() => add("rect")}><Square className="size-4" />Rectangle</Button><Button variant="ghost" size="sm" onClick={() => add("text")}><Type className="size-4" />Text</Button><Button variant="ghost" size="sm" onClick={() => add("image")}><ImagePlus className="size-4" />Image</Button><Button variant="ghost" size="sm" aria-label="Undo" title="Undo" disabled={!design.canUndo} onClick={() => design.undo?.()}><Undo2 className="size-4" /></Button><Button variant="ghost" size="sm" aria-label="Redo" title="Redo" disabled={!design.canRedo} onClick={() => design.redo?.()}><Redo2 className="size-4" /></Button></>}
     <span className="ml-auto text-xs text-muted-foreground">{selected ? selected.name || selected.type : design.editable ? "Drag to move · right-drag or Space to pan · edit in Properties" : "View canvas"}</span>
     <span className="flex items-center gap-0.5" aria-label="Zoom">
      <Button variant="ghost" size="sm" aria-label="Zoom out" onClick={() => step(-1)}><ZoomOut className="size-4" /></Button>
      <span className="w-[52px] shrink-0 text-center font-mono text-xs text-muted-foreground" aria-label="Zoom level">{Math.round(scale * 100)} %</span>
      <Button variant="ghost" size="sm" aria-label="Zoom in" onClick={() => step(1)}><ZoomIn className="size-4" /></Button>
      <Button variant="ghost" size="sm" aria-label="Fit" aria-pressed={placed.fit} onClick={() => setPlaced({ fit: true })}><Scan className="size-4" /></Button>
     </span>
    </div>
    {viewportView("bg-muted/40")}
   </div>
   <aside className="max-h-80 w-full shrink-0 overflow-auto border-t p-4 md:max-h-none md:w-60 md:border-l md:border-t-0" aria-label={panel === "history" ? "Version history" : "Properties"}>
    {panel === "history" ? historyPanel : propertiesPanel}
    <div className="mt-6 border-t pt-4" aria-label="Design comments">{commentsPanel}</div>
   </aside>
  </div>
  {overlays}
 </section>
}

const WEIGHT_NAMES: Record<number, string> = { 100: "Thin", 200: "Extra light", 300: "Light", 400: "Regular", 500: "Medium", 600: "Semibold", 700: "Bold", 800: "Extra bold", 900: "Black" }
const selectClass = "h-9 w-full rounded border bg-background px-2 text-sm coarse:h-[44px]"

/** Text style of a text layer (#5161). Each control writes one field; an unset field shows its default. */
function DesignTextStyleFields({ node, disabled, onChange }: { node: DesignNode; disabled: boolean; onChange: (values: Partial<DesignNode>) => void }) {
 const style = designTextStyle(node)
 const number = (value: string, range: { min: number; max: number }) => { const parsed = Number(value); return value.trim() !== "" && Number.isFinite(parsed) && parsed >= range.min && parsed <= range.max ? parsed : undefined }
 return <fieldset className="space-y-3" aria-label="Text style">
  <div className="grid grid-cols-2 gap-2">
   <Field label="Font"><select aria-label="Font family" className={selectClass} value={style.family} disabled={disabled} onChange={event => onChange({ fontFamily: event.target.value as DesignNode["fontFamily"] })}><option value="sans">Sans</option><option value="serif">Serif</option><option value="mono">Mono</option></select></Field>
   <Field label="Weight"><select aria-label="Font weight" className={selectClass} value={style.weight} disabled={disabled} onChange={event => onChange({ fontWeight: Number(event.target.value) })}>{DESIGN_FONT_WEIGHTS.map(weight => <option key={weight} value={weight}>{weight} · {WEIGHT_NAMES[weight]}</option>)}</select></Field>
   <Field label="Size"><Input type="number" aria-label="Font size" min={DESIGN_FONT_SIZE.min} max={DESIGN_FONT_SIZE.max} value={style.size} disabled={disabled} onChange={event => { const value = number(event.target.value, DESIGN_FONT_SIZE); if (value !== undefined) onChange({ fontSize: value }) }} /></Field>
   <Field label="Line height"><Input type="number" aria-label="Line height" step={0.1} min={DESIGN_LINE_HEIGHT.min} max={DESIGN_LINE_HEIGHT.max} value={style.lineHeight} disabled={disabled} onChange={event => { const value = number(event.target.value, DESIGN_LINE_HEIGHT); if (value !== undefined) onChange({ lineHeight: value }) }} /></Field>
  </div>
  <Field label="Align"><select aria-label="Text alignment" className={selectClass} value={style.align} disabled={disabled} onChange={event => onChange({ textAlign: event.target.value as DesignNode["textAlign"] })}><option value="left">Left</option><option value="center">Center</option><option value="right">Right</option></select></Field>
  <label className="flex items-center gap-2 text-xs text-muted-foreground coarse:min-h-[44px]"><input type="checkbox" aria-label="Wrap text to the box width" checked={style.wrap} disabled={disabled} onChange={event => onChange({ textWrap: event.target.checked ? "wrap" : "none" })} />Wrap to the box width</label>
  <label className="flex items-center gap-2 text-xs text-muted-foreground coarse:min-h-[44px]"><input type="checkbox" aria-label="Clip text to the box" checked={style.clip} disabled={disabled} onChange={event => onChange({ overflow: event.target.checked ? "clip" : "visible" })} />Clip to the box</label>
 </fieldset>
}

function Field({ label, children }: { label: string; children: React.ReactNode }) {
 return <label className="block text-xs text-muted-foreground"><span className="mb-1.5 block">{label}</span>{children}</label>
}

/** Two people changed one text from a stale canvas and only one text stayed on it. */
function DesignConflictNotice({ conflict, canRestore, onRestore, onDismiss }: { conflict: DesignConflict; canRestore: boolean; onRestore: () => void; onDismiss: () => void }) {
 const [copy, setCopy] = useState<"" | "copied" | "failed">("")
 const lost = conflict.kept === "mine" ? conflict.theirs : conflict.mine
 const copyText = () => { void navigator.clipboard.writeText(lost).then(() => setCopy("copied"), () => setCopy("failed")) }
 return <div role="alert" className="space-y-2 border-b px-4 py-3 text-sm">
  <p><b>Edit conflict: two people changed the text of “{conflict.name}”.</b></p>
  <p>{conflict.left === undefined ? "The layer was deleted meanwhile. Its text exists only here in this browser:" : conflict.kept === "mine" ? "Your text stayed on the canvas. The other person’s text was overwritten and stays in History only if it was saved in a version. Here it is:" : "The other person’s text stayed on the canvas. Your text was not saved and exists only here in this browser:"}</p>
  <blockquote className="max-h-32 overflow-auto whitespace-pre-wrap rounded border bg-muted p-2">{lost}</blockquote>
  <div className="flex flex-wrap items-center gap-2">
   <Button variant="outline" size="sm" disabled={!canRestore} onClick={onRestore}>Restore this text</Button>
   <Button variant="outline" size="sm" onClick={copyText}>Copy text</Button>
   <Button variant="ghost" size="sm" onClick={onDismiss}>Dismiss</Button>
   {copy && <span className="text-xs text-muted-foreground">{copy === "copied" ? "Text copied." : "Copy failed. Select the text by hand."}</span>}
  </div>
 </div>
}
