"use client"

import { useEffect, useState } from "react"
import Link from "next/link"
import { BookOpen, Brain, FileText, RefreshCw, ShieldCheck, Users } from "lucide-react"
import { WorkspaceEmpty, WorkspaceGlyph } from "./workspace-visuals"
import { apiFetch } from "@/lib/api-fetch"
import { Button } from "@/components/ui/button"
import { ConfirmDialog } from "@/components/ui/confirm-dialog"
import { useAbilities } from "@/hooks/use-abilities"
import { MemoryExportButton } from "./agent-canvas-tabs/memory-export-button"
import { cn } from "@/lib/utils"
import { invalidate, readThrough } from "@/lib/stale-cache"

export function useWorkspaceResource<T>(url: string, revision = 0, cache = false) {
  const [state, setState] = useState<{ key: string; data?: T; error?: string }>({ key: url })
  useEffect(() => {
    const controller = new AbortController()
    setState({ key: url })
    const fetchResource = async () => {
      const response = await apiFetch(url, cache ? undefined : { signal: controller.signal })
      if (!response.ok) throw new Error(`Could not load data (${response.status}).`)
      return await response.json() as T
    }
    if (cache && revision > 0) invalidate(`crew-preview:${url}`)
    const cached = cache ? readThrough(`crew-preview:${url}`, fetchResource) : null
    if (cached?.value !== undefined) setState({ key: url, data: cached.value })
    void (cached ? cached.fresh : fetchResource()).then(data => {
      if (!controller.signal.aborted) setState({ key: url, data })
    }).catch((error) => {
      if (!controller.signal.aborted) setState({ key: url, error: error instanceof Error ? error.message : "Could not load data." })
    })
    return () => controller.abort()
  }, [url, revision, cache])
  return state.key === url ? state : { key: url }
}

interface Document {
  id: string; name: string; scope: string; content?: string; state: string
  bytes: number | null; revision?: string; updated_at?: string; history_path?: string
}
interface Inventory { documents: Document[]; scopes: Record<string, string> }

