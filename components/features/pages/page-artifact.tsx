"use client"

import { useEffect, useRef, useState } from "react"
import { Button } from "@/components/ui/button"
import { Textarea } from "@/components/ui/textarea"
import { usePageArtifact, type ArtifactMutation, PageArtifactError } from "@/hooks/use-page-artifact"
import { useStaticArtifactDocument } from "@/hooks/use-static-artifact-document"

const MAX_BYTES = 10 * 1024 * 1024
export function PageArtifactView({ workspaceId, slug, pageId, onDirtyChange }: {
  workspaceId: string; slug: string; pageId: string; onDirtyChange: (dirty: boolean) => void
}) {
  // Deliberately retain the identity of an open editor across detail refetches.
  // A recreated slug cannot inherit this editor's draft or retry receipt.
  const originalPage = useRef(pageId)
  const currentPage = useRef(pageId)
  currentPage.current = pageId
  const [revision, setRevision] = useState<number>()
  const work = usePageArtifact(workspaceId, slug, originalPage.current, revision)
  const [view, setView] = useState<"preview" | "source" | "history">("preview")
  const [draft, setDraft] = useState<{ html: string; base: number } | null>(null)
  const [attempt, setAttempt] = useState<{ payload: Readonly<ArtifactMutation>; restore: boolean } | null>(null)
  const [localError, setLocalError] = useState<string | null>(null)
  const data = work.data
  const identityChanged = pageId !== originalPage.current
  const denied = work.error instanceof PageArtifactError && [401, 403, 404, 409].includes(work.error.status)
  const document = useStaticArtifactDocument(!denied && !identityChanged ? data?.html : undefined)
  useEffect(() => {
    onDirtyChange(draft !== null || attempt !== null)
    return () => onDirtyChange(false)
  }, [draft, attempt, onDirtyChange])
  async function send(frozen: { payload: Readonly<ArtifactMutation>; restore: boolean }) {
    if (identityChanged || work.busy) return
    setAttempt(frozen); setLocalError(null)
    try {
      await work.mutate(frozen.payload, frozen.restore)
      setAttempt(null); setDraft(null); setRevision(undefined); setView("preview")
      await work.refresh()
    } catch { /* Keep the exact payload/receipt for explicit retry. */ }
  }
  function save() {
    if (!data?.can_write || !draft || attempt) return
    if (!draft.html.trim() || new TextEncoder().encode(draft.html).length > MAX_BYTES) { setLocalError("HTML must be nonempty UTF-8 and at most 10 MiB."); return }
    void send({ payload: Object.freeze({ page_id: originalPage.current, base_rev: draft.base, mutation_id: crypto.randomUUID(), html: draft.html }), restore: false })
  }
  async function upload(file?: File) {
    if (!file || !data?.can_write || attempt || identityChanged) return
    const captured = { page: pageId, base: data.head_rev }
    if (file.size > MAX_BYTES) { setLocalError("Choose an HTML file of at most 10 MiB."); return }
    try {
      const html = await file.text()
      if (captured.page !== originalPage.current || captured.page !== currentPage.current) return
      setDraft({ html, base: captured.base }); setView("source"); setLocalError(null)
    } catch { setLocalError("Could not read this file.") }
  }
  const discard = () => { setDraft(null); setAttempt(null); setLocalError(null); void work.refresh() }
  return <section aria-label="HTML artifact" className="space-y-3 overflow-auto p-4">
    <h2 className="text-lg font-semibold">HTML artifact</h2>
    <p className="text-sm text-muted-foreground">Static preview. Scripts, navigation and external resources are disabled.</p>
    {identityChanged && <p role="alert">This slug now identifies another Page. This editor cannot save to it. Leave and reopen the Page after discarding your draft.</p>}
    {(localError || work.error) && <p role="alert" className="text-sm text-destructive">{localError ?? work.error?.message}</p>}
    <div className="flex flex-wrap gap-2">
      {(["preview", "source", "history"] as const).map(tab => <Button key={tab} variant={view === tab ? "secondary" : "outline"} className="coarse:min-h-12" onClick={() => setView(tab)}>{tab === "preview" ? "Static preview" : tab === "source" ? "Source" : "History"}</Button>)}
      <Button variant="outline" className="coarse:min-h-12" onClick={() => void work.refresh()} disabled={work.busy}>Refresh</Button>
      {data?.can_write && !identityChanged && <label className="inline-flex min-h-9 items-center rounded-md border px-3 text-sm coarse:min-h-12">Upload HTML<input aria-label="Upload HTML" type="file" accept=".html,.htm,text/html" className="sr-only" disabled={work.busy || attempt !== null} onChange={event => { void upload(event.target.files?.[0]); event.target.value = "" }} /></label>}
    </div>
    {attempt && <div className="space-x-2 rounded border p-3"><p>{work.error instanceof PageArtifactError && work.error.status < 500 ? "The save was refused. Review the latest version before saving again." : "The save may have completed. Retrying sends the same change. If the Page changed, review the latest version before saving again."}</p><Button className="coarse:min-h-12" disabled={work.busy || identityChanged || denied} onClick={() => void send(attempt)}>Retry exact save</Button><Button className="coarse:min-h-12" variant="outline" disabled={work.busy} onClick={discard}>Discard attempt</Button></div>}
    {draft && <div className="space-y-2"><label htmlFor="artifact-source">HTML for the next revision (base {draft.base})</label><Textarea id="artifact-source" value={draft.html} disabled={work.busy || attempt !== null} onChange={event => setDraft({ ...draft, html: event.target.value })} className="min-h-64 font-mono" /><div className="flex gap-2"><Button className="coarse:min-h-12" disabled={work.busy || attempt !== null || identityChanged || !data?.can_write} onClick={save}>Save revision</Button><Button className="coarse:min-h-12" variant="outline" disabled={work.busy} onClick={discard}>Discard draft</Button></div></div>}
    {!data && !work.error && <p role="status">Loading artifact…</p>}
    {data && !denied && !identityChanged && <>
      <p>Revision {data.revision} · latest {data.head_rev}{!data.can_write && " · read only"}</p>
      {view === "preview" && (data.head_rev ? <iframe title="Artifact static preview" sandbox="" referrerPolicy="no-referrer" srcDoc={document} className="h-[60vh] min-h-80 w-full border bg-white" /> : <p>No HTML saved yet. Upload HTML or open Source to start.</p>)}
      {view === "source" && <><pre className="max-h-[60vh] overflow-auto whitespace-pre-wrap rounded border p-3 text-xs">{data.html || "No source saved yet."}</pre>{data.can_write && !draft && <Button className="coarse:min-h-12" disabled={work.busy || attempt !== null} onClick={() => setDraft({ html: data.html, base: data.head_rev })}>Edit as new revision</Button>}</>}
      {view === "history" && <ul className="space-y-2">{data.revisions.map(item => <li key={item.revision} className="flex flex-wrap items-center gap-2"><span>Revision {item.revision}{item.restored_from ? ` · restored from ${item.restored_from}` : ""}</span><Button variant="outline" className="coarse:min-h-12" disabled={work.busy} onClick={() => { setRevision(item.revision); setView("preview") }}>View revision {item.revision}</Button>{data.can_write && <Button variant="outline" className="coarse:min-h-12" disabled={work.busy || draft !== null || attempt !== null} onClick={() => void send({ payload: Object.freeze({ page_id: originalPage.current, base_rev: data.head_rev, mutation_id: crypto.randomUUID(), revision: item.revision }), restore: true })}>Restore revision {item.revision}</Button>}</li>)}</ul>}
    </>}
  </section>
}
