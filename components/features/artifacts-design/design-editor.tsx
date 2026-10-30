"use client"

import { useEffect, useRef, useState, type PointerEvent } from "react"
import dynamic from "next/dynamic"
import { Download, Frame, ImagePlus, MousePointer2, Plus, Save, Square, Trash2, Type } from "lucide-react"
import * as Y from "yjs"
import { useArtifactAgentDispatch } from "@/hooks/use-artifact-agent-dispatch"
import { useSessionSafe } from "@/hooks/use-auth"
import { artifactsDesign } from "@/lib/feature-flags"
import { Button } from "@/components/ui/button"
import { Dialog, DialogContent, DialogHeader, DialogTitle, DialogDescription, DialogFooter } from "@/components/ui/dialog"
import { apiFetch } from "@/lib/api-fetch"
import { Input } from "@/components/ui/input"
import { useDesign } from "./use-design"
import { removeDesignNode, safeImageSource, upsertDesignNode, type DesignNode, type DesignNodeType } from "./design-document"

function encodedDesignState(doc: Y.Doc,maxBytes=Infinity) {
 const state=Y.encodeStateAsUpdate(doc)
 if(state.byteLength>maxBytes)throw new Error("Canvas exceeds the new Design size limit")
 let binary=""
 for(let index=0;index<state.length;index+=8192) binary+=String.fromCharCode(...state.subarray(index,index+8192))
 return btoa(binary)
}

const DesignAccessDialog = dynamic(() => import("./design-access-dialog").then(module => module.DesignAccessDialog), { ssr: false })

