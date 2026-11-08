"use client"

import { useEffect, useLayoutEffect, useRef, useState, type PointerEvent } from "react"
import dynamic from "next/dynamic"
import { ChevronDown, ChevronLeft, ChevronRight, Download, Frame, ImagePlus, Info, Layers, MessageSquare, MousePointer2, Play, Plus, Save, Scan, SlidersHorizontal, Square, Trash2, Type, ZoomIn, ZoomOut } from "lucide-react"
import * as Y from "yjs"
import { useArtifactAgentDispatch } from "@/hooks/use-artifact-agent-dispatch"
import { useSessionSafe } from "@/hooks/use-auth"
import { artifactDraftKey, loadArtifactDraft, writeArtifactDraft, clearArtifactDraft, artifactDraftRevision } from "@/lib/artifact-drafts"
import { artifactsDesign, artifactsDesignPrototype } from "@/lib/feature-flags"
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
import { DESIGN_FONT_SIZE, DESIGN_FONT_WEIGHTS, DESIGN_LINE_HEIGHT, designTextStyle, designLinkTarget, designScreenOf, removeDesignNode, restoreDesignConflict, safeImageSource, setDesignNodeLink, upsertDesignNode, type DesignConflict, type DesignNode, type DesignNodeType } from "./design-document"
import { DesignNodeShape } from "./design-node-shape"
import { DesignPrototypePlayer } from "./design-prototype-player"
import { DesignExportButton } from "./design-export-button"

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
 const [selectedId, setSelectedId] = useState<string | null>(null)
 const [agentChat, setAgentChat] = useState("")
 const [imageURL, setImageURL] = useState("")
 const [panel, setPanel] = useState<"properties" | "history">("properties")
 const canvas = useRef<SVGSVGElement>(null)
 const prototype = artifactsDesignPrototype()
 const [playing, setPlaying] = useState<string | null>(null)
 const playButton = useRef<HTMLButtonElement>(null)
 const wasPlaying = useRef(false)
 useEffect(() => { if (wasPlaying.current && !playing) playButton.current?.focus(); wasPlaying.current = Boolean(playing) }, [playing])
 const drag = useRef<{ id: string; startX: number; startY: number; x: number; y: number } | null>(null)
 const selected = design.nodes.find(node => node.id === selectedId)
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
 const patch = (values: Partial<DesignNode>) => {
  if (selected && design.doc && canChange) upsertDesignNode(design.doc, { id: selected.id, ...values })
 }
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
 // Zoom lives only in the frame; the standalone editor draws at 100 %.
 const viewport = useRef<HTMLDivElement>(null)
 const [zoom, setZoom] = useState<"fit" | number>("fit")
 const [viewportSize, setViewportSize] = useState({ width: 0, height: 0 })
 const [focus, setFocus] = useState(0)
 const [layersOpen, setLayersOpen] = useState(false)
 const focusIndex = Math.min(focus, Math.max(0, frames.length - 1))
 const focused = frames[focusIndex]
 const loaded = Boolean(design.query.data)
 useLayoutEffect(() => {
  const element = viewport.current
  if (!framed || !element) return
  const measure = () => setViewportSize(previous => previous.width === element.clientWidth && previous.height === element.clientHeight ? previous : { width: element.clientWidth, height: element.clientHeight })
  measure()
  if (typeof ResizeObserver === "undefined") return
  const observer = new ResizeObserver(measure)
  observer.observe(element)
  return () => observer.disconnect()
 }, [framed, loaded])
 const box = (nodes: DesignNode[]) => {
  if (!nodes.length) return null
  const places = nodes.map(node => ({ ...position(node), width: Math.max(1, Number(node.width) || 1), height: Math.max(1, Number(node.height) || 1) }))
  const left = Math.min(...places.map(place => place.x)), top = Math.min(...places.map(place => place.y))
  return { x: left, y: top, width: Math.max(...places.map(place => place.x + place.width)) - left, height: Math.max(...places.map(place => place.y + place.height)) - top }
 }
 // Fit: the whole canvas on a wide screen; the focused artboard's width on a phone.
 const fitTarget = phone && focused ? box([focused]) : box(frames.length ? frames : design.nodes)
 const fitScale = !framed || !fitTarget || !viewportSize.width ? 1
  : Math.min(2, Math.max(0.05, phone ? (viewportSize.width - 24) / fitTarget.width : Math.min((viewportSize.width - FIT_PAD * 2) / fitTarget.width, (viewportSize.height - 56 - 64 - 24) / fitTarget.height)))
 const scale = !framed ? 1 : zoom === "fit" ? fitScale : zoom
 const step = (direction: 1 | -1) => setZoom(() => {
  const next = direction > 0 ? ZOOM_STEPS.find(value => value > scale + 0.001) : [...ZOOM_STEPS].reverse().find(value => value < scale - 0.001)
  return next ?? (direction > 0 ? ZOOM_STEPS.at(-1)! : ZOOM_STEPS[0])
 })
 // Bring the fitted area, or the focused artboard, into view.
 const scrollTarget = zoom === "fit" ? (phone && focused ? box([focused]) : fitTarget) : focused ? box([focused]) : null
 const scrollKey = scrollTarget ? `${Math.round(scrollTarget.x)}:${Math.round(scrollTarget.y)}:${scale}:${focusIndex}:${zoom}` : ""
 useEffect(() => {
  const element = viewport.current
  if (!framed || !element || !scrollTarget) return
  const pad = phone ? 12 : FIT_PAD
  // The canvas sits under the view slot's padding, so the target's top lands just below it.
  element.scrollTo?.({ left: Math.max(0, scrollTarget.x * scale - pad), top: Math.max(0, scrollTarget.y * scale - 24) })
  // eslint-disable-next-line react-hooks/exhaustive-deps -- the key holds every input that moves the view
 }, [scrollKey])
 const point = (event: PointerEvent<SVGSVGElement> | PointerEvent<SVGGElement>) => {
  const bounds = canvas.current?.getBoundingClientRect()
  return { x: (event.clientX - (bounds?.left ?? 0)) / scale, y: (event.clientY - (bounds?.top ?? 0)) / scale }
 }
 const beginDrag = (event: PointerEvent<SVGGElement>, node: DesignNode) => {
  event.stopPropagation()
  setSelectedId(node.id)
  setPanel("properties")
  if (framed && !phone && artifactFrame?.drawerTab === null) artifactFrame.openDrawer("properties")
  if (!canChange || !canvas.current) return
  const start = point(event)
  drag.current = { id: node.id, startX: start.x, startY: start.y, x: node.x, y: node.y }
  canvas.current.setPointerCapture(event.pointerId)
 }
 const move = (event: PointerEvent<SVGSVGElement>) => {
  const cursor = point(event)
  design.setCursor(cursor)
  if (!drag.current || !canChange || !design.doc) return
  upsertDesignNode(design.doc, { id: drag.current.id, x: Math.round(drag.current.x + cursor.x - drag.current.startX), y: Math.round(drag.current.y + cursor.y - drag.current.startY) })
 }
 const endDrag = () => { drag.current = null }
 // Links are edited only through the live document, and only by editors.
 const setLink = (target: string) => { if (selected && design.doc && canChange) setDesignNodeLink(design.doc, selected.id, target || null) }
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
 if (design.readRevoked) return <p className="p-6" role="alert">Page read access was removed. The local draft has been deleted where this browser could reach it.</p>
 if (design.query.isError) return <p className="p-6 text-destructive" role="alert">{design.query.error.message}</p>
 if (!design.query.data) return <p className="p-6" role="status">Opening Design…</p>
 const width = Math.max(1200, ...design.nodes.map(node => position(node).x + (Number(node.width) || 0) + 100))
 const height = Math.max(840, ...design.nodes.map(node => position(node).y + (Number(node.height) || 0) + 100))
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
  {design.draftError && <p role="alert" className="border-b px-4 py-2 text-sm">Browser draft storage is unavailable or full. Keep this window open until connected and save a version.</p>}
 </>
 const hasBanners = historyFull || continued?.scope === continueScope || Boolean(failure) || design.connection === "quota" || design.conflicts.length > 0 || design.draftError
 const layerList = design.nodes.map(node => <button key={node.id} onClick={() => { setSelectedId(node.id); if (node.type === "frame") setFocus(frames.indexOf(node)) }} className={`flex min-h-9 items-center gap-2 px-3 text-left text-xs coarse:min-h-[44px] ${selectedId === node.id ? "bg-accent text-accent-foreground" : "hover:bg-muted"} ${node.parentId ? "pl-7" : "font-medium"}`}>
  {node.type === "frame" ? <Frame className="size-3.5 shrink-0" /> : node.type === "text" ? <Type className="size-3.5 shrink-0" /> : <Square className="size-3.5 shrink-0" />}<span className="truncate">{node.name || node.text || node.type}</span>
 </button>)
 const canvasView = <svg ref={canvas} role="img" aria-label="Design canvas" width={width * scale} height={height * scale} viewBox={`0 0 ${width} ${height}`} className="touch-none" onPointerMove={move} onPointerUp={endDrag} onPointerCancel={endDrag} onPointerLeave={() => design.setCursor(null)} onPointerDown={() => setSelectedId(null)}>
  <defs><pattern id="design-dots" width="20" height="20" patternUnits="userSpaceOnUse"><circle cx="1" cy="1" r="1" fill="currentColor" className="text-border" /></pattern></defs>
  <rect width="100%" height="100%" fill="url(#design-dots)" />
  {[...frames, ...design.nodes.filter(node => node.type !== "frame")].map(node => {
   const { x, y } = position(node)
   const w = Math.max(1, Number(node.width) || 1), h = Math.max(1, Number(node.height) || 1)
   return <g key={node.id} data-design-node={node.id} data-node-type={node.type} data-node-name={node.name} transform={`translate(${x},${y})`} onPointerDown={event => beginDrag(event, node)} style={{ cursor: canChange ? "move" : "pointer" }}>
    {node.type === "frame" && <text x="0" y="-10" fontSize={12 / Math.min(1, scale)} fill="currentColor" className="text-muted-foreground">{node.name || "Screen"}</text>}
    <DesignNodeShape node={node} width={w} height={h} />
    {selectedId === node.id && <rect width={w} height={h} fill="none" stroke="#2563eb" strokeWidth={2 / scale} pointerEvents="none" />}
   </g>
  })}
  {design.peers.filter(peer => peer.cursor).map(peer => <g key={peer.clientId} transform={`translate(${peer.cursor!.x},${peer.cursor!.y})${scale === 1 ? "" : ` scale(${1 / scale})`}`} pointerEvents="none" aria-label={`${peer.name} cursor`}><path d="M0 0 L0 20 L5 15 L9 24 L13 22 L9 13 L17 13 Z" fill={peer.color} stroke="white" /><rect x="16" y="18" width={Math.min(160, peer.name.length * 7 + 16)} height="22" rx="4" fill={readablePeerFill(peer.color)} /><text x="24" y="33" fontSize="12" fill="white">{peer.name.slice(0, 22)}</text></g>)}
 </svg>
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
  {selected ? <div className="space-y-3">
   <Field label="Name"><Input aria-label="Layer name" value={selected.name ?? ""} disabled={!canChange} onChange={event => patch({ name: event.target.value })} /></Field>
   <div className="grid grid-cols-2 gap-2">{(["x", "y", "width", "height"] as const).map(field => <Field key={field} label={field === "width" ? "W" : field === "height" ? "H" : field.toUpperCase()}><Input type="number" aria-label={field} value={selected[field] ?? 0} disabled={!canChange} min={field === "width" || field === "height" ? 1 : undefined} onChange={event => { const value = Number(event.target.value); if (Number.isFinite(value) && Math.abs(value) <= 1000000 && (!(field === "width" || field === "height") || value >= 1)) patch({ [field]: value }) }} /></Field>)}</div>
   <Field label={selected.type === "text" ? "Text color" : "Fill"}><div className="flex gap-2"><input aria-label="Fill color" type="color" className="h-9 w-10 rounded border bg-background p-1 coarse:h-[44px]" value={/^#[0-9a-f]{6}$/i.test(selected.fill) ? selected.fill : "#ffffff"} disabled={!canChange} onChange={event => patch({ fill: event.target.value })} /><Input aria-label="Fill value" value={selected.fill ?? ""} disabled={!canChange} onChange={event => patch({ fill: event.target.value })} /></div></Field>
   {selected.type === "text" && <Field label="Text"><textarea aria-label="Text content" maxLength={100000} className="min-h-24 w-full rounded border bg-background p-2 text-sm" value={selected.text ?? ""} disabled={!canChange} onChange={event => patch({ text: event.target.value })} /></Field>}
   {selected.type === "text" && <DesignTextStyleFields node={selected} disabled={!canChange} onChange={patch} />}
   {selected.type === "image" && <Field label="Image URL"><Input aria-label="Image URL" placeholder="https://…" value={selected.src ?? ""} disabled={!canChange} onChange={event => { setImageURL(event.target.value); patch({ src: event.target.value }) }} /><p className="mt-1 text-xs text-muted-foreground">HTTPS image or embedded PNG, JPEG, WebP, GIF.</p></Field>}
   {selected.type !== "frame" && <Field label="Screen"><select aria-label="Parent screen" className="h-9 w-full rounded border bg-background px-2 text-sm coarse:h-[44px]" value={selected.parentId ?? ""} disabled={!canChange} onChange={event => patch({ parentId: event.target.value })}><option value="">On canvas</option>{frames.map(frame => <option key={frame.id} value={frame.id}>{frame.name || "Screen"}</option>)}</select></Field>}
   {prototype && selected.type !== "frame" && (() => {
    const link = designLinkTarget(design.nodes, selected)
    return <Field label="Link to screen"><select aria-label="Link to screen" className="h-9 w-full rounded border bg-background px-2 text-sm coarse:h-[44px]" value={link.kind === "none" ? "" : link.kind === "screen" ? link.screen.id : link.target} disabled={!canChange} onChange={event => setLink(event.target.value)}><option value="">No link</option>{frames.map(frame => <option key={frame.id} value={frame.id}>{frame.name || "Screen"}</option>)}{link.kind === "missing" && <option value={link.target} disabled>Deleted screen</option>}</select>{link.kind === "missing" && <p className="mt-1 text-xs text-warn">The linked screen was deleted. In Play this layer does nothing{canChange ? "; choose another screen or No link." : "."}</p>}{link.kind !== "missing" && <p className="mt-1 text-xs text-muted-foreground">In Play, clicking this layer opens the screen.</p>}</Field>
   })()}
   {canChange && <Button variant="ghost" className="w-full text-destructive" onClick={() => { if (design.doc) removeDesignNode(design.doc, selected.id); setSelectedId(null) }}><Trash2 className="size-4" />Delete {selected.type === "frame" ? "screen and layers" : "layer"}</Button>}
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
   <button className="text-left text-muted-foreground" onClick={() => setSelectedId(comment.selector)}>v{comment.version} · {comment.quote || comment.selector}</button><p className="mt-2 whitespace-pre-wrap">{comment.text}</p>
   {comment.task_id && <p className="mt-2 text-muted-foreground">{comment.replies?.length ? `Done · v${comment.replies.at(-1)?.version}` : delivery[comment.id] ?? "Waiting for agent"}</p>}
   {comment.task_id && !comment.replies?.length && data.origin && data.can_edit && <Button size="sm" variant="ghost" disabled={!agent.ready} onClick={async () => { const accepted = await agent.dispatch(comment); setDelivery(previous => ({ ...previous, [comment.id]: accepted ? "Sent to agent" : agent.refusal?.(comment.id) === "busy" ? "Comment saved. The agent is busy; retry when it finishes." : "Comment saved. Agent delivery could not be confirmed." })) }}>Retry delivery</Button>}
   {comment.replies?.map((reply, index) => <p key={index} className="mt-2 border-t pt-2">v{reply.version}: {reply.text}</p>)}
  </article>)}
 </>
 const overlays = <>
  <CreateSurface open={continueOpen} onOpenChange={setContinueOpen} size="sm" onSubmit={()=>{if(!continuing&&continueName.trim())void continueDesign()}}><CreateSurfaceHeader title="Continue in a new Design" description="Create a copy of this canvas with fresh history. The original canvas and unsent feedback stay in this tab." onClose={()=>setContinueOpen(false)} /><CreateSurfaceBody><CreateSurfaceField label="New Design name" htmlFor="continued-design-name"><Input id="continued-design-name" value={continueName} maxLength={200} onChange={event=>setContinueName(event.target.value)} /></CreateSurfaceField>{continueError&&<p role="alert" className="text-destructive">{continueError}</p>}</CreateSurfaceBody><CreateSurfaceFooter onCancel={()=>setContinueOpen(false)} guardCancel={false} primaryLabel={continuing?"Creating…":"Create new Design"} onPrimary={()=>{void continueDesign()}} primaryDisabled={continuing||!continueName.trim()} /></CreateSurface>
  {prototype && playing && <DesignPrototypePlayer key={playing} nodes={design.nodes} startScreenId={playing} onClose={stopPlaying} />}
  {accessOpen && <DesignAccessDialog workspaceId={workspaceId} slug={slug} onClose={() => setAccessOpen(false)} />}
 </>

 if (framed) {
  const zoomLabel = zoom === "fit" ? `Fit · ${Math.round(scale * 100)} %` : `${Math.round(scale * 100)} %`
  const goTo = (index: number) => { setFocus(Math.max(0, Math.min(frames.length - 1, index))) }
  return <section aria-label="Design editor" className="relative flex min-h-0 flex-1 flex-col bg-muted/40 text-foreground" data-design-connection={design.connection} data-design-review={reviewOnly ? "phone" : undefined}>
   {hasBanners && <div className={cn("relative z-[11] shrink-0 bg-background", phone ? "mt-14" : "mt-16")}>{banners}</div>}
   <div ref={viewport} className="min-h-0 flex-1 overflow-auto" aria-label="Design canvas viewport"><div className={cn("w-max", phone ? "pb-32 pt-16" : "pb-16 pt-14")}>{canvasView}</div></div>
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
   </ArtifactSlot>}
   {reviewOnly && <ArtifactSlot name="tools" label="Review">
    {mayComment && <ArtifactTool icon={MessageSquare} label="Comment" text="Comment" on={artifactFrame?.drawerTab === "comments"} onClick={() => artifactFrame?.openDrawer("comments")} />}
    <span role="note" className="inline-flex items-center gap-1.5 px-2 text-xs text-muted-foreground"><Info aria-hidden className="size-3.5 shrink-0" />Review only. Edit on a larger screen.</span>
   </ArtifactSlot>}
   <ArtifactSlot name="viewport" label="Zoom">
    <ArtifactTool icon={ZoomOut} label="Zoom out" onClick={() => step(-1)} />
    <button type="button" aria-label="Zoom level" onClick={() => setZoom("fit")} className="h-[30px] min-w-16 rounded-[var(--radius-row)] px-1.5 font-mono text-xs text-muted-foreground hover:bg-accent hover:text-foreground">{zoomLabel}</button>
    <ArtifactTool icon={ZoomIn} label="Zoom in" onClick={() => step(1)} />
    <ArtifactToolSeparator />
    <ArtifactTool icon={Scan} label="Fit" on={zoom === "fit"} onClick={() => setZoom("fit")} />
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
     {design.editable && <><Button variant="outline" size="sm" onClick={() => add("frame")}><Plus className="size-4" />Screen</Button><Button variant="ghost" size="sm" onClick={() => add("rect")}><Square className="size-4" />Rectangle</Button><Button variant="ghost" size="sm" onClick={() => add("text")}><Type className="size-4" />Text</Button><Button variant="ghost" size="sm" onClick={() => add("image")}><ImagePlus className="size-4" />Image</Button></>}
     <span className="ml-auto text-xs text-muted-foreground">{selected ? selected.name || selected.type : design.editable ? "Drag to move · edit in Properties" : "View canvas"}</span>
    </div>
    <div className="min-h-0 flex-1 overflow-auto bg-muted/40" aria-label="Design canvas viewport">{canvasView}</div>
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
