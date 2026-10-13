"use client"

import { useEffect, useId, useState } from "react"
import { ArtifactSendButton, ArtifactThreadRequests } from "./artifact-agent-work"
import { Button } from "@/components/ui/button"
import { Textarea } from "@/components/ui/textarea"
import type { ArtifactThread, ArtifactAnchor } from "@/hooks/use-artifact-threads"

export function ArtifactThreadCard({ thread, currentRevision, canComment, canResolve, pending, uncertainReplies, onViewRevision, onViewAnchor, onReply, onResolve, onDirtyChange }: {
  thread: ArtifactThread
  currentRevision: number
  canComment: boolean
  canResolve: boolean
  pending: boolean
  uncertainReplies: { base_version: number; body: string }[]
  onViewRevision: (revision: number) => void
  onViewAnchor?: (revision: number, anchor: ArtifactAnchor) => void
  onReply: (thread: ArtifactThread, body: string) => Promise<boolean>
  onResolve: (thread: ArtifactThread) => void
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
  return <article className="space-y-3 rounded-lg border bg-background p-4" aria-label={`Thread on revision ${thread.revision}`}>
    <div className="flex flex-wrap items-center gap-2">
      <span className="type-page-meta font-medium">Revision {thread.revision} · {thread.anchor.type === "point" ? `Viewport point (${Math.round(thread.anchor.x)}, ${Math.round(thread.anchor.y)})` : "Whole document"}</span>
      <span className="type-page-meta rounded-full bg-muted px-2 py-0.5">{thread.status === "resolved" ? "Resolved" : "Open"}</span>
      {thread.revision !== currentRevision && <Button size="sm" variant="ghost" className="ml-auto coarse:h-12" onClick={() => onViewRevision(thread.revision)}>View original revision {thread.revision}</Button>}
      {thread.anchor.type === "point" && onViewAnchor && <Button size="sm" variant="outline" onClick={() => onViewAnchor(thread.revision, thread.anchor)}>Show comment point</Button>}
      {canResolve && <Button size="sm" variant="ghost" disabled={pending} className="coarse:h-12" onClick={() => onResolve(thread)}>{thread.status === "resolved" ? "Reopen thread" : "Resolve thread"}</Button>}
    </div>
    <ArtifactSendButton thread={thread} />
    <ol className="space-y-3">
      {thread.comments.map(comment => <li key={comment.id} className="space-y-1">
        <div className="flex flex-wrap gap-x-2 text-xs text-muted-foreground"><span title={comment.author_user_id}>{comment.author_name || "Workspace member"}</span><time dateTime={comment.created_at}>{new Date(comment.created_at).toLocaleString()}</time></div>
        <p className="whitespace-pre-wrap break-words text-sm">{comment.body}</p>
      </li>)}
    </ol>
    {canComment && dirty && thread.status === "resolved" && <div className="space-y-2 rounded-md border border-warn/30 p-3"><p className="text-xs text-warn">This thread was resolved while you were writing. Reopen it to continue, or discard your reply.</p><p className="whitespace-pre-wrap text-sm">{reply}</p><Button size="sm" variant="ghost" onClick={() => setReply("")}>Discard reply draft</Button></div>}
    {canComment && thread.status === "open" && <form className="space-y-2" onSubmit={async event => {
      event.preventDefault()
      if (await onReply({ ...thread, version: baseVersion }, reply)) { setReply(""); setBaseVersion(thread.version) }
    }}>
      <label htmlFor={id} className="sr-only">Reply to thread on revision {thread.revision}</label>
      <Textarea id={id} value={reply} disabled={pending} maxLength={4096} placeholder="Reply to this thread…" className="min-h-20 text-sm" onChange={event => {
        if (!reply.trim()) setBaseVersion(thread.version)
        setReply(event.target.value)
      }} />
      {retryOriginal && <p className="text-xs text-muted-foreground">The response was not confirmed. Retry the original reply safely.</p>}
      {versionChanged && <div className="flex flex-wrap items-center gap-2"><p className="text-xs text-warn">Replies changed while you were writing. Review them before continuing.</p><Button type="button" variant="outline" size="sm" onClick={() => setBaseVersion(thread.version)}>Use latest thread version</Button></div>}
      <Button type="submit" size="sm" className="coarse:h-12" disabled={pending || !reply.trim() || versionChanged}>{retryOriginal ? "Retry reply" : "Reply"}</Button>
    </form>}
    <ArtifactThreadRequests threadId={thread.id} />
  </article>
}
