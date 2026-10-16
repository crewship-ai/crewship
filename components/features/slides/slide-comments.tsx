"use client"

import { useEffect, useState } from "react"
import { useSlideComments } from "@/hooks/use-slide-comments"
import type { SlidesState } from "@/lib/slides/mutations"
import type { SlideCommentAnchor } from "@/lib/slides/comments"
import { validCommentBody } from "@/lib/slides/comment-validation"

const control = "min-h-9 coarse:min-h-12 rounded border px-3 disabled:opacity-50"
interface Composer { anchor?: SlideCommentAnchor; threadId?: string; version?: number }
export function SlideComments({ workspaceId, slug, pageId, saved, contentPending, onDirtyChange }: { workspaceId: string; slug: string; pageId: string; saved: SlidesState; contentPending: boolean; onDirtyChange?: (dirty: boolean) => void }) {
  const comments = useSlideComments(workspaceId, slug, pageId)
  const [composer, setComposer] = useState<Composer>({}), [body, setBody] = useState("")
  const [slideId, setSlideId] = useState(saved.deck.slide_order[0]), [elementId, setElementId] = useState("")
  const slide = saved.deck.slides.find(value => value.id === slideId)
  const locked = comments.busy || !!comments.pending || comments.blocked
  const draftDirty = body.length > 0 || !!comments.pending
  useEffect(() => { onDirtyChange?.(draftDirty); return () => onDirtyChange?.(false) }, [draftDirty, onDirtyChange])
  async function submit() {
    if (!validCommentBody(body) || locked || !comments.page?.can_comment) return
    if (composer.threadId && composer.version) {
      if (await comments.send({ path: `/${encodeURIComponent(composer.threadId)}/replies`, threadId: composer.threadId, body: { page_id: pageId, schema_version: 1, mutation_id: crypto.randomUUID(), if_version: composer.version, body } })) { setBody(""); setComposer({}) }
    } else if (composer.anchor && !contentPending) {
      if (await comments.send({ path: "", body: { page_id: pageId, schema_version: 1, base_rev: composer.anchor.revision, mutation_id: crypto.randomUUID(), anchor: composer.anchor, body } })) { setBody(""); setComposer({}) }
    }
  }
  return <section aria-label="Slides comments" className="space-y-3 rounded border p-3">
    <h3 className="font-medium">Comments on saved Slides</h3>
    <p role="status" className="text-sm">{comments.status}</p>
    {comments.error && <p role="alert">{comments.error}</p>}
    <button className={control} disabled={comments.busy} onClick={() => void comments.refresh()}>Refresh comment threads</button>
    {contentPending && <p>Save or discard Slides changes before starting a new targeted comment. Existing replies keep their original thread version.</p>}
    <fieldset disabled={locked || contentPending || !comments.page?.can_comment} className="flex flex-wrap items-end gap-2">
      <legend>Choose a target in saved revision {saved.revision}</legend>
      <label>Saved slide<select aria-label="Comment saved slide" className={control} value={slide ? slideId : ""} onChange={event => { setSlideId(event.target.value); setElementId("") }}><option value="">Choose slide</option>{saved.deck.slide_order.map((id, index) => <option key={id} value={id}>Slide {index + 1}</option>)}</select></label>
      <label>Saved element<select aria-label="Comment saved element" className={control} value={elementId} onChange={event => setElementId(event.target.value)}><option value="">Whole slide</option>{slide?.elements.map((item, index) => <option key={item.id} value={item.id}>{item.type} {index + 1}</option>)}</select></label>
      <button className={control} disabled={!slide || (!!elementId && !slide.elements.some(item => item.id === elementId))} onClick={() => { if (slide) setComposer({ anchor: { anchor_schema_version: 1, kind: elementId ? "element" : "slide", slide_id: slide.id, ...(elementId ? { element_id: elementId } : {}), revision: saved.revision } }) }}>Comment on saved target</button>
    </fieldset>
    {(composer.anchor || composer.threadId || body || comments.pending) && <div className="space-y-2">
      <p>{composer.anchor ? `New ${composer.anchor.kind} comment · original saved revision ${composer.anchor.revision}` : `Reply to thread ${composer.threadId ?? ""} · original version ${composer.version ?? ""}`}</p>
      <label className="block">Comment body<textarea className="min-h-24 w-full rounded border p-2" disabled={locked} value={body} onChange={event => setBody(event.target.value)} /></label>
      {body && !validCommentBody(body) && <p role="alert">Use nonblank text of at most 10,000 UTF-8 bytes.</p>}
      <button className={control} disabled={locked || !validCommentBody(body) || !comments.page?.can_comment || (!composer.threadId && contentPending)} onClick={() => void submit()}>{composer.threadId ? "Send reply" : "Post comment"}</button>
      <button className={control} disabled={comments.busy || !!comments.pending} onClick={() => { if (!body || window.confirm("Discard this unsent comment draft?")) { setBody(""); setComposer({}) } }}>Discard comment draft</button>
    </div>}
    {comments.pending && <div className="flex flex-wrap gap-2">
      <button className={control} disabled={comments.busy || comments.blocked} onClick={() => void comments.send().then(ok => { if (ok) { setBody(""); setComposer({}) } })}>Retry original comment request</button>
      <button className={control} disabled={comments.busy} onClick={() => { if (window.confirm("Discard the original comment request and review current threads? An uncertain request may already be committed. Your text draft stays here; choose a current target or thread version explicitly before sending again.")) { setComposer({}); void comments.discardAttempt() } }}>Review current threads and discard original request</button>
    </div>}
    {!comments.threads.length && comments.page && <p>No comment threads.</p>}
    {comments.threads.map(thread => <article key={thread.id} className="space-y-2 rounded border p-3" aria-label={`Comment thread ${thread.id}`}>
      <p className="text-sm">{thread.anchor.kind} {thread.anchor.element_id ?? thread.anchor.slide_id} · anchored at revision {thread.anchor.revision} · projected at revision {thread.projection_revision} · version {thread.version} · {thread.resolved ? "Resolved" : "Open"}</p>
      <p>{thread.status === "detached" ? `Detached: original target missing since revision ${thread.last_missing_revision}.` : thread.status === "restored" ? `Restored original target at revision ${thread.restored_at_revision}; previously missing at revision ${thread.last_missing_revision}.` : "Attached to original target."}</p>
      <p className="whitespace-pre-wrap break-words">{thread.first_comment.body}</p>
      <div className="flex flex-wrap gap-2">
        <button className={control} disabled={locked || thread.resolved || !comments.page?.can_comment} onClick={() => { setComposer({ threadId: thread.id, version: thread.version }); void comments.loadHistory(thread.id) }}>Reply to version {thread.version}</button>
        <button className={control} disabled={comments.busy} onClick={() => void comments.loadHistory(thread.id)}>View comment history</button>
        <button className={control} disabled={locked || draftDirty || !comments.page?.can_resolve} onClick={() => void comments.send({ path: `/${encodeURIComponent(thread.id)}/resolve`, threadId: thread.id, body: { page_id: pageId, schema_version: 1, mutation_id: crypto.randomUUID(), if_version: thread.version, resolved: !thread.resolved } })}>{thread.resolved ? "Reopen thread" : "Resolve thread"}</button>
      </div>
    </article>)}
    {comments.page?.has_more && <button className={control} disabled={comments.busy} onClick={() => void comments.refresh(comments.page?.next_cursor)}>Load more comment threads</button>}
    {comments.history && <section aria-label="Comment history" className="space-y-2"><h4>History for thread {comments.history.threadId} · snapshot watermark {comments.history.page.watermark}</h4>{comments.history.page.items.map(event => <p key={event.version} className="whitespace-pre-wrap break-words">Version {event.version} · {event.kind} · {event.created_at} · {event.actor}{event.body ? `: ${event.body}` : ""}</p>)}{comments.history.page.has_more && <button className={control} disabled={comments.busy} onClick={() => { if (comments.history) void comments.loadHistory(comments.history.threadId, comments.history.page.next_cursor) }}>Load more comment history</button>}</section>}
  </section>
}
