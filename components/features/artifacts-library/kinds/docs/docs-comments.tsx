"use client"

import * as React from "react"
import { CheckCircle2, MessageSquare, RotateCcw } from "lucide-react"
import { Button } from "@/components/ui/button"
import { Textarea } from "@/components/ui/textarea"
import type { CommentWrite, DocumentComment, DocumentComments } from "@/hooks/use-document-comments"
import type { CommentTarget } from "@/components/features/pages/document-rich-extensions"
import { cn } from "@/lib/utils"

export interface CommentThread { root: DocumentComment; replies: DocumentComment[] }

/** Roots oldest first, each with its replies; a reply whose root is missing is not shown. */
export function commentThreads(comments: DocumentComment[]): CommentThread[] {
  const threads = new Map<string, CommentThread>()
  for (const comment of comments) if (!comment.parent) threads.set(comment.id, { root: comment, replies: [] })
  for (const comment of comments) if (comment.parent) threads.get(comment.parent)?.replies.push(comment)
  return [...threads.values()]
}

const time = new Intl.DateTimeFormat("en", { month: "short", day: "numeric", hour: "2-digit", minute: "2-digit" })
function when(value: string) { const date = new Date(value); return Number.isNaN(date.getTime()) ? "" : time.format(date) }
function initials(name: string) { return name.split(/\s+/).filter(Boolean).slice(0, 2).map(part => part[0]!.toUpperCase()).join("") || "?" }

/**
 * The Comments drawer tab for a Docs page: open threads with their quote and
 * replies, a composer for the selection the person chose, resolved threads
 * behind one toggle. Every write is a request; nothing shows as sent until
 * the read after it says so.
 */
export function DocsComments({ data, nameOf, draft, notice, onCancelDraft, onWrite, onJump, active }: {
  data: DocumentComments
  nameOf: (actor: string) => string
  draft: CommentTarget | null
  notice: string | null
  onCancelDraft: () => void
  /** Resolves once the write is confirmed; rejects with a message for the person. */
  onWrite: (change: CommentWrite) => Promise<void>
  onJump: (thread: DocumentComment) => void
  active: string | null
}) {
  const threads = commentThreads(data.comments)
  const open = threads.filter(thread => !thread.root.resolved)
  const resolved = threads.filter(thread => thread.root.resolved)
  const [showResolved, setShowResolved] = React.useState(false)
  return <div className="grid content-start gap-3 p-3.5">
    {draft && data.canComment && <NewThread draft={draft} onCancel={onCancelDraft} onWrite={onWrite} />}
    {notice && <p role="status" className="text-xs text-muted-foreground">{notice}</p>}
    {open.length === 0 && !draft && <p className="flex items-start gap-2 text-sm text-muted-foreground"><MessageSquare aria-hidden className="mt-0.5 size-4 shrink-0" />
      {data.canComment ? "No open comments. Select text in the page, then choose Comment." : "No open comments."}</p>}
    {open.map(thread => <Thread key={thread.root.id} thread={thread} data={data} nameOf={nameOf} onWrite={onWrite} onJump={onJump} active={active === thread.root.id} />)}
    {resolved.length > 0 && <div className="flex items-center gap-2 rounded-card border px-3 py-2 text-sm text-muted-foreground">
      <CheckCircle2 aria-hidden className="size-4" />
      <span className="flex-1">{resolved.length === 1 ? "1 resolved thread" : `${resolved.length} resolved threads`}</span>
      <Button type="button" variant="link" size="sm" className="h-auto p-0 coarse:min-h-[44px]" aria-expanded={showResolved} onClick={() => setShowResolved(value => !value)}>{showResolved ? "Hide" : "Show"}</Button>
    </div>}
    {showResolved && resolved.map(thread => <Thread key={thread.root.id} thread={thread} data={data} nameOf={nameOf} onWrite={onWrite} onJump={onJump} active={active === thread.root.id} />)}
    {!data.canComment && <p className="text-xs text-muted-foreground">You can read comments. Commenting needs comment access.</p>}
  </div>
}

function NewThread({ draft, onCancel, onWrite }: { draft: CommentTarget; onCancel: () => void; onWrite: (change: CommentWrite) => Promise<void> }) {
  const [body, setBody] = React.useState("")
  const [busy, setBusy] = React.useState(false)
  const [error, setError] = React.useState<string>()
  const send = async () => {
    if (!body.trim() || busy) return
    setBusy(true); setError(undefined)
    try { await onWrite({ kind: "thread", block: draft.block, from: draft.from, to: draft.to, text: body }); setBody(""); onCancel() }
    catch (cause) { setError(cause instanceof Error ? cause.message : String(cause)) }
    finally { setBusy(false) }
  }
  return <form aria-label="New comment" className="grid gap-2 rounded-card border p-3" onSubmit={event => { event.preventDefault(); void send() }}>
    <Quote text={draft.quote} />
    <Textarea aria-label="Comment" value={body} maxLength={10000} rows={3} autoFocus placeholder="Add a comment…" onChange={event => setBody(event.target.value)} />
    {error && <p role="alert" className="text-xs text-destructive">{error}</p>}
    <div className="flex justify-end gap-2">
      <Button type="button" size="sm" variant="ghost" className="coarse:min-h-[44px]" onClick={onCancel}>Cancel</Button>
      <Button type="submit" size="sm" className="coarse:min-h-[44px]" disabled={!body.trim() || busy}>{busy ? "Sending…" : "Comment"}</Button>
    </div>
  </form>
}

