"use client"
import { useEffect, useRef, useState } from "react"
import type { Editor } from "@tiptap/react"
import { Button } from "@/components/ui/button"
import { Textarea } from "@/components/ui/textarea"
import { DocumentAPIError, documentRequest } from "@/hooks/use-page-document"
interface Comment { id: string; body: string; actor: string; rev: number; block: string; quote: string; status: string; from?: number; to?: number; version: number }
export function DocumentComments({ workspaceId, slug, rev, epoch, editor, disabled }: { workspaceId: string; slug: string; rev: number; epoch: string; editor: Editor | null; disabled: boolean }) {
  const attempts = useRef(new Map<string, unknown>())
  const [comments, setComments] = useState<Comment[]>([])
  const [allowed, setAllowed] = useState(false)
  const [canResolve, setCanResolve] = useState(false)
  const [body, setBody] = useState("")
  const [anchor, setAnchor] = useState<{ block: string; from: number; to: number; quote: string }>()
  const [error, setError] = useState<string>()
  const [busy, setBusy] = useState(false)
  async function refresh() { const result = await documentRequest<{ comments: Comment[]; can_comment: boolean; can_resolve: boolean }>(workspaceId, slug, "/comments"); setComments(result.comments); setAllowed(result.can_comment); setCanResolve(result.can_resolve) }
  useEffect(() => {
    let active = true
    async function read() { try { const result = await documentRequest<{ comments: Comment[]; can_comment: boolean; can_resolve: boolean }>(workspaceId, slug, "/comments"); if (active) { setComments(result.comments); setAllowed(result.can_comment); setCanResolve(result.can_resolve) } } catch (cause) { if (active) { setAllowed(false); setCanResolve(false); setError(cause instanceof Error ? cause.message : "Could not load comments.") } } }
    void read(); const timer = setInterval(() => void read(), 5000)
    return () => { active = false; clearInterval(timer) }
  }, [workspaceId, slug])
  async function write(path: string, input: Record<string, unknown>) {
    const fingerprint = JSON.stringify({ path, ...input, id: undefined, ifRev: undefined })
    const payload = attempts.current.get(fingerprint) ?? input
    attempts.current.set(fingerprint, payload)
    setBusy(true); setError(undefined); try { await documentRequest(workspaceId, slug, path, payload); attempts.current.delete(fingerprint); await refresh(); return true } catch (cause) { if (cause instanceof DocumentAPIError && cause.status === 409) attempts.current.delete(fingerprint); setError(cause instanceof Error ? cause.message : "Could not save comment."); return false } finally { setBusy(false) } }
  function selectAnchor() {
    if (!editor) return
    const { $from, $to, from, to } = editor.state.selection
    if (from === to || $from.parent !== $to.parent || !$from.parent.attrs.id) { setError("Select text within one paragraph or heading to comment."); return }
    setAnchor({ block: $from.parent.attrs.id, from: $from.parentOffset, to: $to.parentOffset, quote: editor.state.doc.textBetween(from, to) }); setError(undefined)
  }
  function focusComment(comment: Comment) {
    if (!editor || comment.status !== "attached" || comment.from === undefined || comment.to === undefined) return
    editor.state.doc.descendants((node, pos) => { if (node.attrs.id === comment.block) editor.chain().focus().setTextSelection({ from: pos + 1 + comment.from!, to: pos + 1 + comment.to! }).run() })
  }
  return <section aria-label="Document comments" className="space-y-3 border-t p-6"><h2 className="font-medium">Comments</h2><p className="text-xs text-muted-foreground">Select text to anchor a comment. Comments do not start agent work.</p>{error && <p role="alert" className="text-sm text-destructive">{error}</p>}
    {allowed && <form className="space-y-2" onSubmit={event => { event.preventDefault(); if (!anchor) return; void write("/comments", { id: crypto.randomUUID(), schema: 2, epoch, ifRev: rev, block: anchor.block, from: anchor.from, to: anchor.to, text: body }).then(saved => { if (saved) { setBody(""); setAnchor(undefined) } }) }}><Button type="button" size="sm" variant="outline" disabled={disabled || busy} onClick={selectAnchor}>Comment on selected text</Button>{anchor && <blockquote className="border-l-2 pl-3 text-sm">{anchor.quote}</blockquote>}<Textarea aria-label="Comment text" value={body} onChange={event => setBody(event.target.value)} disabled={disabled || busy} /><Button disabled={disabled || busy || !anchor || !body.trim()} type="submit">Add comment</Button></form>}
    {comments.map(comment => <article key={comment.id} className="space-y-2 rounded border p-3"><div className="flex gap-2 text-xs text-muted-foreground"><span>{comment.actor}</span><span>Revision {comment.rev}</span><span>{comment.status}</span></div><button className="border-l-2 pl-2 text-left text-sm" disabled={comment.status !== "attached"} onClick={() => focusComment(comment)}>{comment.quote}</button><p className="whitespace-pre-wrap text-sm">{comment.body}</p>{canResolve && <Button variant="ghost" size="sm" disabled={disabled || busy} onClick={() => void write("/comments/resolve", { id: crypto.randomUUID(), schema: 2, epoch, ifRev: rev, comment: comment.id, version: comment.version, resolved: comment.status !== "resolved" })}>{comment.status === "resolved" ? "Reopen" : "Resolve"}</Button>}</article>)}
  </section>
}
