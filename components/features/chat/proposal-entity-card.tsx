"use client"

import { useState } from "react"
import { Button } from "@/components/ui/button"
import { E2CommandSchemas, type ProposalRef } from "@/lib/chat-e2-cards"
import { EntityCardFrame } from "./entity-card-frame"
import { proposalDraft, useProposalDraft, type ProposalScope } from "@/hooks/use-proposal-draft"
import { useProposalOperation, type ProposalTransport } from "@/hooks/use-proposal-operation"

type Props = {
  reference: ProposalRef; viewerId: string; surface: ProposalScope["surface"]
  transport: ProposalTransport; onSnapshot: (reference: ProposalRef) => void; onAccessLost: () => void
  onComment: (comment: string) => Promise<boolean>; onOpen?: () => void
  onReadCurrent?: () => Promise<ProposalRef | null>
  renderIssue: (issue: { id: string; identifier: string }) => React.ReactNode
  assignees: { id: string; name: string }[]; projects?: { id: string; name: string }[]
  rechecking?: boolean
}
const control = "block w-full rounded-md border bg-background px-2 py-2 coarse:min-h-[44px]"

/** The caller supplies authorized snapshots and transport, never generated text. */
export function ProposalEntityCard({ reference, viewerId, surface, transport, onSnapshot, onAccessLost, onComment, onOpen, renderIssue, assignees, projects = [], rechecking = false, onReadCurrent }: Props) {
  const scope = { workspaceId: reference.entity.workspace_id, viewerId, proposalId: reference.entity.id, surface }
  const fields = useProposalDraft(reference, scope)
  const operation = useProposalOperation(scope, transport, (snapshot, action) => {
    if (action === "edit") fields.setDraft(proposalDraft(snapshot))
    setReview(false); onSnapshot(snapshot)
  }, onAccessLost)
  const [review, setReview] = useState(false)
  const [commentBusy, setCommentBusy] = useState(false)
  const [commentError, setCommentError] = useState(false)
  const [readingConflict, setReadingConflict] = useState(false)
  const proposal = reference.proposal
  const selected = fields.draft.items.filter(item => item.selected)
  const dirty = JSON.stringify({ title: fields.draft.title, items: fields.draft.items }) !== JSON.stringify({ title: proposal.title, items: proposal.items })
  const locked = rechecking || fields.conflict || ["saving", "unknown", "conflict"].includes(operation.state)
  const update = (id: string, change: Partial<typeof proposal.items[number]>) => {
    setReview(false)
    fields.setDraft(draft => ({ ...draft, items: draft.items.map(item => item.id === id ? { ...item, ...change } : item) }))
  }
  const command = (action: "edit" | "confirm" | "discard") => {
    const body = action === "edit" ? { expected_version: fields.draft.version, title: fields.draft.title, items: fields.draft.items }
      : action === "confirm" ? { proposal_id: reference.entity.id, reviewed_version: fields.draft.version }
        : { expected_version: fields.draft.version }
    const parsed = E2CommandSchemas[`proposal_${action}`].safeParse(body)
    if (parsed.success) void operation.submit(action, parsed.data)
  }
  if (operation.state === "unavailable") return null
  return <EntityCardFrame>
    <div className="flex items-center justify-between gap-2"><span className="text-xs text-muted-foreground">Proposal · {proposal.status}</span>{onOpen && <Button variant="outline" size="sm" className="coarse:min-h-[44px]" onClick={onOpen}>Open on right</Button>}</div>
    <h3 className="break-words font-medium">{proposal.title}</h3>
    <p className="text-xs text-muted-foreground">Proposed by agent {reference.provenance.actor.id}</p>
    {proposal.status === "accepted" && proposal.outcome ? <>
      <p role="status">Created {proposal.outcome.issues.length} issues · confirmed by {proposal.outcome.confirmed_by} · {new Date(proposal.outcome.confirmed_at).toLocaleString()}</p>
      <p className="text-xs text-muted-foreground">Receipt for version {proposal.outcome.reviewed_version}. Issues below show their current authorized state.</p>
      {proposal.outcome.issues.map(issue => <div key={issue.id}>{renderIssue(issue)}</div>)}
    </> : proposal.status !== "draft" ? <p role="status">This proposal is {proposal.status}.</p> : <>
      {rechecking && <p role="status">Checking current access… Your draft is kept.</p>}
      {fields.storageError && <p role="alert">Could not preserve this draft for reload.</p>}
      {fields.conflict && <div role="alert" className="space-y-2"><p>A newer proposal version is available. Your draft has not changed.</p><p>Current proposal: {proposal.title} · version {proposal.version}</p><ul>{proposal.items.map(item => <li key={item.id}>{item.title} · {item.priority} · {item.selected ? "selected" : "excluded"}</li>)}</ul><Button variant="outline" className="coarse:min-h-[44px]" disabled={operation.state === "unknown" || operation.state === "saving" || operation.state === "conflict"} onClick={() => { fields.useCurrent(); setReview(false) }}>Use current proposal and replace field draft</Button><p className="text-xs">Your separate comment draft will remain.</p></div>}
      <fieldset disabled={["saving", "unknown"].includes(operation.state) || !proposal.allowed_actions.includes("edit")} className="min-w-0 space-y-3">
        <label className="block text-xs">Proposal title<input className={control} maxLength={240} required value={fields.draft.title} onChange={event => { fields.setDraft(draft => ({ ...draft, title: event.target.value })); setReview(false) }} /></label>
        {fields.draft.items.map((item, index) => <div key={item.id} className="min-w-0 space-y-2 rounded-md border p-3">
          <label className="flex min-h-9 items-center gap-2 coarse:min-h-[44px]"><input type="checkbox" checked={item.selected} onChange={event => update(item.id, { selected: event.target.checked })} />Include issue {index + 1}</label>
          <label className="block text-xs">Issue {index + 1} title<input className={control} maxLength={240} required value={item.title} onChange={event => update(item.id, { title: event.target.value })} /></label>
          <label className="block text-xs">Issue {index + 1} description<textarea className={control} maxLength={10000} value={item.description} onChange={event => update(item.id, { description: event.target.value })} /></label>
          <div className="grid gap-2 sm:grid-cols-2"><label className="text-xs">Issue {index + 1} priority<select className={control} value={item.priority} onChange={event => update(item.id, { priority: event.target.value as typeof item.priority })}>{["none", "urgent", "high", "medium", "low"].map(priority => <option key={priority}>{priority}</option>)}</select></label>
            <label className="text-xs">Issue {index + 1} assignee<select className={control} value={item.assignee_id ?? ""} onChange={event => update(item.id, { assignee_id: event.target.value || null })}><option value="">Unassigned</option>{item.assignee_id && !assignees.some(person => person.id === item.assignee_id) && <option value={item.assignee_id}>Current assignee</option>}{assignees.map(person => <option key={person.id} value={person.id}>{person.name}</option>)}</select></label></div>
          <p className="text-xs text-muted-foreground">Project: {projects.find(project => project.id === item.project_id)?.name ?? item.project_id ?? "No project"}</p>
        </div>)}
      </fieldset>
      <div className="flex flex-wrap gap-2">
        {proposal.allowed_actions.includes("edit") && <Button className="coarse:min-h-[44px]" variant="outline" disabled={locked || !dirty || !E2CommandSchemas.proposal_edit.safeParse({ expected_version: fields.draft.version, title: fields.draft.title, items: fields.draft.items }).success} onClick={() => command("edit")}>Save proposal edits</Button>}
        {proposal.allowed_actions.includes("confirm") && <Button className="coarse:min-h-[44px]" disabled={locked || dirty || !selected.length} onClick={() => setReview(true)}>Review confirmation</Button>}
        {proposal.allowed_actions.includes("discard") && <Button className="coarse:min-h-[44px]" variant="ghost" disabled={locked} onClick={() => command("discard")}>Discard proposal</Button>}
      </div>
      {dirty && <p className="text-xs text-muted-foreground">Save edits before confirming.</p>}
      {review && !dirty && !fields.conflict && <div className="space-y-2 rounded-md border p-3" aria-label="Confirmation summary"><p>Create {selected.length} issues as {viewerId}.</p><ul>{selected.map(item => <li key={item.id}>{item.title} · project {projects.find(project => project.id === item.project_id)?.name ?? item.project_id ?? "none"} · {item.assignee_id ? `assigned to ${assignees.find(person => person.id === item.assignee_id)?.name ?? item.assignee_id}` : "unassigned"}</li>)}</ul><p>All selected issues are created together. Direct start is a separate action; existing workspace automations still apply.</p><Button className="coarse:min-h-[44px]" disabled={locked} onClick={() => command("confirm")}>Confirm creation of {selected.length} issues</Button></div>}
      {operation.state !== "idle" && <div role={operation.state === "saving" ? "status" : "alert"} className="space-y-2"><p>{operation.state === "saving" ? "Saving…" : operation.message}</p>{operation.state === "unknown" && <Button className="coarse:min-h-[44px]" variant="outline" disabled={rechecking} onClick={() => { if (!rechecking) void operation.retry() }}>Retry identical operation</Button>}{operation.state === "conflict" && operation.current && <><p>Current version {operation.current.proposal.version}: {operation.current.proposal.title}</p><Button className="coarse:min-h-[44px]" variant="outline" onClick={() => operation.reviewCurrent()}>Review current version; keep draft</Button></>}{operation.state === "conflict" && !operation.current && onReadCurrent && <Button variant="outline" className="coarse:min-h-[44px]" disabled={rechecking || readingConflict} onClick={async () => {
        setReadingConflict(true)
        try { const snapshot = await onReadCurrent(); if (snapshot) operation.reviewCurrent(snapshot) } finally { setReadingConflict(false) }
      }}>Read current proposal and review conflict</Button>}</div>}
      <div className="space-y-2 border-t pt-3"><label className="block text-xs">Comment to agent<textarea className={control} maxLength={10000} value={fields.comment} disabled={commentBusy} onChange={event => fields.setComment(event.target.value)} /></label><Button className="coarse:min-h-[44px]" variant="outline" disabled={commentBusy || rechecking || !fields.comment.trim()} onClick={async () => {
        setCommentBusy(true); setCommentError(false)
        try { if (await onComment(fields.comment)) fields.setComment(""); else setCommentError(true) } catch { setCommentError(true) } finally { setCommentBusy(false) }
      }}>Send comment to agent</Button>{commentError && <p role="alert">Could not send. Your comment draft is kept.</p>}</div>
    </>}
  </EntityCardFrame>
}