function Quote({ text, hint, onClick }: { text: string; hint?: string; onClick?: () => void }) {
  const content = <><MessageSquare aria-hidden className="mt-0.5 size-3 shrink-0" /><span className="line-clamp-2 flex-1 italic">“{text}”</span>{hint && <span className="shrink-0 not-italic">{hint}</span>}</>
  const className = "flex items-start gap-1.5 text-left text-xs text-muted-foreground"
  return onClick ? <button type="button" className={cn(className, "rounded-[var(--radius-row)] hover:text-foreground coarse:min-h-[44px]")} onClick={onClick}>{content}</button> : <p className={className}>{content}</p>
}

function Remark({ comment, nameOf, reply }: { comment: DocumentComment; nameOf: (actor: string) => string; reply?: boolean }) {
  const name = nameOf(comment.actor)
  return <div className={cn("flex gap-2", reply && "pl-6")}>
    <span aria-hidden className={cn("grid shrink-0 place-items-center rounded-full bg-muted font-medium text-muted-foreground", reply ? "size-5 text-xs" : "size-6 text-xs")}>{initials(name)}</span>
    <div className="min-w-0 flex-1">
      <p className="text-sm"><b className="font-semibold">{name}</b> <span className="text-xs text-muted-foreground">{when(comment.created_at)}</span></p>
      <p className="whitespace-pre-wrap break-words text-sm">{comment.body}</p>
    </div>
  </div>
}

function Thread({ thread, data, nameOf, onWrite, onJump, active }: { thread: CommentThread; data: DocumentComments; nameOf: (actor: string) => string; onWrite: (change: CommentWrite) => Promise<void>; onJump: (thread: DocumentComment) => void; active: boolean }) {
  const { root, replies } = thread
  const [replying, setReplying] = React.useState(false)
  const [body, setBody] = React.useState("")
  const [busy, setBusy] = React.useState(false)
  const [error, setError] = React.useState<string>()
  const run = async (change: CommentWrite, done?: () => void) => {
    if (busy) return
    setBusy(true); setError(undefined)
    try { await onWrite(change); done?.() } catch (cause) { setError(cause instanceof Error ? cause.message : String(cause)) } finally { setBusy(false) }
  }
  const gone = root.status === "detached" || root.status === "collapsed"
  return <article aria-label={`Comment by ${nameOf(root.actor)}`} data-comment-thread={root.id} className={cn("grid gap-2 rounded-card border p-3", active && "border-primary", root.resolved && "opacity-75")}>
    <Quote text={root.quote ?? ""} hint={root.resolved ? "Resolved" : gone ? "Text removed" : undefined} onClick={gone ? undefined : () => onJump(root)} />
    <Remark comment={root} nameOf={nameOf} />
    {replies.map(reply => <Remark key={reply.id} comment={reply} nameOf={nameOf} reply />)}
    {replying && <form aria-label="Reply" className="grid gap-2 pl-6" onSubmit={event => { event.preventDefault(); if (body.trim()) void run({ kind: "reply", parent: root.id, text: body }, () => { setBody(""); setReplying(false) }) }}>
      <Textarea aria-label="Reply" value={body} maxLength={10000} rows={2} autoFocus placeholder="Reply…" onChange={event => setBody(event.target.value)} />
      <div className="flex justify-end gap-2">
        <Button type="button" size="sm" variant="ghost" className="coarse:min-h-[44px]" onClick={() => { setReplying(false); setBody("") }}>Cancel</Button>
        <Button type="submit" size="sm" className="coarse:min-h-[44px]" disabled={!body.trim() || busy}>{busy ? "Sending…" : "Reply"}</Button>
      </div>
    </form>}
    {error && <p role="alert" className="text-xs text-destructive">{error}</p>}
    {(data.canComment || data.canResolve) && !replying && <div className="flex justify-end gap-1">
      {data.canComment && !root.resolved && <Button type="button" size="sm" variant="ghost" className="coarse:min-h-[44px]" onClick={() => setReplying(true)}>Reply</Button>}
      {data.canResolve && <Button type="button" size="sm" variant="ghost" className="coarse:min-h-[44px]" disabled={busy}
        onClick={() => void run({ kind: "resolve", comment: root.id, version: root.version, resolved: !root.resolved })}>
        {root.resolved ? <><RotateCcw aria-hidden />Reopen</> : <><CheckCircle2 aria-hidden />Resolve</>}
      </Button>}
    </div>}
  </article>
}
