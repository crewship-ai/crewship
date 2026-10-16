"use client"

import { useEffect, useState } from "react"
import { SlideComments } from "@/components/features/slides/slide-comments"
import { SlidesEditor } from "@/components/features/slides/slides-editor"
import { slidesRequest, usePageSlides } from "@/hooks/use-page-slides"
import type { SlidesState } from "@/lib/slides/mutations"

interface SlideRevision { revision: number; created_at: string; kind: string; actor: string }
export function PageSlides({ workspaceId, slug, pageId, onDirtyChange }: { workspaceId: string; slug: string; pageId: string; onDirtyChange?: (dirty: boolean) => void }) {
  const sync = usePageSlides(workspaceId, slug, pageId)
  const [commentDirty, setCommentDirty] = useState(false)
  const [history, setHistory] = useState<SlideRevision[]>(), [preview, setPreview] = useState<SlidesState>(), [historyError, setHistoryError] = useState<string>()
  useEffect(() => { onDirtyChange?.(sync.dirty || sync.retry || commentDirty); return () => onDirtyChange?.(false) }, [sync.dirty, sync.retry, commentDirty, onDirtyChange])
  useEffect(() => {
    if (!sync.dirty && !sync.retry && !commentDirty) return
    const warn = (event: BeforeUnloadEvent) => { event.preventDefault(); event.returnValue = "" }
    window.addEventListener("beforeunload", warn); return () => window.removeEventListener("beforeunload", warn)
  }, [sync.dirty, sync.retry, commentDirty])
  async function loadHistory() {
    setHistoryError(undefined)
    try {
      const value = await slidesRequest<SlideRevision[]>(workspaceId, slug, "/history")
      if (!Array.isArray(value) || value.some(item => !Number.isSafeInteger(item.revision) || item.revision < 1 || typeof item.created_at !== "string" || typeof item.kind !== "string" || typeof item.actor !== "string")) throw new Error("Unsupported Slides history format.")
      setHistory(value)
    } catch (cause) { setHistoryError(cause instanceof Error ? cause.message : "Could not load history.") }
  }
  async function viewRevision(revision: number) { try { setPreview(await sync.read(`/revisions/${revision}`)); setHistoryError(undefined) } catch (cause) { setHistoryError(cause instanceof Error ? cause.message : "Could not read revision.") } }
  function downloadDraft() {
    const url = URL.createObjectURL(new Blob([JSON.stringify(sync.draft, null, 2)], { type: "application/json" }))
    const link = document.createElement("a"); link.href = url; link.download = `${slug}-unsaved-slides.json`; link.click(); URL.revokeObjectURL(url)
  }
  const controls = "min-h-9 coarse:min-h-12 rounded border px-3 disabled:opacity-50"
  return <section aria-label="Slides Page" className="space-y-4 p-4">
    <div className="flex flex-wrap gap-2">
      <button className={controls} disabled={!sync.canWrite || sync.busy || sync.blocked || (!sync.dirty && !sync.retry)} onClick={() => void sync.save()}>{sync.busy ? "Saving…" : sync.retry ? "Retry original save" : "Save changes"}</button>
      <button className={controls} disabled={sync.busy} onClick={() => { if ((sync.dirty || sync.retry) && !window.confirm("Discard the retained draft and original save attempt, then reopen saved Slides?")) return; setPreview(undefined); void sync.reopen() }}>{sync.dirty || sync.retry ? "Discard draft and reopen saved Slides" : "Reopen saved Slides"}</button>
      <button className={controls} disabled={sync.busy || !sync.base} onClick={() => void loadHistory()}>History</button>
      {(sync.dirty || sync.retry) && <button className={controls} onClick={downloadDraft}>Download unsaved draft</button>}
    </div>
    {sync.error && <p role="alert">{sync.error}</p>}
    {sync.orderOnly && <p role="status">Save or discard this reorder before editing content.</p>}
    {sync.dirty && !sync.orderOnly && <p className="text-sm">Save or discard content changes before reordering slides.</p>}
    {!sync.draft ? <p role="status">{sync.status}</p> : <SlidesEditor key={`${sync.base?.deck.deck_id}:${sync.base?.revision}`} deck={sync.draft} onChange={sync.change} readOnly={!sync.canWrite || sync.busy || sync.blocked || sync.retry || sync.orderOnly} reorderDisabled={sync.dirty} persistenceStatus={sync.status} commentsAvailable />}
    {sync.base && <SlideComments key={`${workspaceId}:${pageId}`} workspaceId={workspaceId} slug={slug} pageId={pageId} saved={sync.base} contentPending={sync.dirty || sync.retry} onDirtyChange={setCommentDirty} />}
    {historyError && <p role="alert">{historyError}</p>}
    {history && <section aria-label="Slides history" className="space-y-2"><h3 className="font-medium">Saved revisions</h3>{history.map(item => <div key={item.revision} className="flex flex-wrap items-center gap-2"><span>Revision {item.revision} · {item.kind} · {item.created_at}</span><button className={controls} onClick={() => void viewRevision(item.revision)}>View revision {item.revision}</button><button className={controls} disabled={!sync.canWrite || sync.busy || sync.blocked || sync.dirty || sync.retry} onClick={() => { setPreview(undefined); void sync.restore(item.revision) }}>Restore revision {item.revision}</button></div>)}</section>}
    {preview && <section aria-label="Saved Slides revision" className="rounded border p-3"><h3>Saved revision {preview.revision}</h3><button className={controls} onClick={() => setPreview(undefined)}>Close revision preview</button><SlidesEditor key={`${preview.deck.deck_id}:${preview.revision}`} deck={preview.deck} readOnly persistenceStatus={`Saved revision ${preview.revision}; read only`} /></section>}
  </section>
}
