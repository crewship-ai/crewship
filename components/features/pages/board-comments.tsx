"use client"

import { useCallback, useEffect, useId, useState } from "react"
import { Button } from "@/components/ui/button"
import { Textarea } from "@/components/ui/textarea"
import { BoardCommentError, useBoardThreads, type BoardThread, type BoardAnchor } from "@/hooks/use-board-threads"
import type { PageBoard } from "@/hooks/use-page-board"
import { BoardThreadCard } from "./board-thread-card"

export function BoardComments({ workspaceId, slug, board, selectedElementId, onDirtyChange, onViewAnchor }: {
  workspaceId: string; slug: string; board: PageBoard; selectedElementId?: string
  onDirtyChange: (dirty: boolean) => void
  onViewAnchor?: (anchor: BoardAnchor) => void
}) {
  const [discussionStatus, setDiscussionStatus] = useState<"all" | "open" | "resolved">("all")
  const { query, write, history, uncertainReplies, uncertainCreate } = useBoardThreads(workspaceId, slug, discussionStatus)
  const [body, setBody] = useState("")
  const [draftAnchor, setDraftAnchor] = useState<BoardAnchor | null>(null)
  const [replyDrafts, setReplyDrafts] = useState<Record<string, boolean>>({})
  const [expanded, setExpanded] = useState(true)
  const composerId = useId()
  const panelId = useId()
  const dirty = body.trim().length > 0 || Object.values(replyDrafts).some(Boolean)
  useEffect(() => { onDirtyChange(dirty); return () => onDirtyChange(false) }, [dirty, onDirtyChange])
  const markReplyDirty = useCallback((id: string, value: boolean) => {
    setReplyDrafts(old => old[id] === value ? old : { ...old, [id]: value })
  }, [])
  const selected = board.records.find(record => record.element_id === selectedElementId && !record.deleted)
  const currentAnchor: BoardAnchor = selected
    ? { schema_version: 1, type: "board-element", board_seq: board.seq, element_id: selected.element_id, element_rev: selected.element_rev }
    : { schema_version: 1, type: "board", board_seq: board.seq }
  const anchor = draftAnchor ?? currentAnchor
  const data = query.data
  const accessDenied = [query.error, write.error].some(error => error instanceof BoardCommentError && [401, 403, 404].includes(error.status))
  useEffect(() => {
    if (accessDenied || data?.can_comment === false) { setBody(""); setDraftAnchor(null); setReplyDrafts({}) }
  }, [accessDenied, data?.can_comment])
  async function reply(thread: BoardThread, text: string) {
    try { await write.mutateAsync({ action: "reply", threadId: thread.id, base_version: thread.version, body: text }); return true }
    catch { return false }
  }
  if (accessDenied || (query.error && !data)) return <div role="alert" className="space-y-2 border-t p-4 text-sm text-destructive">
    <p>{accessDenied ? "Your access to board comments changed. Comments and drafts are hidden." : query.error?.message}</p>
    <Button size="sm" variant="outline" onClick={async () => { const refreshed = await query.refetch(); if (!refreshed.error) write.reset() }}>Retry comments</Button>
  </div>
  if (!data) return <p role="status" className="border-t p-4 text-sm text-muted-foreground">Loading board comments…</p>
  return <section aria-label="Board comments" className="space-y-4 border-t p-4 md:p-6">
    <div className="flex items-center gap-2 px-4 py-2">
      <label className="text-sm">Discussions <select aria-label="Discussion status" disabled={dirty} title={dirty ? "Finish or clear your draft before changing discussion filters." : undefined} value={discussionStatus} onChange={event => setDiscussionStatus(event.target.value as "all" | "open" | "resolved")} className="ml-2 rounded border bg-background p-2 coarse:h-12">
        <option value="all">All</option><option value="open">Open</option><option value="resolved">Resolved</option>
      </select></label>
      {query.hasNextPage && <Button variant="outline" size="sm" disabled={query.isFetchingNextPage} onClick={() => void query.fetchNextPage()}>{query.isFetchingNextPage ? "Loading…" : "Load older discussions"}</Button>}
    </div>
    {dirty && <p className="px-4 text-xs text-muted-foreground">Finish or clear your draft before changing discussion filters.</p>}
    <div className="flex flex-wrap items-center gap-2">
      <div className="mr-auto"><h2 className="text-sm font-semibold">Comments</h2><p className="mt-1 text-xs text-muted-foreground">Select one saved drawing to discuss it, or comment on the whole board.</p></div>
      <Button size="sm" variant="ghost" className="coarse:h-12" aria-expanded={expanded} aria-controls={panelId} onClick={() => setExpanded(value => !value)}>{expanded ? "Hide comments" : `Show comments (${data.threads.length})`}{!expanded && dirty ? " · Draft" : ""}</Button>
      <Button size="sm" variant="ghost" className="coarse:h-12" disabled={query.isFetching || write.isPending} onClick={() => { write.reset(); void query.refetch() }}>Refresh comments</Button>
    </div>
    <div id={panelId} hidden={!expanded} className="space-y-4">
    {query.error && <p role="alert" className="text-sm text-warn">{query.isFetchNextPageError ? "Older discussions could not load. Retry loading older discussions." : "Comments could not refresh."} Drafts are preserved. {query.error.message}</p>}
    {write.error && <p role="alert" className="text-sm text-destructive">{write.error.message}</p>}
    <div className="grid gap-6 xl:grid-cols-[minmax(0,1fr)_minmax(260px,360px)]">
      <div className="min-w-0 space-y-4">
        {!data.threads.length && <p className="rounded-lg border border-dashed p-4 text-sm text-muted-foreground">No comments yet.</p>}
        {data.threads.map(thread => {
          const target = thread.anchor
          const original = target.type === "board-element" ? board.records.find(record => record.element_id === target.element_id) : undefined
          const elementStatus = thread.anchor.type !== "board-element" ? undefined : !original || original.deleted ? "deleted" : original.element_rev === thread.anchor.element_rev ? "current" : "changed"
          return <BoardThreadCard key={thread.id} thread={thread} elementStatus={elementStatus}
            uncertainReplies={uncertainReplies.filter(input => input.threadId === thread.id)}
            canComment={data.can_comment} canResolve={data.can_resolve} pending={write.isPending}
            onViewAnchor={onViewAnchor} onReply={reply} onDirtyChange={markReplyDirty}
            onCommentHistory={history}
            onCommentChange={async (item, change) => { await write.mutateAsync({ ...change, threadId: item.id, base_version: item.version }) }}
            onResolve={async item => { try { await write.mutateAsync({ action: "resolve", threadId: item.id, base_version: item.version, resolved: item.status !== "resolved" }) } catch { /* Keep error visible. */ } }} />
        })}
      </div>
      {data.can_comment ? <form className="min-w-0 space-y-3 self-start rounded-lg border bg-muted/20 p-4" onSubmit={async event => {
        event.preventDefault()
        try { await write.mutateAsync(uncertainCreate ?? { action: "create", anchor, body }); setBody(""); setDraftAnchor(null) }
        catch { /* Original anchor and body remain available for an exact retry. */ }
      }}>
        <label htmlFor={composerId} className="text-sm font-medium">New board thread</label>
        <p className="text-xs text-muted-foreground">{anchor.type === "board-element" ? "Selected drawing" : "Whole board"} · Board version {anchor.board_seq}</p>
        {selectedElementId && !selected && !draftAnchor && <p className="text-xs text-warn">This selection is not a saved drawing. Save it first to attach a comment.</p>}
        {draftAnchor && draftAnchor.board_seq !== board.seq && <p className="text-xs text-warn">The board changed while you were writing. Review it before choosing a new comment anchor.</p>}
        {uncertainCreate && <p role="status" className="text-xs text-warn">The last response was lost. Post again to confirm the original comment before changing its text or anchor.</p>}
        <Textarea id={composerId} disabled={write.isPending || Boolean(uncertainCreate)} value={body} maxLength={4096} placeholder="What should change or be discussed?" className="min-h-32 text-sm" onChange={event => { if (!draftAnchor) setDraftAnchor(currentAnchor); setBody(event.target.value) }} />
        {draftAnchor && <Button type="button" size="sm" variant="outline" disabled={write.isPending || Boolean(uncertainCreate)} onClick={() => setDraftAnchor(currentAnchor)}>Use current selection and board version</Button>}
        <p className="text-xs text-muted-foreground">Comments are visible to page readers. Posting a comment does not start or authorize an agent.</p>
        <Button type="submit" className="coarse:h-12" disabled={write.isPending || !body.trim()}>{write.isPending ? "Saving…" : "Post comment"}</Button>
      </form> : <p className="text-sm text-muted-foreground">Comment permission is required to reply; editors can resolve threads.</p>}
    </div>
    </div>
  </section>
}
