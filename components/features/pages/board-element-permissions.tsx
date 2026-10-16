"use client"
import { useEffect, useState } from "react"
import { Button } from "@/components/ui/button"
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog"
import { useBoardElementPermissions, BoardPermissionError, type BoardPermissionIntent } from "@/hooks/use-board-element-permissions"
import type { PageBoard } from "@/hooks/use-page-board"

export function BoardElementPermissions({ workspaceId, slug, board, selectedIds, disabled, onDirtyChange }: {
  workspaceId: string; slug: string; board: PageBoard; selectedIds: string[]; disabled: boolean; onDirtyChange: (dirty: boolean) => void
}) {
  const [open, setOpen] = useState(false)
  const [intent, setIntent] = useState<BoardPermissionIntent | null>(null)
  const [submitted, setSubmitted] = useState<{ intent: BoardPermissionIntent; needsBoardDelegation: boolean } | null>(null)
  const permission = useBoardElementPermissions(workspaceId, slug, open && board.can_write)
  const uncertain = Boolean(permission.grant.error && (!(permission.grant.error instanceof BoardPermissionError) || permission.grant.error.status >= 500))
  const pending = permission.grant.isPending || permission.revoke.isPending || permission.revokeDelegation.isPending
  const locked = uncertain || Boolean(permission.partialDelegation)
  const dirty = Boolean(intent?.purpose.trim() || pending || uncertain || permission.partialDelegation)
  useEffect(() => { onDirtyChange(dirty); return () => onDirtyChange(false) }, [dirty, onDirtyChange])
  useEffect(() => { if (permission.denied || !board.can_write) { setIntent(null); if (!permission.partialDelegation) setSubmitted(null) } }, [permission.denied, board.can_write, permission.partialDelegation])
  if (!board.can_write) return null
  return <div className="px-4 pb-3">
    <Button variant="outline" size="sm" disabled={disabled || open || pending || uncertain} onClick={() => {
      if (permission.partialDelegation) { setOpen(true); return }
      permission.grant.reset(); permission.revoke.reset(); setSubmitted(null)
      setIntent({ mutation_id: crypto.randomUUID(), run_id: "", elements: board.records.filter(record => selectedIds.includes(record.element_id) && !record.deleted && !record.author_agent_id).map(record => ({ element_id: record.element_id, expected_rev: record.element_rev })), methods: ["update"], purpose: "", expires_in_seconds: 900 })
      setOpen(true)
    }}>Agent editing permissions</Button>
    <Dialog open={open} onOpenChange={value => { if (!pending && !uncertain) { setOpen(value); if (!value && !permission.partialDelegation) { setIntent(null); setSubmitted(null) } } }}><DialogContent className="max-h-[85dvh] overflow-y-auto"><DialogHeader>
      <DialogTitle>Allow changes to selected drawings</DialogTitle>
      <DialogDescription>Authorize one saved change by a specific running agent. The selected elements must still have the versions shown when this dialog opened. Other human drawings remain protected.</DialogDescription>
    </DialogHeader>
      {permission.partialDelegation && <div className="space-y-2 rounded-md border border-warn/30 p-3"><p role="status" className="text-sm">The run's Board delegation was granted, but the selected-drawing permission has not been confirmed. While the run and delegation remain valid, it can use its existing Page write grant for its own Board elements.</p><Button variant="outline" disabled={pending} onClick={async () => { try { await permission.revokeDelegation.mutateAsync(permission.partialDelegation!); setIntent(null); setSubmitted(null) } catch { /* Keep confirmed partial authority visible. */ } }}>Revoke granted run delegation</Button>{permission.revokeDelegation.error && <p role="alert" className="text-sm text-destructive">{permission.revokeDelegation.error.message}</p>}</div>}
      {permission.denied ? <div className="space-y-2"><p role="alert">Your access changed. Permissions and drafts are hidden.</p><Button variant="outline" disabled={pending} onClick={() => void permission.refresh()}>Recheck editing access</Button></div> : <>
        {permission.query.error && <p role="alert" className="text-sm text-destructive">{permission.query.error.message}</p>}
        {permission.grant.error && <p role="alert" className="text-sm text-destructive">{permission.grant.error.message}{uncertain && " Retry the unchanged permission to confirm its outcome."}</p>}
        {permission.revoke.error && <p role="alert" className="text-sm text-destructive">{permission.revoke.error.message}</p>}
        {intent && intent.elements.length > 0 ? <div className="space-y-3">
          <p className="text-sm">{intent.elements.length} saved human {intent.elements.length === 1 ? "drawing" : "drawings"} selected · Permission lasts up to 15 minutes and ends with the run.</p>
          <label className="block space-y-1 text-sm">Running agent<select aria-label="Running agent to authorize" className="w-full rounded-md border bg-background p-2" value={intent.run_id} disabled={pending || locked} onChange={event => { setSubmitted(null); setIntent({ ...intent, mutation_id: crypto.randomUUID(), run_id: event.target.value }) }}><option value="">Select an eligible running agent</option>{permission.query.data?.eligible_runs.map(run => <option key={run.run_id} value={run.run_id}>{run.agent_name || "Agent"} · {run.run_id.slice(-8)}</option>)}</select></label>
          {permission.query.data && !permission.query.data.eligible_runs.length && <p className="text-xs text-muted-foreground">No eligible run is active. Start a private chat run with an agent that has an explicit write grant on this page.</p>}
          <label className="block space-y-1 text-sm">Purpose<textarea aria-label="Permission purpose" maxLength={1024} className="min-h-20 w-full rounded-md border bg-background p-2" value={intent.purpose} disabled={pending || locked} onChange={event => { setSubmitted(null); setIntent({ ...intent, mutation_id: crypto.randomUUID(), purpose: event.target.value }) }} /></label>
          <label className="flex items-center gap-2 text-sm"><input type="checkbox" checked={intent.methods.includes("delete")} disabled={pending || locked} onChange={event => { setSubmitted(null); setIntent({ ...intent, mutation_id: crypto.randomUUID(), methods: event.target.checked ? ["update", "delete"] : ["update"] }) }} />Also allow deleting these selected drawings</label>
          <p className="text-xs text-muted-foreground">This also authorizes the chosen run to use its existing page write grant for its own board elements. It does not grant access to another page or permission to edit other human elements.</p>
          <Button disabled={pending || !intent.run_id || !intent.purpose.trim() || new TextEncoder().encode(intent.purpose).length > 1024 || !permission.query.data} onClick={async () => {
            const run = permission.query.data?.eligible_runs.find(item => item.run_id === intent.run_id)
            if (!submitted && !run) return
            const input = submitted ?? { intent: structuredClone(intent), needsBoardDelegation: Boolean(run?.needs_board_delegation) }
            setSubmitted(input)
            try { await permission.grant.mutateAsync(input); setIntent(null); setSubmitted(null) }
            catch { /* Preserve exact versions and mutation identity for retry. */ }
          }}>{pending ? "Saving…" : uncertain ? "Retry permission" : "Allow one change"}</Button>
        </div> : <p className="text-sm text-muted-foreground">Select saved human drawings before opening this dialog to allow a change.</p>}
        <div className="space-y-2 border-t pt-3"><h3 className="text-sm font-medium">Existing permissions</h3>{permission.query.data?.permissions.map(item => <div key={item.id} className="space-y-1 rounded border p-3 text-sm"><p>{item.agent_name || "Agent"} · {item.elements.length} drawings · {item.used ? "Used" : item.revoked ? "Revoked" : new Date(item.expires_at).getTime() < Date.now() ? "Expired" : "Available"}</p><p className="whitespace-pre-wrap break-words text-xs text-muted-foreground">{item.purpose}</p>{!item.used && !item.revoked && <Button variant="outline" size="sm" disabled={pending} onClick={async () => { try { await permission.revoke.mutateAsync(item.id) } catch { /* Failure remains visible. */ } }}>Revoke permission</Button>}</div>)}</div>
      </>}
      <DialogFooter><Button variant="ghost" disabled={pending || uncertain} onClick={() => { setOpen(false); if (!permission.partialDelegation) { setIntent(null); setSubmitted(null) } }}>Close</Button></DialogFooter>
    </DialogContent></Dialog>
  </div>
}
