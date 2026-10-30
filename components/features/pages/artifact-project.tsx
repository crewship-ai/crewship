"use client"

import { useCallback, useEffect, useMemo, useRef, useState } from "react"
import Link from "next/link"
import { useArtifactAgentDispatch } from "@/hooks/use-artifact-agent-dispatch"
import { useSessionSafe } from "@/hooks/use-auth"
import { Button } from "@/components/ui/button"
import { useArtifactProject, type ArtifactAnchor, type ArtifactProjectSave } from "@/hooks/use-artifact-project"
import { artifactProjectDocument, artifactProjectQuote } from "@/lib/pages/artifact-project-preview"
import { artifactDashboards } from "@/lib/feature-flags"
import { ArtifactDashboardPreview } from "@/components/features/chat/artifact-context/viewer/artifact-dashboard-preview"
import { artifactSlidesMode } from "@/lib/pages/artifact-slides"
import { ArtifactSlidesPreview } from "@/components/features/chat/artifact-context/viewer/artifact-slides-preview"
import { artifactDraftKey, loadArtifactDraft, writeArtifactDraft } from "@/lib/artifact-drafts"

export function ArtifactProject({ workspaceId, slug, initialVersion }: { workspaceId: string; slug: string; initialVersion?: number }) {
 const session = useSessionSafe()
 const [version, setVersion] = useState<number | undefined>(initialVersion)
 const { query, restore, share, comment, save } = useArtifactProject(workspaceId, slug, version)
 const [screenPath, setScreenPath] = useState("")
 const [selection, setSelection] = useState<{scope:string; selecting:boolean; anchor?:ArtifactAnchor}>()
 const draftKey = artifactDraftKey("artifact-comment", session?.data?.user?.id, workspaceId, slug)
 const [draftState, setDraftState] = useState<{key:string;text:string} | null>(null)
 const draft = draftKey && draftState?.key === draftKey ? draftState.text : ""
 const setDraft = (text: string) => setDraftState(draftKey ? {key:draftKey,text} : null)
 const [storageError, setStorageError] = useState(false)
 const frame = useRef<HTMLIFrameElement>(null)
 const sourceScope=useMemo(()=>({human:session?.data?.user?.id,workspaceId,slug}),[session?.data?.user?.id,workspaceId,slug])
 const [sourceDraft,setSourceDraft]=useState<{scope:object;body:ArtifactProjectSave;path:string}>()
 const [sourceError,setSourceError]=useState<{scope:object;message:string;hidePreview:boolean}>()
 const activeSource=sourceDraft?.scope===sourceScope?sourceDraft:undefined
 // An unsaved source draft survives a crash or closed tab in this browser
 // (IndexedDB, strict durability: lib/artifact-drafts); it is only offered
 // back, never saved over newer work automatically.
 const sourceKey=artifactDraftKey("artifact-source",sourceScope.human,workspaceId,slug)
 const [storedSource,setStoredSource]=useState<{key:string;draft:{body:ArtifactProjectSave;path:string}}>()
 // Edit source starts a draft that is written to the same key, so it waits
 // until the kept draft is known: a failed or pending read must not let a
 // fresh edit overwrite it.
 const [sourceRead,setSourceRead]=useState<{key:string;failed:boolean}>()
 const [sourceReadAttempt,setSourceReadAttempt]=useState(0)
 useEffect(()=>{
  if(!sourceKey)return
  let live=true
  loadArtifactDraft(sourceKey).then(raw=>{
   if(!live)return
   try{const parsed=raw?JSON.parse(raw):null;setStoredSource(parsed&&Array.isArray(parsed.body?.files)&&typeof parsed.path==="string"?{key:sourceKey,draft:parsed}:undefined)}catch{setStoredSource(undefined)}
   setSourceRead({key:sourceKey,failed:false})
  },()=>{if(live)setSourceRead({key:sourceKey,failed:true})})
  return()=>{live=false}
 },[sourceKey,sourceReadAttempt])
 const sourceReadDone=sourceRead?.key===sourceKey
 useEffect(()=>{
  if(!sourceKey||!activeSource)return
  writeArtifactDraft(sourceKey,JSON.stringify({body:activeSource.body,path:activeSource.path})).then(()=>setStorageError(false),()=>setStorageError(true))
 },[sourceKey,activeSource])
 const forgetSource=useCallback((key:string|null)=>{if(!key)return;void writeArtifactDraft(key,"").catch(()=>{});setStoredSource(undefined)},[])
 const sourceCurrent=useRef({scope:sourceScope,draft:activeSource,canWrite:false})
 const data = query.data
 sourceCurrent.current={scope:sourceScope,draft:activeSource,canWrite:data?.can_write===true&&!query.isError}
 const agent = useArtifactAgentDispatch(workspaceId, data?.page_id, data?.origin, data?.can_write === true && data?.can_send_to_agent === true && !query.isError)
 const [delivery, setDelivery] = useState<Record<string,string>>({})
 const manifest = data?.manifest
 const slidesMode = artifactSlidesMode(data?.artifact_kind, manifest?.files ?? [])
 const path = manifest?.files.some(file => file.path === screenPath) ? screenPath : manifest?.entrypoint ?? ""
 const canComment = !query.isError && (data?.can_write === true || (data?.can_comment === true && data.shared_version > 0 && manifest?.version === data.shared_version))
 const selectionScope = JSON.stringify([session?.data?.user?.id, workspaceId, slug, data?.page_id, manifest?.version, path, canComment, data?.can_write, data?.can_comment, data?.can_send_to_agent])
 const submissionScope = useMemo(() => ({scope:selectionScope}), [selectionScope])
 const current = useRef({submissionScope, draftKey, draft, canWrite:data?.can_write === true && data?.can_send_to_agent === true})
 current.current = {submissionScope, draftKey, draft, canWrite:data?.can_write === true && data?.can_send_to_agent === true}
 const selecting = selection?.scope === selectionScope && selection.selecting
 const anchor = selection?.scope === selectionScope ? selection.anchor : undefined
 const setSelecting = useCallback((value:boolean) => setSelection(previous => ({...(previous?.scope === selectionScope ? previous : {}),scope:selectionScope,selecting:value})), [selectionScope])
 const setAnchor = useCallback((value:ArtifactAnchor | undefined) => setSelection(previous => ({scope:selectionScope,selecting:previous?.scope === selectionScope && previous.selecting,anchor:value})), [selectionScope])
 useEffect(() => { setSelection(undefined) }, [selectionScope])
 const token = useMemo(() => [path, manifest?.version, selecting, typeof crypto !== "undefined" ? Array.from(crypto.getRandomValues(new Uint8Array(16)), value => value.toString(16).padStart(2, "0")).join("") : ""].join(":"), [path, manifest?.version, selecting])
 const source = useMemo(() => manifest && !slidesMode ? artifactProjectDocument(manifest.files, path, token, selecting) : "", [manifest, path, token, selecting, slidesMode])
 useEffect(() => {
  if (!draftKey) { setDraftState(null); return }
  let live = true
  // Text typed while the stored draft loads wins over the stored copy. A failed
  // read leaves the state empty, so nothing writes over the copy on disk.
  loadArtifactDraft(draftKey).then(text => { if (live) setDraftState(previous => previous?.key === draftKey ? previous : {key:draftKey,text}) }, () => { if (live) setStorageError(true) })
  return () => { live = false }
 }, [draftKey])
 useEffect(() => {
  if (!draftKey || draftState?.key !== draftKey) return
  writeArtifactDraft(draftKey, draftState.text).then(() => setStorageError(false), () => setStorageError(true))
 }, [draftState, draftKey])
 useEffect(() => {
  const receive = (event: MessageEvent) => {
   if (event.origin !== "null" || event.source !== frame.current?.contentWindow || event.data?.token !== token || !manifest) return
   if (typeof event.data.screen === "string" && manifest.files.some(file => file.path === event.data.screen && file.media_type === "text/html")) { setScreenPath(event.data.screen); setAnchor(undefined); return }
   if (!selecting || !canComment || typeof event.data.selector !== "string" || event.data.selector.length > 2000 || typeof event.data.quote !== "string") return
   setAnchor({ version: manifest.version, path, selector: event.data.selector, quote: artifactProjectQuote(event.data.quote) })
   setSelecting(false)
  }
  window.addEventListener("message", receive)
  return () => window.removeEventListener("message", receive)
 }, [token, manifest, path, selecting, canComment, setAnchor, setSelecting])
 // A query error immediately removes cached content, including after revocation.
 if (query.isError) return <section className="p-6"><p role="alert">{query.error.message}</p>{activeSource&&<SourceRecovery body={activeSource.body}/>}</section>
 if (!data) return <p role="status" className="p-6">Loading artifact project…</p>
 const mutationError = restore.error ?? share.error ?? comment.error
 const viewedVersion = version ?? manifest?.version ?? data.latest_version
 const selectVersion = (value: number) => { setVersion(value); setAnchor(undefined); setSelecting(false) }
 const saveSource=()=>{
  const submitted=activeSource
  if(!submitted||!data.can_write||save.isPending||sourceCurrent.current.scope!==submitted.scope)return
  save.mutate(submitted.body,{
   onSuccess:()=>{
    if(sourceCurrent.current.scope!==submitted.scope||!sourceCurrent.current.canWrite)return
    // Edits typed after the save was submitted are not saved yet: keep them and their durable copy.
    if(sourceCurrent.current.draft===submitted){setSourceDraft(previous=>previous===submitted?undefined:previous);forgetSource(sourceKey)}
    setSourceError(undefined);setVersion(undefined)
   },
   onError:(error)=>{
    if(sourceCurrent.current.scope!==submitted.scope)return
    setSourceError({scope:submitted.scope,message:error.message,hidePreview:![409,413].includes((error as Error&{status?:number}).status??0)})
   },
  })
 }
 const send = () => {
  if (!anchor || anchor.version !== manifest?.version || !draft.trim() || !canComment) return
  const submitted = current.current
  comment.mutate({ ...anchor, text: draft, send_to_agent: data.can_write === true && data.can_send_to_agent === true }, { onSuccess: async posted => {
   if (current.current.submissionScope !== submitted.submissionScope) return
   setDraftState(previous => previous?.key === submitted.draftKey && previous.text === submitted.draft ? {...previous,text:""} : previous)
   if (current.current.draftKey === submitted.draftKey && current.current.draft === submitted.draft) setSelection(previous => previous?.scope === selectionScope && previous.anchor === anchor ? {...previous,anchor:undefined} : previous)
   if (data.can_write === true && data.can_send_to_agent === true && current.current.canWrite && data.origin) {
    setDelivery(previous => ({...previous,[posted.id]:"Sending to agent…"}))
    const accepted = await agent.dispatch(posted)
    if (current.current.submissionScope !== submitted.submissionScope || !current.current.canWrite) return
    setDelivery(previous => ({...previous,[posted.id]:accepted ? "Sent to agent" : agent.refusal?.(posted.id) === "busy" ? "Comment saved. The agent is busy; retry when it finishes." : "Comment saved. Agent delivery could not be confirmed. Retry when the source chat is connected."}))
   }
  } })
 }
 return <main className="flex h-[calc(100dvh-var(--app-header-h)-var(--mobile-tab-bar-h))] min-h-0 flex-col">
  <header className="flex flex-wrap items-center gap-3 border-b p-4">
   <Link href={`/pages/${encodeURIComponent(slug)}?workspace_id=${encodeURIComponent(workspaceId)}`} className="inline-flex items-center text-sm underline coarse:min-h-[44px]">Back to Page</Link>
   <h1 className="mr-auto text-lg font-semibold">{slug} · {slidesMode === "slides" ? "Slides" : "Artifact project"}</h1>
   {!data.can_write && <span className="text-sm text-muted-foreground">Read only</span>}
   <label className="flex items-center gap-2 text-sm">Version<select aria-label="Version" className="rounded border bg-background p-2 coarse:min-h-[44px]" value={viewedVersion} onChange={event => selectVersion(Number(event.target.value))}>{data.versions.map(item => <option key={item.version} value={item.version}>Version {item.version}{item.version === data.shared_version ? " · shared" : ""}</option>)}</select></label>
   {data.can_write === true && data.can_restore === true && <Button variant="outline" disabled={restore.isPending} onClick={() => restore.mutate({ version: viewedVersion, base_version: data.latest_version }, { onSuccess: () => { setVersion(undefined); setAnchor(undefined) } })}>Restore version {viewedVersion}</Button>}{data.can_write === true && data.can_share === true && <Button variant="outline" disabled={share.isPending} onClick={() => share.mutate({ version: viewedVersion, base_version: data.latest_version })}>Share version {viewedVersion}</Button>}
  </header>
  {data.can_write && <div className="border-b px-4 py-2 text-xs text-muted-foreground" role="status">Storage: {data.quota.used_bytes.toLocaleString()} / {data.quota.limit_bytes.toLocaleString()} bytes. All versions retained; history is never deleted automatically.{data.quota.warning && <strong className="ml-2 text-destructive">Storage is nearly full. Existing versions remain safe.</strong>}</div>}
  {activeSource && !data.can_write && <SourceRecovery body={activeSource.body}/>}
  {data.can_write === true && manifest?.version === data.latest_version && <section aria-label="Project source" className="border-b p-3">
   {!activeSource&&storedSource?.key===sourceKey&&data.can_write&&<div role="status" className="space-y-1"><p>Unsaved source draft from version {storedSource.draft.body.base_version} was kept in this browser.{storedSource.draft.body.base_version!==data.latest_version?` The project is now at version ${data.latest_version}; saving it will report a conflict, and your draft stays.`:""}</p><Button variant="outline" onClick={()=>{setSourceError(undefined);setSourceDraft({scope:sourceScope,...storedSource.draft})}}>Restore source draft</Button><Button variant="ghost" onClick={()=>forgetSource(sourceKey)}>Discard it</Button></div>}
   {!activeSource&&sourceReadDone&&sourceRead.failed&&<div role="alert" className="space-y-1"><p>This browser could not check for an unsaved source draft. Editing now may replace one kept here.</p><Button variant="outline" onClick={()=>setSourceReadAttempt(attempt=>attempt+1)}>Check again</Button></div>}
   {!activeSource ? (storedSource?.key===sourceKey ? null : <Button variant="outline" disabled={!sourceReadDone} onClick={()=>{setSourceError(undefined);setSourceDraft({scope:sourceScope,path:manifest!.entrypoint,body:{base_version:data.latest_version,entrypoint:manifest!.entrypoint,files:manifest!.files.map(file=>({...file}))}})}}>Edit source</Button>) : <div className="space-y-2">
    <label>Source file path<select aria-label="Source file path" value={activeSource.path} onChange={event=>setSourceDraft({...activeSource,path:event.target.value})} className="ml-2 rounded border bg-background p-2 coarse:min-h-[44px]">{activeSource.body.files.map(file=><option key={file.path} value={file.path}>{file.path}</option>)}</select></label>
    <textarea aria-label="Source file" className="min-h-48 w-full rounded border bg-background p-2 font-mono text-sm" value={activeSource.body.files.find(file=>file.path===activeSource.path)?.content??""} onChange={event=>setSourceDraft({...activeSource,body:{...activeSource.body,files:activeSource.body.files.map(file=>file.path===activeSource.path?{...file,content:event.target.value}:file)}})} />
    <Button disabled={save.isPending} onClick={saveSource}>Save project</Button><Button variant="ghost" disabled={save.isPending} onClick={()=>{setSourceDraft(undefined);forgetSource(sourceKey)}}>Discard source draft</Button>
    <p className="text-xs">Draft from version {activeSource.body.base_version}. Kept in this browser until you save or discard it.</p>
   </div>}
   {sourceError?.scope===sourceScope&&<p role="alert">{sourceError.message}</p>}
  </section>}
  {mutationError && <p role="alert" className="px-4 py-2 text-sm text-destructive">{mutationError.message}</p>}
  <div className="flex min-h-0 flex-1 flex-col md:flex-row">
   <section aria-label="Project screens" className="flex min-h-0 flex-1 flex-col">
    <nav aria-label="Screens" className="flex flex-wrap gap-2 border-b p-3">{!slidesMode && manifest?.files.filter(file => file.media_type === "text/html" || /\.html?$/.test(file.path)).map(file => <Button key={file.path} variant={file.path === path ? "secondary" : "ghost"} onClick={() => { setScreenPath(file.path); setAnchor(undefined) }}>{file.path}</Button>)}{canComment && slidesMode !== "unsupported" && <Button className="ml-auto" variant={selecting ? "secondary" : "outline"} aria-pressed={selecting} onClick={() => setSelecting(!selecting)}>{selecting ? "Cancel selection" : "Select an element"}</Button>}</nav>
    {sourceError?.scope===sourceScope&&sourceError.hidePreview ? <p role="alert">Project preview is unavailable. Reload to confirm access.</p> : slidesMode === "unsupported" ? <p role="alert" className="p-4">This Slides format is unsupported.</p>
     : slidesMode === "slides" && manifest ? <ArtifactSlidesPreview workspaceId={workspaceId} pageId={data.page_id} codeVersion={manifest.version} artifactKind={data.artifact_kind} path={screenPath} title="Artifact preview" selectMode={selecting && canComment}
       className="min-h-72 flex-1" onSelect={selection => { if (!canComment || !selecting) return; setAnchor({ version: manifest.version, path, ...selection }); setSelecting(false) }} onNavigate={next => { setScreenPath(next); setAnchor(undefined) }} />
     : artifactDashboards() && manifest?.files.some(file => file.path === "dashboard.json")
     ? <ArtifactDashboardPreview workspaceId={workspaceId} pageId={data.page_id} codeVersion={manifest.version} files={manifest.files} path={path} title="Artifact preview" selectMode={selecting && canComment}
       className="min-h-72 flex-1" onSelect={selection => { if (!canComment || !selecting) return; setAnchor({ version: manifest.version, path, ...selection }); setSelecting(false) }} onNavigate={next => { setScreenPath(next); setAnchor(undefined) }} />
     : <iframe ref={frame} key={`${manifest?.version}:${path}:${token}`} title="Artifact preview" data-screen={path} className="min-h-72 w-full flex-1 border-0 bg-white" sandbox="allow-scripts" referrerPolicy="no-referrer" srcDoc={source} />}
   </section>
   <aside className="w-full overflow-auto border-t p-4 md:w-80 md:border-l md:border-t-0" aria-label="Comments">
    <h2 className="mb-3 font-semibold">Comments</h2>
    {canComment && <div className="mb-4 space-y-2">{anchor ? <blockquote className="rounded border p-2 text-sm">Version {anchor.version} · {anchor.path}<p>{anchor.quote || anchor.selector}</p></blockquote> : <p className="text-sm text-muted-foreground">Select an element in the preview to give the agent an exact location.</p>}<textarea aria-label={data.can_write && data.can_send_to_agent ? "Comment for agent" : "Comment"} value={draft} onChange={event => setDraft(event.target.value)} className="min-h-24 w-full rounded border bg-background p-2 text-sm" placeholder="What should change?" /><p className="text-xs text-muted-foreground">{storageError ? "Browser storage unavailable. Copy your draft before leaving." : "Your unsent comment is saved in this browser."}</p><Button disabled={!anchor || !draft.trim() || comment.isPending} onClick={send}>{data.can_write && data.can_send_to_agent ? "Send to agent" : "Post comment"}</Button></div>}
    {data.comments.map(item => <article key={item.id} className="mb-3 rounded border p-3 text-sm"><p className="mb-1 text-xs text-muted-foreground">Version {item.version} · {item.path}</p><blockquote className="mb-2 border-l-2 pl-2">{item.quote}</blockquote><p>{item.text}</p>{item.task_id && <p className="mt-2 text-xs">{item.replies?.length ? "Answered" : delivery[item.id] ?? "Queued for agent"}</p>}{data.can_write && data.can_send_to_agent === true && data.origin && item.task_id && !item.replies?.length && <Button size="sm" variant="ghost" disabled={!agent.ready} onClick={async () => { const accepted = await agent.dispatch(item); setDelivery(previous => ({...previous,[item.id]:accepted ? "Sent to agent" : "Comment saved. Agent delivery could not be confirmed."})) }}>Retry agent delivery</Button>}{item.replies?.map((reply, index) => <p key={index} className="mt-2 border-t pt-2">Version {reply.version}: {reply.text}</p>)}</article>)}
    {data.shared_version > 0 && <Link className="inline-flex items-center text-sm underline coarse:min-h-[44px]" onClick={event => { if (event.button === 0 && !event.metaKey && !event.ctrlKey && !event.shiftKey && !event.altKey && version !== data.shared_version) selectVersion(data.shared_version) }} href={`/pages/${encodeURIComponent(slug)}?artifact=1&version=${data.shared_version}&workspace_id=${encodeURIComponent(workspaceId)}`}>Open shared version {data.shared_version}</Link>}
   </aside>
  </div>
 </main>
}

function SourceRecovery({body}:{body:ArtifactProjectSave}){return <div className="mt-3 space-y-2 p-3"><p>Local source draft. Copy it before leaving this page.</p><textarea aria-label="Source recovery draft" readOnly className="min-h-48 w-full rounded border bg-background p-2 font-mono text-sm" value={JSON.stringify(body,null,2)} /></div>}