export function MemoryWorkspace({ workspaceId, agentId, agentSlug, crewId, memoryEnabled }: {
  workspaceId: string; agentId?: string; agentSlug?: string; crewId?: string; memoryEnabled?: boolean
}) {
  const [section, setSection] = useState("knowledge")
  const [revision, setRevision] = useState(0)
  const [example, setExample] = useState(false)
  const [scope, setScope] = useState<string | null>(null)
  const [refreshed, setRefreshed] = useState(false)
  const [selected, setSelected] = useState<string | null>(null)
  const endpoint = agentId ? `agents/${agentId}` : `crews/${crewId}`
  const { data, error } = useWorkspaceResource<Inventory>(`/api/v1/${endpoint}/memory?workspace_id=${encodeURIComponent(workspaceId)}`, revision)
  const selectedScope = scope && data?.scopes[scope] ? scope : data ? Object.keys(data.scopes)[0] : null
  const scopeDocuments = data?.documents.filter(item => item.scope === selectedScope) ?? []
  const document = scopeDocuments.find((item) => item.id === selected) ?? scopeDocuments[0]
  const loading = !data && !error
  useEffect(() => { if (revision > 0 && (data || error)) setRefreshed(true) }, [revision, data, error])
  return <div className="space-y-5 max-w-6xl">
    <div className="flex items-start justify-between gap-4">
      <div><h2 className="text-lg font-medium">Memory</h2><p className="text-sm text-muted-foreground">Knowledge retained for future work, shared context, and your personal preferences.</p></div>
      <div className="flex flex-wrap justify-end gap-2">{crewId && section === "knowledge" && (selectedScope === "agent" || selectedScope === "crew") && <ScopeExport key={`${crewId}:${agentSlug ?? ""}:${selectedScope}`} crewId={crewId} agentSlug={selectedScope === "agent" ? agentSlug : undefined} workspaceId={workspaceId} />}<Button variant="outline" size="sm" disabled={loading} onClick={() => { setRefreshed(false); setRevision((n) => n + 1) }}><RefreshCw className={cn("h-4 w-4", loading && "animate-spin")} />{loading ? "Refreshing…" : "Refresh"}</Button></div>
    </div>
    {refreshed && !loading && section === "knowledge" && <p role="status" className="text-xs text-muted-foreground">{error ? "Refresh failed. Your saved notes are unchanged." : "Knowledge refreshed from storage."}</p>}
    {memoryEnabled === false && <p className="rounded-xl border border-border p-3 text-sm text-muted-foreground">Saved knowledge is not currently added to this agent&apos;s runs. Existing notes remain available here. Conversation history and episodic recall are separate.</p>}
    <div className="flex flex-wrap gap-2" aria-label="Memory views">
      {[['knowledge', 'Knowledge'], ['me', 'About me']].map(([id, label]) => <Button key={id} variant={section === id ? "soft" : "ghost"} onClick={() => setSection(id)} aria-pressed={section === id}>{id === "me" ? <ShieldCheck className="h-4 w-4" /> : <BookOpen className="h-4 w-4" />}{label}</Button>)}
      <Button variant="ghost" asChild><Link href="/inbox?kind=memory_consolidation">Knowledge proposals ↗</Link></Button>
    </div>
    {section === "me" ? <PersonalMemory key={workspaceId} workspaceId={workspaceId} refreshRevision={revision} /> : <>
      {error ? <p role="alert" className="text-sm text-destructive">{error} Use Refresh to try again.</p> : !data ? <p role="status" className="text-sm text-muted-foreground">Loading knowledge…</p> : <div className="space-y-4">
        <div className="grid gap-4 sm:grid-cols-2 xl:grid-cols-3">
          {Object.entries(data.scopes).map(([scopeName, state]) => <button key={scopeName} aria-pressed={selectedScope === scopeName} onClick={() => { setScope(scopeName); setSelected(null); setExample(false) }} className={cn("rounded-xl border border-border/60 bg-card p-5 text-left transition-colors hover:border-primary/40", selectedScope === scopeName && "border-primary/50 bg-primary/5")}>
            <div className="flex items-center gap-3"><WorkspaceGlyph icon={scopeName === "agent" ? Brain : Users} tone={scopeName === "agent" ? "purple" : "blue"} /><h3 className="font-medium text-sm">{scopeName === "agent" ? "Agent knowledge" : scopeName === "workspace" ? "Shared with workspace" : "Shared with crew"}</h3></div>
            <div aria-hidden="true" className="flex items-end gap-1.5 my-5"><span className="h-8 w-5 -rotate-6 rounded border border-purple/20 bg-purple/10" /><span className="h-10 w-6 rounded border border-purple/30 bg-purple/20" /><span className="h-8 w-5 rotate-6 rounded border border-purple/20 bg-purple/10" /></div>
            <p className="text-lg font-medium">{state === "unavailable" ? "Unavailable" : `${data.documents.filter(d => d.scope === scopeName).length} notes`}</p>
            <p className="text-xs text-muted-foreground mt-1">{scopeName === "agent" ? "Notes retained by this agent." : scopeName === "workspace" ? "Knowledge shared across crews." : "Available to agents working in this crew."}</p>
          </button>)}
        </div>
        {scopeDocuments.length > 1 && <div className="flex flex-wrap gap-2" aria-label="Saved notes">{scopeDocuments.map(note => <Button key={note.id} variant={document?.id === note.id ? "secondary" : "outline"} size="sm" onClick={() => setSelected(note.id)}><FileText className="h-3.5 w-3.5" />{note.name}</Button>)}</div>}
        <section className="min-w-0 rounded-2xl border border-border bg-card p-5">
          {document ? <><h3 className="font-medium break-words">{document.name}</h3><p className="text-xs text-muted-foreground mt-1">Current file · {document.scope === "crew" ? "Crew knowledge" : document.scope === "workspace" ? "Workspace knowledge" : "Agent knowledge"}{document.updated_at && ` · Updated ${new Date(document.updated_at).toLocaleString()}`}</p>
            {document.state !== "available" ? <p role="alert" className="mt-4">Current content is unavailable.</p> : <pre className="mt-5 whitespace-pre-wrap break-words text-sm font-sans leading-relaxed">{document.content || "This note is empty."}</pre>}
            {document.state === "available" && <Button className="mt-4" size="sm" variant="outline" onClick={() => {
              const url = URL.createObjectURL(new Blob([document.content ?? ""], { type: "text/markdown;charset=utf-8" }))
              const anchor = window.document.createElement("a"); anchor.href = url; anchor.download = document.name.split('/').pop() ?? "memory.md"; anchor.click(); setTimeout(() => URL.revokeObjectURL(url), 1000)
            }}>Download note</Button>}
            {document.history_path && <DocumentHistory key={`${document.id}:${revision}`} path={document.history_path} workspaceId={workspaceId} />}
          </> : selectedScope && data.scopes[selectedScope] === "unavailable" ? <WorkspaceEmpty icon={BookOpen} title="This knowledge could not be read." description="Try Refresh. Unavailable content does not mean there are no saved notes." /> : <><WorkspaceEmpty icon={BookOpen} title="No saved notes yet." description="Useful context can be retained as your agent works. Instructions and persona belong in Edit." /><div className="flex flex-wrap justify-center gap-2">{agentSlug && <Button asChild size="sm"><Link href={`/chat/${encodeURIComponent(agentSlug)}`}>Start a conversation</Link></Button>}<Button variant="outline" size="sm" onClick={() => setExample(v => !v)}>{example ? "Hide example" : "See an example"}</Button></div>{example && <div className="mt-5 rounded-xl border border-purple/20 bg-purple/5 p-4"><p className="text-xs uppercase tracking-wide text-purple">Example only · not saved</p><h4 className="mt-2 text-sm font-medium">Website delivery checklist</h4><p className="mt-2 text-sm text-muted-foreground">Check mobile navigation, readability and links. Include a short explanation of the changes when delivering the result.</p></div>}</>}
        </section>
      </div>}
    </>}
  </div>
}

