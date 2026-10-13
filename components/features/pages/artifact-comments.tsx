"use client"

import { useCallback, useEffect, useId, useState } from "react"
import { Button } from "@/components/ui/button"
import { Textarea } from "@/components/ui/textarea"
import { ArtifactCommentError, useArtifactThreads, type ArtifactThread } from "@/hooks/use-artifact-threads"
import { ArtifactThreadCard } from "./artifact-thread-card"

export function ArtifactComments({ workspaceId, slug, revision, onViewRevision, onDirtyChange }: {
  workspaceId: string
  slug: string
  revision: number
  onViewRevision: (revision: number) => void
  onDirtyChange: (dirty: boolean) => void
}) {
  const { query, write, uncertainReplies } = useArtifactThreads(workspaceId, slug)
  const [body, setBody] = useState("")
  const [draftRevision, setDraftRevision] = useState<number | null>(null)
  const [replyDrafts, setReplyDrafts] = useState<Record<string, boolean>>({})
  const composerId = useId()
  const dirty = body.trim().length > 0 || Object.values(replyDrafts).some(Boolean)
  useEffect(() => {
    onDirtyChange(dirty)
    return () => onDirtyChange(false)
  }, [dirty, onDirtyChange])
  const markReplyDirty = useCallback((id: string, value: boolean) => {
    setReplyDrafts(old => old[id] === value ? old : { ...old, [id]: value })
  }, [])
  const data = query.data
  const accessDenied = [query.error, write.error].some(error => error instanceof ArtifactCommentError && [401, 403, 404].includes(error.status))
  useEffect(() => {
    if (accessDenied || data?.can_comment === false) { setBody(""); setDraftRevision(null); setReplyDrafts({}) }
  }, [accessDenied, data?.can_comment])
  async function submitReply(thread: ArtifactThread, text: string) {
    try { await write.mutateAsync({ action: "reply", threadId: thread.id, base_version: thread.version, body: text }); return true }
    catch { return false }
  }
  async function changeResolution(thread: ArtifactThread) {
    try { await write.mutateAsync({ action: "resolve", threadId: thread.id, base_version: thread.version, resolved: thread.status !== "resolved" }) }
    catch { /* Keep the failure visible without changing the thread locally. */ }
  }
  if (accessDenied || (query.error && !data)) return <div role="alert" className="space-y-2 border-t p-4 text-sm text-destructive">
    <p>{accessDenied ? "Your access to comments changed. Comments and drafts are hidden." : query.error?.message}</p>
    <Button size="sm" variant="outline" onClick={async () => { const refreshed = await query.refetch(); if (!refreshed.error) write.reset() }}>Retry comments</Button>
  </div>
  if (!data) return <p role="status" className="border-t p-4 text-sm text-muted-foreground">Loading comments…</p>
  const current = data.threads.filter(thread => thread.revision === revision)
  const previous = data.threads.filter(thread => thread.revision !== revision)
  const ordered = [...current, ...previous]
  const renderThread = (thread: ArtifactThread) => <ArtifactThreadCard
    key={thread.id} thread={thread} currentRevision={revision}
    uncertainReplies={uncertainReplies.filter(input => input.threadId === thread.id)}
    canComment={data.can_comment} canResolve={data.can_resolve} pending={write.isPending}
    onViewRevision={onViewRevision} onReply={submitReply}
    onResolve={item => void changeResolution(item)} onDirtyChange={markReplyDirty}
  />
  return <section aria-label="Artifact comments" className="space-y-4 border-t p-4 md:p-6">
    <div className="flex flex-wrap items-center gap-2">
      <div className="mr-auto"><h2 className="text-sm font-semibold">Comments</h2><p className="mt-1 text-xs text-muted-foreground">Threads belong to their original revision. Comments do not run agents.</p></div>
      <Button size="sm" variant="ghost" className="coarse:h-12" disabled={query.isFetching || write.isPending} onClick={() => { write.reset(); void query.refetch() }}>Refresh comments</Button>
    </div>
    {query.error && <p role="alert" className="rounded-md border border-warn/30 p-3 text-sm text-warn">Comments could not refresh. Showing the last authorized snapshot; drafts are preserved. {query.error.message}</p>}
    {write.error && <p role="alert" className="rounded-md border border-destructive/30 p-3 text-sm text-destructive">{write.error.message}</p>}
    <div className="grid gap-6 xl:grid-cols-[minmax(0,1fr)_minmax(260px,360px)]">
      <div className="min-w-0 space-y-4">
        {!current.length && <div className="space-y-3"><h3 className="type-page-meta font-medium">Revision {revision} · 0 threads</h3><p className="rounded-lg border border-dashed p-4 text-sm text-muted-foreground">No comments on this revision yet.</p></div>}
        {ordered.map(thread => <div key={thread.id} className="space-y-3">
          {thread.id === current[0]?.id && <h3 className="type-page-meta font-medium">Revision {revision} · {current.length} {current.length === 1 ? "thread" : "threads"}</h3>}
          {thread.id === previous[0]?.id && <h3 className="type-page-meta font-medium">Other revisions · Anchors stay with the original content</h3>}
          {renderThread(thread)}
        </div>)}
      </div>
      <div className="min-w-0">
        {data.can_comment ? <form className="space-y-3 rounded-lg border bg-muted/20 p-4" onSubmit={async event => {
          event.preventDefault()
          try {
            await write.mutateAsync({ action: "create", revision: draftRevision ?? revision, body })
            setBody(""); setDraftRevision(null)
          } catch { /* Preserve private draft in memory for an authorized retry. */ }
        }}>
          <label htmlFor={composerId} className="text-sm font-medium">New thread</label>
          <p className="text-xs text-muted-foreground">Revision {draftRevision ?? revision} · Whole document</p>
          {draftRevision !== null && draftRevision !== revision && <p className="text-xs text-warn">This draft stays anchored to revision {draftRevision}. Switching the preview does not move it.</p>}
          <Textarea id={composerId} disabled={write.isPending} value={body} maxLength={4096} placeholder="What should change or be discussed?" className="min-h-32 text-sm" onChange={event => {
            if (!body.trim()) setDraftRevision(revision)
            setBody(event.target.value)
          }} />
          <p className="text-xs text-muted-foreground">Plain text · Up to 4 KiB · Visible to people who can read this page.</p>
          <Button type="submit" className="coarse:h-12" disabled={write.isPending || !body.trim()}>{write.isPending ? "Saving…" : "Post comment"}</Button>
        </form> : <p className="rounded-lg border bg-muted/20 p-4 text-sm text-muted-foreground">You can read these threads. Editing permission is required to comment or resolve them.</p>}
      </div>
    </div>
  </section>
}
