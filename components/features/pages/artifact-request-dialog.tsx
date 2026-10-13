"use client"

import { useId } from "react"
import { Button } from "@/components/ui/button"
import { Textarea } from "@/components/ui/textarea"
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog"
import type { ArtifactRequestDraft, ArtifactRequestTarget } from "@/hooks/use-artifact-requests"

export function ArtifactRequestDialog({ open, onOpenChange, draft, head, targets, pending, error, onChange, onDiscard, onRefresh, onSend }: {
  open: boolean
  onOpenChange: (open: boolean) => void
  draft: ArtifactRequestDraft
  head: number
  targets: ArtifactRequestTarget[]
  pending: boolean
  error: string | null
  onChange: (draft: ArtifactRequestDraft) => void
  onDiscard: () => void
  onRefresh: () => void
  onSend: () => void
}) {
  const id = useId()
  const selected = targets.find(item => item.chat_id === draft.chat_id)
  const stale = draft.base_rev !== head
  return <Dialog open={open} onOpenChange={onOpenChange}>
    <DialogContent className="max-h-[85dvh] overflow-y-auto">
      <DialogHeader>
        <DialogTitle>Send to agent</DialogTitle>
        <DialogDescription>This explicitly starts agent work. Choose an eligible private chat; the agent must already have write access to this artifact.</DialogDescription>
      </DialogHeader>
      <form className="space-y-4" onSubmit={event => { event.preventDefault(); onSend() }}>
        <div className="rounded-md border bg-muted/30 p-3 text-sm">
          <p className="font-medium">Source: revision {draft.base_rev}{draft.thread_id ? " · Selected thread" : " · Whole document"}</p>
          <p className="mt-1 text-xs text-muted-foreground">The selected chat receives this saved artifact revision and your instructions{draft.thread_id ? ", plus all saved comments in this thread at send time" : ""}. Later replies do not change the request. Other threads are not sent.</p>
        </div>
        {stale && <p role="alert" className="text-sm text-warn">Revision {draft.base_rev} is no longer latest. This draft stays on its original revision. Discard it and start a new request from revision {head}.</p>}
        <div className="space-y-2">
          <label htmlFor={`${id}-chat`} className="text-sm font-medium">Private agent chat</label>
          <select id={`${id}-chat`} className="w-full rounded-md border bg-background p-2 text-sm coarse:min-h-12" value={draft.chat_id} disabled={pending} onChange={event => onChange({ ...draft, chat_id: event.target.value })}>
            <option value="">Choose a chat</option>
            {targets.map(target => <option key={target.chat_id} value={target.chat_id}>{target.agent_name} · {target.title || "Untitled chat"}</option>)}
          </select>
          {!targets.length && <p className="text-xs text-muted-foreground">No eligible private chats. Open a private chat with an agent that has an explicit write grant on this page, then refresh the choices.</p>}
          {draft.chat_id && !selected && <p className="text-xs text-warn">The selected chat is no longer eligible. Choose a currently authorized private chat.</p>}
          <Button type="button" variant="ghost" size="sm" disabled={pending} onClick={onRefresh}>Refresh chat choices</Button>
        </div>
        <div className="space-y-2">
          <label htmlFor={`${id}-instruction`} className="text-sm font-medium">Instructions</label>
          <Textarea id={`${id}-instruction`} className="min-h-32 text-sm" maxLength={4096} disabled={pending} value={draft.instruction} onChange={event => onChange({ ...draft, instruction: event.target.value })} placeholder="Describe the change you want the agent to make…" />
          <p className="text-xs text-muted-foreground">Up to 4 KiB. Work uses the agent’s configured model and budget. A comment or mention alone does not send a request.</p>
        </div>
        {selected && <p className="text-xs text-muted-foreground">Target: <span className="font-medium text-foreground">{selected.agent_name}</span> in {selected.title || "Untitled chat"}.</p>}
        {error && <p role="alert" className="rounded-md border border-destructive/30 p-3 text-sm text-destructive">{error}</p>}
        <DialogFooter>
          <Button type="button" variant="ghost" disabled={pending} onClick={onDiscard}>{draft.instruction.trim() ? "Discard draft" : "Cancel"}</Button>
          <Button type="submit" disabled={pending || stale || !selected || !draft.instruction.trim()}>{pending ? "Sending…" : error ? "Retry send" : "Send request"}</Button>
        </DialogFooter>
      </form>
    </DialogContent>
  </Dialog>
}