export function DesignEditor({ workspaceId, slug, initialVersion }: { workspaceId: string; slug: string; initialVersion?: number }) {
 const [accessOpen, setAccessOpen] = useState(false)
 const [version, setVersion] = useState(initialVersion)
 const design = useDesign(workspaceId, slug, version)
 const session = useSessionSafe()
 const commentPageId = design.query.data?.page_id
 const commentKey = session?.data?.user?.id && commentPageId ? `design-comment:v2:${session.data.user.id}:${workspaceId}:${commentPageId}` : null
 const [draft, setDraft] = useState("")
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
 useEffect(() => { try { setDraft(commentKey ? localStorage.getItem(commentKey) ?? "" : "") } catch { /* Draft remains in the open editor. */ } }, [commentKey])
 const changeDraft = (value: string) => { setDraft(value); if (commentKey) { try { localStorage.setItem(commentKey, value) } catch { /* Draft remains in the open editor. */ } } }
 const [selectedId, setSelectedId] = useState<string | null>(null)
 const [agentChat, setAgentChat] = useState("")
 const [imageURL, setImageURL] = useState("")
 const [panel, setPanel] = useState<"properties" | "history">("properties")
 const canvas = useRef<SVGSVGElement>(null)
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
 const patch = (values: Partial<DesignNode>) => {
  if (selected && design.doc && design.editable) upsertDesignNode(design.doc, { id: selected.id, ...values })
 }
 const add = (type: DesignNodeType) => {
  if (!design.doc || !design.editable) return
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
 }
 const point = (event: PointerEvent<SVGSVGElement> | PointerEvent<SVGGElement>) => {
  const bounds = canvas.current?.getBoundingClientRect()
  return { x: event.clientX - (bounds?.left ?? 0), y: event.clientY - (bounds?.top ?? 0) }
 }
 const beginDrag = (event: PointerEvent<SVGGElement>, node: DesignNode) => {
  event.stopPropagation()
  setSelectedId(node.id)
  setPanel("properties")
  if (!design.editable || !canvas.current) return
  const start = point(event)
  drag.current = { id: node.id, startX: start.x, startY: start.y, x: node.x, y: node.y }
  canvas.current.setPointerCapture(event.pointerId)
 }
 const move = (event: PointerEvent<SVGSVGElement>) => {
  const cursor = point(event)
  design.setCursor(cursor)
  if (!drag.current || !design.editable || !design.doc) return
  upsertDesignNode(design.doc, { id: drag.current.id, x: Math.round(drag.current.x + cursor.x - drag.current.startX), y: Math.round(drag.current.y + cursor.y - drag.current.startY) })
 }
 const endDrag = () => { drag.current = null }
 if (!artifactsDesign()) return <p className="p-6" role="status">Design is not enabled.</p>
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
  if (!selected || !draft.trim()) return
  setCommentError("")
  try {
   const checkpoint = design.editable ? await design.save.mutateAsync("Checkpoint before feedback") : undefined
   const posted = await design.comment.mutateAsync({ version: checkpoint?.version ?? viewedVersion, path: "design.json", selector: selected.id, quote: String(selected.text ?? selected.name ?? "").length <= 2000 ? String(selected.text ?? selected.name ?? "") : "", text: draft.trim(), send_to_agent: Boolean(data?.origin && data?.can_edit) })
   changeDraft("")
   if (data?.origin && data?.can_edit) {
    setDelivery(previous => ({ ...previous, [posted.id]: "Sending to agent…" }))
    const accepted = await agent.dispatch(posted)
    setDelivery(previous => ({ ...previous, [posted.id]: accepted ? "Sent to agent" : agent.refusal?.(posted.id) === "busy" ? "Comment saved. The agent is busy; retry when it finishes." : "Comment saved. Retry when the source chat is connected." }))
   }
  } catch (error) { setCommentError(error instanceof Error ? error.message : "Comment could not be saved.") }
 }
 return <section aria-label="Design editor" className="flex h-full min-h-[600px] flex-col bg-background text-foreground" data-design-connection={design.connection}>
  <header className="flex flex-wrap items-center gap-3 border-b px-4 py-3">
   <Frame className="size-5" aria-hidden="true" />
   <h1 className="mr-auto truncate font-semibold">{slug} <span className="ml-2 text-xs font-normal text-muted-foreground">Design</span></h1>
   <span role="status" className="text-xs text-muted-foreground">{version ? `Version ${version}` : historyFull ? "History full · changes kept in this browser" : design.connection === "connected" ? design.dirty ? "Live · changes saved as you work" : "Live · up to date" : design.connection === "denied" ? "Access changed · reopen to continue" : design.connection === "connecting" ? "Connecting…" : "Offline · changes kept in this browser"}</span>
   {!design.editable && <span className="rounded bg-muted px-2 py-1 text-xs">{version ? "History" : "Read only"}</span>}
   <div className="flex -space-x-1" aria-label="People on this canvas">{design.peers.map(peer => <span key={peer.clientId} title={peer.name} className="flex size-7 items-center justify-center rounded-full border-2 border-background text-xs font-medium text-white" style={{ backgroundColor: peer.color }}>{peer.name.slice(0, 2).toUpperCase()}</span>)}</div>
   <Button variant="outline" onClick={() => setAccessOpen(true)}>Access</Button>
   <Button variant="outline" onClick={() => setPanel(panel === "history" ? "properties" : "history")}>History{viewedVersion ? ` · v${viewedVersion}` : ""}</Button>
   {design.editable && <Button disabled={design.save.isPending || design.connection !== "connected"} onClick={() => design.save.mutate("Saved from Design editor")}><Save className="size-4" /> Save version</Button>}
  </header>
  {historyFull && <div role="alert" className="space-y-2 border-b px-4 py-3 text-sm"><p><b>History capacity is full.</b> Another version could not be saved. Existing versions are retained and your pending work stays in this browser.</p><p>Download your local canvas and unsent feedback as a draft file to keep a copy. History is not deleted automatically.</p>{version && <Button variant="outline" onClick={() => setVersion(undefined)}>Return to live canvas</Button>}<Button variant="outline" disabled={!design.doc || Boolean(version)} onClick={downloadDraft}><Download className="size-4" />Download draft</Button><Button variant="outline" disabled={!design.doc||Boolean(version)||!human||continuing} onClick={()=>{setContinueName(`${slug} (continued)`);setContinueSlug(`design-${crypto.randomUUID().replaceAll("-","").slice(0,24)}`);setContinueError("");setContinueOpen(true)}}>Continue in a new Design</Button>{downloadError && <p className="text-destructive">{downloadError}</p>}</div>}
  {continued?.scope===continueScope&&<div role="status" className="border-b px-4 py-3 text-sm"><p>A new Design contains the copied canvas. This original canvas, its history and your unsent feedback remain here.</p><a className="underline" href={`/artifacts/${encodeURIComponent(continued.page)}?workspace_id=${encodeURIComponent(workspaceId)}`} target="_blank" rel="noopener noreferrer">Open new Design</a></div>}
  {failure && !historyFull && <p role="alert" className="border-b px-4 py-2 text-sm text-destructive">{failure.message}</p>}
  {design.draftError && <p role="alert" className="border-b px-4 py-2 text-sm">Browser draft storage is unavailable or full. Keep this window open until connected and save a version.</p>}
  <div className="flex min-h-0 flex-1 flex-col md:flex-row">
   <aside className="flex max-h-40 shrink-0 flex-col overflow-auto border-b md:max-h-none md:w-48 md:border-b-0 md:border-r" aria-label="Layers">
    <div className="flex items-center gap-2 px-3 py-3 text-xs font-semibold uppercase tracking-wide text-muted-foreground">Screens <span className="ml-auto">{frames.length}</span></div>
    {frames.length === 0 && <p className="px-3 pb-3 text-xs text-muted-foreground">Add a screen to start designing.</p>}
    {design.nodes.map(node => <button key={node.id} onClick={() => setSelectedId(node.id)} className={`flex min-h-9 items-center gap-2 px-3 text-left text-xs coarse:min-h-[44px] ${selectedId === node.id ? "bg-accent text-accent-foreground" : "hover:bg-muted"} ${node.parentId ? "pl-7" : "font-medium"}`}>
     {node.type === "frame" ? <Frame className="size-3.5 shrink-0" /> : node.type === "text" ? <Type className="size-3.5 shrink-0" /> : <Square className="size-3.5 shrink-0" />}<span className="truncate">{node.name || node.text || node.type}</span>
    </button>)}
   </aside>
   <div className="relative flex min-h-96 min-w-0 flex-1 flex-col">
    <div className="z-10 flex flex-wrap items-center gap-1 border-b bg-background px-3 py-2" aria-label="Design tools">
     <Button variant="ghost" size="sm" title="Select and move"><MousePointer2 className="size-4" /><span className="sr-only">Select and move</span></Button>
     {design.editable && <><Button variant="outline" size="sm" onClick={() => add("frame")}><Plus className="size-4" />Screen</Button><Button variant="ghost" size="sm" onClick={() => add("rect")}><Square className="size-4" />Rectangle</Button><Button variant="ghost" size="sm" onClick={() => add("text")}><Type className="size-4" />Text</Button><Button variant="ghost" size="sm" onClick={() => add("image")}><ImagePlus className="size-4" />Image</Button></>}
     <span className="ml-auto text-xs text-muted-foreground">{selected ? selected.name || selected.type : design.editable ? "Drag to move · edit in Properties" : "Follow changes live"}</span>
    </div>
    <div className="min-h-0 flex-1 overflow-auto bg-muted/40" aria-label="Design canvas viewport">
     <svg ref={canvas} role="img" aria-label="Design canvas" width={width} height={height} className="touch-none" onPointerMove={move} onPointerUp={endDrag} onPointerCancel={endDrag} onPointerLeave={() => design.setCursor(null)} onPointerDown={() => setSelectedId(null)}>
      <defs><pattern id="design-dots" width="20" height="20" patternUnits="userSpaceOnUse"><circle cx="1" cy="1" r="1" fill="currentColor" className="text-border" /></pattern></defs>
      <rect width="100%" height="100%" fill="url(#design-dots)" />
      {[...frames, ...design.nodes.filter(node => node.type !== "frame")].map(node => {
       const { x, y } = position(node)
       const w = Math.max(1, Number(node.width) || 1), h = Math.max(1, Number(node.height) || 1)
       const image = node.type === "image" ? safeImageSource(node.src) : undefined
       return <g key={node.id} data-design-node={node.id} data-node-type={node.type} data-node-name={node.name} transform={`translate(${x},${y})`} onPointerDown={event => beginDrag(event, node)} style={{ cursor: design.editable ? "move" : "pointer" }}>
        {node.type === "frame" && <text x="0" y="-10" fontSize="12" fill="currentColor" className="text-muted-foreground">{node.name || "Screen"}</text>}
        {node.type === "text" ? <><rect width={w} height={h} fill="transparent" /><text x="0" y="22" fill={node.fill || "#172554"} fontSize="20" fontFamily="system-ui, sans-serif">{String(node.text ?? "").split("\n").map((line, index) => <tspan key={index} x="0" dy={index ? "1.3em" : "0"}>{line}</tspan>)}</text></> : node.type === "image" ? image ? <image href={image} width={w} height={h} preserveAspectRatio="xMidYMid slice" /> : <><rect width={w} height={h} fill="#e2e8f0" rx="4" /><text x="12" y="28" fill="#64748b" fontSize="14">Set an image URL in Properties</text></> : <rect width={w} height={h} fill={node.fill || "#ffffff"} rx={node.type === "frame" ? 0 : 6} stroke={node.type === "frame" ? "#cbd5e1" : "none"} />}
        {selectedId === node.id && <rect width={w} height={h} fill="none" stroke="#2563eb" strokeWidth="2" pointerEvents="none" />}
       </g>
      })}
      {design.peers.filter(peer => peer.cursor).map(peer => <g key={peer.clientId} transform={`translate(${peer.cursor!.x},${peer.cursor!.y})`} pointerEvents="none" aria-label={`${peer.name} cursor`}><path d="M0 0 L0 20 L5 15 L9 24 L13 22 L9 13 L17 13 Z" fill={peer.color} stroke="white" /><rect x="16" y="18" width={Math.min(160, peer.name.length * 7 + 16)} height="22" rx="4" fill={peer.color} /><text x="24" y="33" fontSize="12" fill="white">{peer.name.slice(0, 22)}</text></g>)}
     </svg>
    </div>
   </div>
   <aside className="max-h-80 w-full shrink-0 overflow-auto border-t p-4 md:max-h-none md:w-60 md:border-l md:border-t-0" aria-label={panel === "history" ? "Version history" : "Properties"}>
    {panel === "history" ? <>
     <h2 className="mb-3 text-sm font-semibold">History</h2>
     {Boolean(design.versions.data?.shared_version) && <a className="mb-3 inline-block text-xs underline" target="_blank" rel="noopener noreferrer" href={`/artifacts?design=1&item=${encodeURIComponent(slug)}&version=${design.versions.data!.shared_version}&workspace_id=${encodeURIComponent(workspaceId)}`}>Open shared version {design.versions.data!.shared_version}</a>}
     <Button className="mb-3 w-full" variant={version ? "outline" : "secondary"} onClick={() => setVersion(undefined)}>Live canvas</Button>
     {design.versions.isError && <p role="alert" className="text-sm text-destructive">{design.versions.error.message}</p>}
     {design.versions.data?.versions.map(item => <div key={item.version} className="mb-3 rounded border p-3 text-xs"><button className="w-full text-left" onClick={() => setVersion(item.version)}><span className="font-medium">Version {item.version}</span>{item.version === design.versions.data?.shared_version && <span className="ml-2 text-muted-foreground">Shared</span>}<p className="mt-1 text-muted-foreground">{item.message || "Design checkpoint"}</p>{item.created_at && <p className="mt-1 text-muted-foreground">{new Date(item.created_at).toLocaleString()}</p>}</button>{data.can_edit && <div className="mt-2 flex gap-2"><Button size="sm" variant="outline" disabled={design.restore.isPending} onClick={() => design.restore.mutate(item.version, { onSuccess: () => setVersion(undefined) })}>Restore</Button><Button size="sm" variant="ghost" disabled={design.share.isPending} onClick={() => design.share.mutate(item.version)}>Share</Button></div>}</div>)}
    </> : <>
     <h2 className="mb-4 text-sm font-semibold">Properties</h2>
     {selected ? <div className="space-y-3">
      <Field label="Name"><Input aria-label="Layer name" value={selected.name ?? ""} disabled={!design.editable} onChange={event => patch({ name: event.target.value })} /></Field>
      <div className="grid grid-cols-2 gap-2">{(["x", "y", "width", "height"] as const).map(field => <Field key={field} label={field === "width" ? "W" : field === "height" ? "H" : field.toUpperCase()}><Input type="number" aria-label={field} value={selected[field] ?? 0} disabled={!design.editable} min={field === "width" || field === "height" ? 1 : undefined} onChange={event => { const value = Number(event.target.value); if (Number.isFinite(value) && Math.abs(value) <= 1000000 && (!(field === "width" || field === "height") || value >= 1)) patch({ [field]: value }) }} /></Field>)}</div>
      <Field label={selected.type === "text" ? "Text color" : "Fill"}><div className="flex gap-2"><input aria-label="Fill color" type="color" className="h-9 w-10 rounded border bg-background p-1 coarse:h-[44px]" value={/^#[0-9a-f]{6}$/i.test(selected.fill) ? selected.fill : "#ffffff"} disabled={!design.editable} onChange={event => patch({ fill: event.target.value })} /><Input aria-label="Fill value" value={selected.fill ?? ""} disabled={!design.editable} onChange={event => patch({ fill: event.target.value })} /></div></Field>
      {selected.type === "text" && <Field label="Text"><textarea aria-label="Text content" maxLength={100000} className="min-h-24 w-full rounded border bg-background p-2 text-sm" value={selected.text ?? ""} disabled={!design.editable} onChange={event => patch({ text: event.target.value })} /></Field>}
      {selected.type === "image" && <Field label="Image URL"><Input aria-label="Image URL" placeholder="https://…" value={selected.src ?? ""} disabled={!design.editable} onChange={event => { setImageURL(event.target.value); patch({ src: event.target.value }) }} /><p className="mt-1 text-xs text-muted-foreground">HTTPS image or embedded PNG, JPEG, WebP, GIF.</p></Field>}
      {selected.type !== "frame" && <Field label="Screen"><select aria-label="Parent screen" className="h-9 w-full rounded border bg-background px-2 text-sm coarse:h-[44px]" value={selected.parentId ?? ""} disabled={!design.editable} onChange={event => patch({ parentId: event.target.value })}><option value="">On canvas</option>{frames.map(frame => <option key={frame.id} value={frame.id}>{frame.name || "Screen"}</option>)}</select></Field>}
      {design.editable && <Button variant="ghost" className="w-full text-destructive" onClick={() => { if (design.doc) removeDesignNode(design.doc, selected.id); setSelectedId(null) }}><Trash2 className="size-4" />Delete {selected.type === "frame" ? "screen and layers" : "layer"}</Button>}
     </div> : <p className="text-sm text-muted-foreground">Select a screen or layer to see its properties.{design.editable && " Add your first screen using the toolbar."}</p>}
    </>}
    <div className="mt-6 border-t pt-4" aria-label="Design comments">
     <h2 className="mb-3 text-sm font-semibold">Comments</h2>
     {design.comments.isError && <p role="alert" className="mb-3 text-xs text-destructive">{design.comments.error.message}</p>}
     {data.can_edit && !data.origin && <div className="mb-4 space-y-2 rounded border p-3">
      <p className="text-xs text-muted-foreground">Connect a private agent chat to send feedback directly to your agent.</p>
      <Input aria-label="Agent chat ID" placeholder="Chat ID" value={agentChat} onChange={event => setAgentChat(event.target.value)} />
      <Button size="sm" variant="outline" disabled={!agentChat.trim() || design.connectAgent.isPending} onClick={() => design.connectAgent.mutate(agentChat.trim())}>Connect agent chat</Button>
     </div>}
     {data.origin && <a className="mb-3 inline-block text-xs underline" href={`/chat/${encodeURIComponent(data.origin.chat_id)}?workspace_id=${encodeURIComponent(workspaceId)}`}>Open agent chat</a>}
     {data.can_comment !== false && (data.can_comment || data.can_edit) && <div className="mb-4 space-y-2">
      <p className="text-xs text-muted-foreground">{selected ? `On ${selected.name || selected.type}` : "Select a layer on the canvas to leave precise feedback."}</p>
      <textarea aria-label="Comment for agent" className="min-h-20 w-full rounded border bg-background p-2 text-sm" placeholder="What should change?" value={draft} onChange={event => changeDraft(event.target.value)} />
      <Button size="sm" disabled={!selected || !draft.trim() || design.comment.isPending || design.save.isPending} onClick={() => { void sendComment() }}>{data.origin && data.can_edit ? "Send to agent" : "Save comment"}</Button>
      {commentError && <p role="alert" className="text-xs text-destructive">{commentError}</p>}
     </div>}
     {(design.comments.data?.comments ?? []).map(comment => <article key={comment.id} className="mb-3 rounded border p-3 text-xs">
      <button className="text-left text-muted-foreground" onClick={() => setSelectedId(comment.selector)}>v{comment.version} · {comment.quote || comment.selector}</button><p className="mt-2 whitespace-pre-wrap">{comment.text}</p>
      {comment.task_id && <p className="mt-2 text-muted-foreground">{comment.replies?.length ? `Done · v${comment.replies.at(-1)?.version}` : delivery[comment.id] ?? "Waiting for agent"}</p>}
      {comment.task_id && !comment.replies?.length && data.origin && data.can_edit && <Button size="sm" variant="ghost" disabled={!agent.ready} onClick={async () => { const accepted = await agent.dispatch(comment); setDelivery(previous => ({ ...previous, [comment.id]: accepted ? "Sent to agent" : agent.refusal?.(comment.id) === "busy" ? "Comment saved. The agent is busy; retry when it finishes." : "Comment saved. Agent delivery could not be confirmed." })) }}>Retry delivery</Button>}
      {comment.replies?.map((reply, index) => <p key={index} className="mt-2 border-t pt-2">v{reply.version}: {reply.text}</p>)}
     </article>)}
    </div>
   </aside>
  </div>
  <Dialog open={continueOpen} onOpenChange={setContinueOpen}><DialogContent><DialogHeader><DialogTitle>Continue in a new Design</DialogTitle><DialogDescription>Create a copy of this canvas with fresh history. The original canvas and unsent feedback stay in this tab.</DialogDescription></DialogHeader><label htmlFor="continued-design-name">New Design name</label><Input id="continued-design-name" value={continueName} maxLength={200} onChange={event=>setContinueName(event.target.value)} />{continueError&&<p role="alert" className="text-destructive">{continueError}</p>}<DialogFooter><Button variant="outline" onClick={()=>setContinueOpen(false)}>Cancel</Button><Button disabled={continuing||!continueName.trim()} onClick={()=>{void continueDesign()}}>{continuing?"Creating…":"Create new Design"}</Button></DialogFooter></DialogContent></Dialog>
  {accessOpen && <DesignAccessDialog workspaceId={workspaceId} slug={slug} onClose={() => setAccessOpen(false)} />}
 </section>
}

function Field({ label, children }: { label: string; children: React.ReactNode }) {
 return <label className="block text-xs text-muted-foreground"><span className="mb-1.5 block">{label}</span>{children}</label>
}
