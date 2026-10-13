"use client"

import { useEffect, useId, useState } from "react"
import { ArtifactCommentItem } from "./artifact-comment-item"
import { Button } from "@/components/ui/button"
import { Textarea } from "@/components/ui/textarea"
import type { BoardThread, BoardAnchor, BoardCommentChange, BoardCommentHistory } from "@/hooks/use-board-threads"

export function BoardThreadCard({ thread, elementStatus, canComment, canResolve, pending, uncertainReplies, onViewAnchor, onReply, onResolve, onCommentChange, onCommentHistory, onDirtyChange }: {
  thread: BoardThread
  elementStatus?: "current" | "changed" | "deleted"
  canComment: boolean
  canResolve: boolean
  pending: boolean
  uncertainReplies: { base_version: number; body: string }[]
  onViewAnchor?: (anchor: BoardAnchor) => void
  onReply: (thread: BoardThread, body: string) => Promise<boolean>
  onResolve: (thread: BoardThread) => void
  onCommentChange: (thread: BoardThread, change: BoardCommentChange) => Promise<void>
  onCommentHistory: (threadId: string, commentId: string) => Promise<BoardCommentHistory[]>
  onDirtyChange: (id: string, dirty: boolean) => void
}) {
  const [reply, setReply] = useState("")
  const [baseVersion, setBaseVersion] = useState(thread.version)
  const id = useId()
  const dirty = reply.trim().length > 0
  const retryOriginal = uncertainReplies.some(input => input.base_version === baseVersion && input.body === reply)
  const versionChanged = dirty && baseVersion !== thread.version && !retryOriginal
  useEffect(() => {
    onDirtyChange(thread.id, dirty)
    return () => onDirtyChange(thread.id, false)
  }, [thread.id, dirty, onDirtyChange])
  useEffect(() => { if (!canComment) setReply("") }, [canComment])
  return <article className="space-y-3 rounded-lg border bg-background p-4" aria-label={`Board thread ${thread.id}`}>
    <div className="flex flex-wrap items-center gap-2">
      <span className="type-page-meta font-medium">{thread.anchor.type === "board-element" ? `${thread.anchor.snapshot?.element_type ?? "Element"} · original position (${Math.round(thread.anchor.snapshot?.x ?? 0)}, ${Math.round(thread.anchor.snapshot?.y ?? 0)})` : "Whole board"} · Board version {thread.anchor.board_seq}</span>
      <span className="type-page-meta rounded-full bg-muted px-2 py-0.5">{thread.status === "resolved" ? "Resolved" : "Open"}</span>
      {elementStatus === "changed" && <span className="text-xs text-muted-foreground">Element changed since this comment.</span>}
      {elementStatus === "deleted" && <span className="text-xs text-muted-foreground">Original element deleted. The comment retains its original position.</span>}
      {thread.anchor.type === "board-element" && onViewAnchor && <Button size="sm" variant="outline" onClick={() => onViewAnchor(thread.anchor)}>Show original position</Button>}
      {canResolve && <Button size="sm" variant="ghost" disabled={pending} className="coarse:h-12" onClick={() => onResolve(thread)}>{thread.status === "resolved" ? "Reopen thread" : "Resolve thread"}</Button>}
    </div>
    <ol className="space-y-3">
      {thread.comments.map(comment => <ArtifactCommentItem key={comment.id} comment={comment} thread={thread} canComment={canComment} pending={pending} onChange={onCommentChange} onHistory={onCommentHistory} onDirtyChange={onDirtyChange} />)}
    </ol>
    {canComment && dirty && thread.status === "resolved" && <div className="space-y-2 rounded-md border border-warn/30 p-3"><p className="text-xs text-warn">This thread was resolved while you were writing. Reopen it to continue, or discard your reply.</p><p className="whitespace-pre-wrap text-sm">{reply}</p><Button size="sm" variant="ghost" onClick={() => setReply("")}>Discard reply draft</Button></div>}
    {canComment && thread.status === "open" && <form className="space-y-2" onSubmit={async event => {
      event.preventDefault()
      if (await onReply({ ...thread, version: baseVersion }, reply)) { setReply(""); setBaseVersion(thread.version) }
    }}>
      <label htmlFor={id} className="sr-only">Reply to board thread {thread.id}</label>
      <Textarea id={id} value={reply} disabled={pending} maxLength={4096} placeholder="Reply to this thread…" className="min-h-20 text-sm" onChange={event => {
        if (!reply.trim()) setBaseVersion(thread.version)
        setReply(event.target.value)
      }} />
      {retryOriginal && <p className="text-xs text-muted-foreground">The response was not confirmed. Retry the original reply safely.</p>}
      {versionChanged && <div className="flex flex-wrap items-center gap-2"><p className="text-xs text-warn">Replies changed while you were writing. Review them before continuing.</p><Button type="button" variant="outline" size="sm" onClick={() => setBaseVersion(thread.version)}>Use latest thread version</Button></div>}
      <Button type="submit" size="sm" className="coarse:h-12" disabled={pending || !reply.trim() || versionChanged}>{retryOriginal ? "Retry reply" : "Reply"}</Button>
    </form>}
  </article>
}
