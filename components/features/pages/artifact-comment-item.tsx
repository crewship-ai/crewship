"use client"

import { useEffect, useRef, useState } from "react"
import { Button } from "@/components/ui/button"
import { Textarea } from "@/components/ui/textarea"
import { type ArtifactComment, type ArtifactCommentChange, type ArtifactCommentHistory, type ArtifactThread } from "@/hooks/use-artifact-threads"

export function ArtifactCommentItem<T extends Pick<ArtifactThread, "id" | "version">>({ comment, thread, canComment, pending, onChange, onHistory, onDirtyChange }: {
  comment: ArtifactComment; thread: T; canComment: boolean; pending: boolean
  onChange: (thread: T, change: ArtifactCommentChange) => Promise<void>
  onHistory: (threadId: string, commentId: string) => Promise<ArtifactCommentHistory[]>
  onDirtyChange: (id: string, dirty: boolean) => void
}) {
  const [editing, setEditing] = useState(false)
  const [body, setBody] = useState(comment.body)
  const [base, setBase] = useState(thread.version)
  const [removing, setRemoving] = useState(false)
  const [uncertain, setUncertain] = useState<{ change: ArtifactCommentChange; version: number } | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [history, setHistory] = useState<ArtifactCommentHistory[] | null>(null)
  const [loadingHistory, setLoadingHistory] = useState(false)
  const epoch = useRef(0)
  useEffect(() => () => { epoch.current++ }, [])
  useEffect(() => {
    if (!canComment) { epoch.current++; setEditing(false); setRemoving(false); setUncertain(null); setBody(""); setHistory(null); setLoadingHistory(false) }
    if (!comment.can_view_history) { epoch.current++; setHistory(null); setLoadingHistory(false) }
  }, [canComment, comment.can_view_history])
  useEffect(() => {
    onDirtyChange(comment.id, editing || removing || Boolean(uncertain))
    return () => onDirtyChange(comment.id, false)
  }, [comment.id, editing, removing, uncertain, onDirtyChange])
  async function apply(change: ArtifactCommentChange, version: number) {
    const intent = epoch.current
    setError(null)
    try {
      await onChange({ ...thread, version }, change)
      if (intent !== epoch.current) return
      setEditing(false); setRemoving(false); setUncertain(null); setHistory(null)
    } catch (failure) {
      if (intent !== epoch.current) return
      setError(failure instanceof Error ? failure.message : "Could not update comment.")
      if (!(failure instanceof Error && "status" in failure && typeof failure.status === "number") || failure.status >= 500 || failure.status === 408) setUncertain({ change, version })
      else setUncertain(null)
    }
  }
  const stale = (editing || removing) && base !== thread.version
  const locked = pending || Boolean(uncertain)
  return <li className="space-y-2">
    <div className="flex flex-wrap gap-x-2 text-xs text-muted-foreground">
      {comment.author_agent_id || comment.author_run_id ? <>
        <span className="rounded-full bg-muted px-2 py-0.5" title={`${comment.author_agent_id ? `Agent ${comment.author_agent_id}` : "Removed agent"}${comment.author_run_id ? ` · Run ${comment.author_run_id}` : ""}`}>Agent reply</span>
        <span title={comment.author_user_id}>On behalf of {comment.author_name || "workspace member"}</span>
      </> : <span title={comment.author_user_id}>{comment.author_name || "Workspace member"}</span>}
      <time dateTime={comment.created_at}>{new Date(comment.created_at).toLocaleString()}</time>
      {comment.edited_at && !comment.deleted_at && <span>Edited</span>}
    </div>
    <p className="whitespace-pre-wrap break-words text-sm">{comment.deleted_at ? "Comment deleted" : comment.body}</p>
    {error && <p role="alert" className="text-xs text-destructive">{error}</p>}
    {canComment && !comment.deleted_at && <div className="flex flex-wrap gap-1">
      {comment.can_react && ["👍", "❤️", "👀", "✅"].map(emoji => { const reaction = comment.reactions?.find(item => item.emoji === emoji); return <Button key={emoji} size="sm" variant={reaction?.reacted_by_me ? "secondary" : "ghost"} className="coarse:h-12" aria-label={`React ${emoji}`} aria-pressed={Boolean(reaction?.reacted_by_me)} disabled={locked || editing || removing} onClick={() => void apply({ action: "react", commentId: comment.id, emoji, active: !reaction?.reacted_by_me }, thread.version)}>{emoji}{reaction?.count ? ` ${reaction.count}` : ""}</Button> })}
      {comment.can_edit && <Button size="sm" variant="ghost" disabled={locked || editing || removing} onClick={() => { setBody(comment.body); setBase(thread.version); setEditing(true) }}>Edit comment</Button>}
      {comment.can_delete && <Button size="sm" variant="ghost" disabled={locked || editing || removing} onClick={() => { setBase(thread.version); setRemoving(true) }}>Delete comment</Button>}
    </div>}
    {(!canComment || !comment.can_react) && comment.reactions?.some(item => item.count > 0) && <p className="text-xs">{comment.reactions.filter(item => item.count > 0).map(item => `${item.emoji} ${item.count}`).join(" · ")}</p>}
    {canComment && editing && <div className="space-y-2"><Textarea aria-label="Edit comment text" value={body} maxLength={4096} disabled={locked} onChange={event => setBody(event.target.value)} /><Button size="sm" disabled={locked || stale || !body.trim()} onClick={() => void apply({ action: "edit", commentId: comment.id, body }, base)}>Save comment</Button></div>}
    {canComment && removing && <div className="space-y-2"><p className="text-xs">Delete this comment? Its place in the thread and private edit history will remain.</p><Button size="sm" variant="destructive" disabled={locked || stale} onClick={() => void apply({ action: "delete", commentId: comment.id }, base)}>Confirm deletion</Button></div>}
    {canComment && stale && !uncertain && <div className="space-y-2"><p className="text-xs text-warn">This thread changed. Review the current comments before continuing.</p><Button size="sm" variant="outline" disabled={pending} onClick={() => setBase(thread.version)}>Use latest thread version</Button></div>}
    {canComment && (editing || removing) && !uncertain && <Button size="sm" variant="ghost" disabled={pending} onClick={() => { setEditing(false); setRemoving(false); setError(null) }}>Cancel comment change</Button>}
    {canComment && uncertain && <Button size="sm" variant="outline" disabled={pending} onClick={() => void apply(uncertain.change, uncertain.version)}>Retry unchanged comment update</Button>}
    {canComment && comment.can_view_history && <Button size="sm" variant="ghost" disabled={loadingHistory} onClick={async () => {
      if (history) { setHistory(null); return }
      const intent = epoch.current; setLoadingHistory(true)
      try { const rows = await onHistory(thread.id, comment.id); if (intent === epoch.current) setHistory(rows) }
      catch (failure) { if (intent === epoch.current) setError(failure instanceof Error ? failure.message : "History unavailable.") }
      finally { if (intent === epoch.current) setLoadingHistory(false) }
    }}>{history ? "Hide edit history" : "View edit history"}</Button>}
    {canComment && comment.can_view_history && history && <ol aria-label="Comment edit history" className="space-y-2 border-l pl-3">{history.map(item => <li key={item.version} className="text-xs"><p>Version {item.version} · {item.action} · {new Date(item.created_at).toLocaleString()}</p><p className="whitespace-pre-wrap break-words">{item.body}</p></li>)}</ol>}
  </li>
}
