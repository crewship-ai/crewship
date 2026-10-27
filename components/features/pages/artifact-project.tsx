"use client"

import { useEffect, useMemo, useRef, useState } from "react"
import Link from "next/link"
import { useArtifactAgentDispatch } from "@/hooks/use-artifact-agent-dispatch"
import { useSessionSafe } from "@/hooks/use-auth"
import { Button } from "@/components/ui/button"
import { useArtifactProject, type ArtifactAnchor } from "@/hooks/use-artifact-project"
import { artifactProjectDocument } from "@/lib/pages/artifact-project-preview"

export function ArtifactProject({ workspaceId, slug, initialVersion }: { workspaceId: string; slug: string; initialVersion?: number }) {
 const session = useSessionSafe()
 const [version, setVersion] = useState<number | undefined>(initialVersion)
 const { query, restore, share, comment } = useArtifactProject(workspaceId, slug, version)
 const [screenPath, setScreenPath] = useState("")
 const [selecting, setSelecting] = useState(false)
 const [anchor, setAnchor] = useState<ArtifactAnchor>()
 const draftKey = session?.data?.user?.id ? `artifact-comment:${session.data.user.id}:${workspaceId}:${slug}` : null
 const [draftState, setDraftState] = useState<{key:string;text:string} | null>(null)
 const draft = draftKey && draftState?.key === draftKey ? draftState.text : ""
 const setDraft = (text: string) => setDraftState(draftKey ? {key:draftKey,text} : null)
 const [storageError, setStorageError] = useState(false)
 const frame = useRef<HTMLIFrameElement>(null)
 const data = query.data
 const agent = useArtifactAgentDispatch(workspaceId, data?.page_id, data?.origin, data?.can_write === true && !query.isError)
 const [delivery, setDelivery] = useState<Record<string,string>>({})
 const manifest = data?.manifest
 const path = manifest?.files.some(file => file.path === screenPath) ? screenPath : manifest?.entrypoint ?? ""
 const token = useMemo(() => [path, manifest?.version, selecting, typeof crypto !== "undefined" ? Array.from(crypto.getRandomValues(new Uint8Array(16)), value => value.toString(16).padStart(2, "0")).join("") : ""].join(":"), [path, manifest?.version, selecting])
 const source = useMemo(() => manifest ? artifactProjectDocument(manifest.files, path, token, selecting) : "", [manifest, path, token, selecting])
 useEffect(() => {
  if (!draftKey) { setDraftState(null); return }
  try { setDraftState({key:draftKey,text:localStorage.getItem(draftKey) ?? ""}); setStorageError(false) } catch { setDraftState({key:draftKey,text:""}); setStorageError(true) }
 }, [draftKey])
 useEffect(() => {
  if (!draftKey || draftState?.key !== draftKey) return
  try { if (draftState.text) localStorage.setItem(draftKey, draftState.text); else localStorage.removeItem(draftKey) } catch { setStorageError(true) }
 }, [draftState, draftKey])
 useEffect(() => {
  const receive = (event: MessageEvent) => {
   if (event.origin !== "null" || event.source !== frame.current?.contentWindow || event.data?.token !== token || !manifest) return
   if (typeof event.data.screen === "string" && manifest.files.some(file => file.path === event.data.screen && file.media_type === "text/html")) { setScreenPath(event.data.screen); setAnchor(undefined); return }
   if (!selecting || !data?.can_write || typeof event.data.selector !== "string" || event.data.selector.length > 2000 || typeof event.data.quote !== "string") return
   setAnchor({ version: manifest.version, path, selector: event.data.selector, quote: event.data.quote.slice(0,500) })
   setSelecting(false)
  }
  window.addEventListener("message", receive)
  return () => window.removeEventListener("message", receive)
 }, [token, manifest, path, selecting, data?.can_write])
 // A query error immediately removes cached content, including after revocation.
 if (query.isError) return <p role="alert" className="p-6">{query.error.message}</p>
 if (!data) return <p role="status" className="p-6">Loading artifact project…</p>
 const mutationError = restore.error ?? share.error ?? comment.error
 const viewedVersion = version ?? manifest?.version ?? data.latest_version
 const selectVersion = (value: number) => { setVersion(value); setAnchor(undefined); setSelecting(false) }
 const send = () => {
  if (!anchor || !draft.trim() || !data.can_write) return
  comment.mutate({ ...anchor, text: draft, send_to_agent: true }, { onSuccess: async posted => { setDraft(""); setAnchor(undefined); if (data.origin) { setDelivery(previous => ({...previous,[posted.id]:"Sending to agent…"})); const accepted = await agent.dispatch(posted); setDelivery(previous => ({...previous,[posted.id]:accepted ? "Sent to agent" : "Comment saved. Agent delivery could not be confirmed. Retry when the source chat is connected."})) } } })
 }
 return <main className="flex h-[calc(100dvh-var(--app-header-h)-var(--mobile-tab-bar-h))] min-h-0 flex-col">
  <header className="flex flex-wrap items-center gap-3 border-b p-4">
   <Link href={`/pages/${encodeURIComponent(slug)}?workspace_id=${encodeURIComponent(workspaceId)}`} className="inline-flex items-center text-sm underline coarse:min-h-[44px]">Back to Page</Link>
   <h1 className="mr-auto text-lg font-semibold">{slug} · Artifact project</h1>
   {!data.can_write && <span className="text-sm text-muted-foreground">Read only</span>}
   <label className="flex items-center gap-2 text-sm">Version<select aria-label="Version" className="rounded border bg-background p-2 coarse:min-h-[44px]" value={viewedVersion} onChange={event => selectVersion(Number(event.target.value))}>{data.versions.map(item => <option key={item.version} value={item.version}>Version {item.version}{item.version === data.shared_version ? " · shared" : ""}</option>)}</select></label>
   {data.can_write && <><Button variant="outline" disabled={restore.isPending} onClick={() => restore.mutate({ version: viewedVersion, base_version: data.latest_version }, { onSuccess: () => { setVersion(undefined); setAnchor(undefined) } })}>Restore version {viewedVersion}</Button><Button variant="outline" disabled={share.isPending} onClick={() => share.mutate({ version: viewedVersion, base_version: data.latest_version })}>Share version {viewedVersion}</Button></>}
  </header>
  {data.can_write && <div className="border-b px-4 py-2 text-xs text-muted-foreground" role="status">Storage: {data.quota.used_bytes.toLocaleString()} / {data.quota.limit_bytes.toLocaleString()} bytes. All versions retained; history is never deleted automatically.{data.quota.warning && <strong className="ml-2 text-destructive">Storage is nearly full. Existing versions remain safe.</strong>}</div>}
  {mutationError && <p role="alert" className="px-4 py-2 text-sm text-destructive">{mutationError.message}</p>}
  <div className="flex min-h-0 flex-1 flex-col md:flex-row">
   <section aria-label="Project screens" className="flex min-h-0 flex-1 flex-col">
    <nav aria-label="Screens" className="flex flex-wrap gap-2 border-b p-3">{manifest?.files.filter(file => file.media_type === "text/html" || /\.html?$/.test(file.path)).map(file => <Button key={file.path} variant={file.path === path ? "secondary" : "ghost"} onClick={() => { setScreenPath(file.path); setAnchor(undefined) }}>{file.path}</Button>)}{data.can_write && <Button className="ml-auto" variant={selecting ? "secondary" : "outline"} aria-pressed={selecting} onClick={() => setSelecting(!selecting)}>{selecting ? "Cancel selection" : "Select an element"}</Button>}</nav>
    <iframe ref={frame} key={`${manifest?.version}:${path}:${token}`} title="Artifact preview" data-screen={path} className="min-h-72 w-full flex-1 border-0 bg-white" sandbox="allow-scripts" referrerPolicy="no-referrer" srcDoc={source} />
   </section>
   <aside className="w-full overflow-auto border-t p-4 md:w-80 md:border-l md:border-t-0" aria-label="Comments">
    <h2 className="mb-3 font-semibold">Comments</h2>
    {data.can_write && <div className="mb-4 space-y-2">{anchor ? <blockquote className="rounded border p-2 text-sm">Version {anchor.version} · {anchor.path}<p>{anchor.quote || anchor.selector}</p></blockquote> : <p className="text-sm text-muted-foreground">Select an element in the preview to give the agent an exact location.</p>}<textarea aria-label="Comment for agent" value={draft} onChange={event => setDraft(event.target.value)} className="min-h-24 w-full rounded border bg-background p-2 text-sm" placeholder="What should change?" /><p className="text-xs text-muted-foreground">{storageError ? "Browser storage unavailable. Copy your draft before leaving." : "Your unsent comment is saved in this browser."}</p><Button disabled={!anchor || !draft.trim() || comment.isPending} onClick={send}>Send to agent</Button></div>}
    {data.comments.map(item => <article key={item.id} className="mb-3 rounded border p-3 text-sm"><p className="mb-1 text-xs text-muted-foreground">Version {item.version} · {item.path}</p><blockquote className="mb-2 border-l-2 pl-2">{item.quote}</blockquote><p>{item.text}</p>{item.task_id && <p className="mt-2 text-xs">{item.replies?.length ? "Answered" : delivery[item.id] ?? "Queued for agent"}</p>}{data.can_write && data.origin && item.task_id && !item.replies?.length && <Button size="sm" variant="ghost" disabled={!agent.ready} onClick={async () => { const accepted = await agent.dispatch(item); setDelivery(previous => ({...previous,[item.id]:accepted ? "Sent to agent" : "Comment saved. Agent delivery could not be confirmed."})) }}>Retry agent delivery</Button>}{item.replies?.map((reply, index) => <p key={index} className="mt-2 border-t pt-2">Version {reply.version}: {reply.text}</p>)}</article>)}
    {data.shared_version > 0 && <Link className="inline-flex items-center text-sm underline coarse:min-h-[44px]" href={`/pages/${encodeURIComponent(slug)}?artifact=1&version=${data.shared_version}&workspace_id=${encodeURIComponent(workspaceId)}`}>Open shared version {data.shared_version}</Link>}
   </aside>
  </div>
 </main>
}