function ScopeExport(props: { crewId: string; agentSlug?: string; workspaceId: string }) {
  const { role } = useAbilities()
  return role === "ADMIN" || role === "OWNER" ? <MemoryExportButton {...props} /> : null
}

function DocumentHistory({ path, workspaceId }: { path: string; workspaceId: string }) {
  const [open, setOpen] = useState(false)
  return <details className="mt-6 border-t border-border pt-4" onToggle={(event) => setOpen(event.currentTarget.open)}><summary className="cursor-pointer text-sm">Version history</summary>{open && <HistoryEntries path={path} workspaceId={workspaceId} />}</details>
}
function HistoryEntries({ path, workspaceId }: { path: string; workspaceId: string }) {
  const [version, setVersion] = useState<string | null>(null)
  const { data, error } = useWorkspaceResource<{ entries?: { id: string; written_at: string; written_by: string; sha256: string }[]; projection?: { state: string } }>(`/api/v1/memory/versions?workspace_id=${encodeURIComponent(workspaceId)}&path=${encodeURIComponent(path)}`)
  return <div className="mt-3 text-sm text-muted-foreground">{error ? <p role="alert">History could not be loaded. Current content is unaffected.</p> : !data ? "Loading history…" : data.projection?.state === "unavailable" ? "History is unavailable on this server. You are reading the current file above." : data.projection?.state === "unrecorded" ? "This kind of note does not have version history." : !data.entries?.length ? "No recorded versions. This does not mean the note was never written." : <ul className="space-y-2">{data.entries.map((entry) => <li key={entry.id}><button className="text-primary underline" onClick={() => setVersion(entry.sha256)}>{new Date(entry.written_at).toLocaleString()} · {entry.written_by}</button></li>)}</ul>}{version && <RecordedVersion key={version} sha={version} path={path} workspaceId={workspaceId} />}</div>
}

function RecordedVersion({ sha, path, workspaceId }: { sha: string; path: string; workspaceId: string }) {
  const [state, setState] = useState<{ content?: string; error?: boolean }>({})
  useEffect(() => {
    const controller = new AbortController()
    void apiFetch(`/api/v1/memory/versions/${encodeURIComponent(sha)}?workspace_id=${encodeURIComponent(workspaceId)}&path=${encodeURIComponent(path)}`, { signal: controller.signal }).then(async (r) => {
      if (!r.ok) throw new Error("Version unavailable")
      const content = await r.text(); if (!controller.signal.aborted) setState({ content })
    }).catch(() => { if (!controller.signal.aborted) setState({ error: true }) })
    return () => controller.abort()
  }, [sha, path, workspaceId])
  return <div className="mt-4 rounded-xl border border-border p-4"><p className="font-medium text-foreground">Recorded version</p><p className="text-xs mt-1">Historical snapshot. The current note remains above.</p>{state.error ? <p role="alert" className="mt-3">This version could not be read.</p> : state.content === undefined ? <p className="mt-3">Loading version…</p> : <pre className="mt-3 whitespace-pre-wrap break-words font-sans">{state.content || "This version is empty."}</pre>}</div>
}

interface MyModel { exists: boolean; content?: string; facts: { key: string; value: string }[] }
interface MyCards { peers: { id: string; agent_slug: string; content?: string }[] }
export function PersonalMemory({ workspaceId, refreshRevision = 0 }: { workspaceId: string; refreshRevision?: number }) {
  const [revision, setRevision] = useState(0)
  const [action, setAction] = useState<{ path: string; label: string; body?: object } | null>(null)
  const [error, setError] = useState<string | null>(null)
  const query = `?workspace_id=${encodeURIComponent(workspaceId)}`
  const model = useWorkspaceResource<MyModel>(`/api/v1/users/me/user-model${query}`, revision + refreshRevision)
  const cards = useWorkspaceResource<MyCards>(`/api/v1/users/me/peer-cards${query}`, revision + refreshRevision)
  const consent = useWorkspaceResource<{ opted_out: boolean }>(`/api/v1/users/me/peer-consent${query}`, revision + refreshRevision)
  async function perform() {
    if (!action) return
    const response = await apiFetch(`/api/v1/users/me/${action.path}${query}`, { method: action.body ? "PUT" : "DELETE", headers: { "Content-Type": "application/json" }, body: action.body ? JSON.stringify(action.body) : undefined })
    if (!response.ok) { setError("The change could not be saved. Your request can be retried."); throw new Error("Could not update personal memory") }
    setError(null); setAction(null); setRevision((n) => n + 1)
  }
  return <div className="space-y-4">
    {refreshRevision > 0 && <p role="status" className="text-xs text-muted-foreground">{model.error || cards.error || consent.error ? "Some personal data could not be refreshed." : model.data && cards.data && consent.data ? "Personal memory refreshed." : "Refreshing personal memory…"}</p>}
    <div className="rounded-2xl border border-border p-5"><div className="flex items-center gap-3"><WorkspaceGlyph icon={ShieldCheck} tone="purple" /><h3 className="font-medium">About me in this workspace</h3></div><p className="text-sm text-muted-foreground mt-1">Your retained preferences and notes from working with agents. This view uses your signed-in identity.</p>
      {consent.data && <div className="flex flex-wrap items-center gap-3 mt-4 text-sm"><span>Personalization {consent.data.opted_out ? "off" : "on"}</span><Button variant="outline" size="sm" onClick={() => setAction({ path: "peer-consent", label: consent.data!.opted_out ? "Enable personalization" : "Turn off personalization and forget saved profiles", body: { opted_out: !consent.data!.opted_out } })}>{consent.data.opted_out ? "Enable" : "Turn off and forget"}</Button></div>}
      {(model.error || cards.error || consent.error || error) && <p role="alert" className="mt-3 text-sm text-destructive">{error || "Some personal data could not be loaded."} <button className="underline" onClick={() => setRevision((n) => n + 1)}>Retry</button></p>}
    </div>
    <section className="rounded-2xl border border-border p-5"><h3 className="font-medium">Saved preferences</h3>{!model.data ? <p className="text-sm mt-3">{model.error ? "Unavailable" : "Loading…"}</p> : !model.data.exists ? <p className="text-sm text-muted-foreground mt-3">No saved preferences yet.</p> : <><ul className="divide-y divide-border mt-3">{(model.data.facts ?? []).map((fact) => <li key={fact.key} className="flex items-start justify-between gap-4 py-3"><div className="min-w-0"><div className="text-xs text-muted-foreground">{fact.key.replaceAll('_', ' ')}</div><p className="text-sm break-words">{fact.value}</p></div><Button size="sm" variant="ghost" onClick={() => setAction({ path: `user-model/facts/${encodeURIComponent(fact.key)}`, label: `Forget ${fact.key}` })}>Forget</Button></li>)}</ul>{model.data.content && !model.data.facts?.length && <pre className="whitespace-pre-wrap break-words text-sm my-3">{model.data.content}</pre>}<Button size="sm" variant="outline" onClick={() => setAction({ path: "user-model", label: "Forget all saved preferences" })}>Forget all preferences</Button><p className="mt-3 text-xs text-muted-foreground">Deleting a preference removes its saved value. Future conversations may teach it again while personalization is on. Original-message evidence is not yet recorded for these preferences.</p></>}</section>
    <section className="rounded-2xl border border-border p-5"><h3 className="font-medium">Notes about working with me</h3><p className="text-sm text-muted-foreground mt-2">Automatic agent-specific profile generation is not available in this release. Existing notes, if any, remain readable.</p>{cards.data?.peers?.map((card) => <div key={card.id} className="mt-4"><h4 className="text-sm">{card.agent_slug}</h4><pre className="mt-2 whitespace-pre-wrap break-words text-sm font-sans">{card.content ?? "Content unavailable"}</pre></div>)}{!!cards.data?.peers.length && <Button className="mt-4" size="sm" variant="outline" onClick={() => setAction({ path: "peer-cards", label: "Forget all agent notes about me" })}>Forget these notes</Button>}</section>
    <ConfirmDialog open={!!action} onOpenChange={(open) => { if (!open) setAction(null) }} title={action?.label ?? "Update personal memory"} description="This applies to your personal data in this workspace." confirmLabel="Confirm" onConfirm={perform} />
  </div>
}
