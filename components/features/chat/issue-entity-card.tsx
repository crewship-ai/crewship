"use client"

import { useEffect, useRef, useState } from "react"
import { useQuery } from "@tanstack/react-query"
import { ArrowRight, MessageSquare, Pencil, RefreshCw } from "lucide-react"
import { Button } from "@/components/ui/button"
import { StatusIcon, statusLabel } from "@/components/features/issues/status-icon"
import { PriorityIcon, priorityLabel } from "@/components/features/issues/priority-icon"
import { ISSUE_STATUSES, ALL_PRIORITIES } from "@/components/features/issues/issue-constants"
import { apiFetch } from "@/lib/api-fetch"
import type { EntityRef } from "@/lib/chat-entity-cards"
import type { Mission } from "@/lib/types/mission"
import { chatEntityKeys } from "@/hooks/use-chat-entity-refs"
import { useChatIssueMutation, type CardWriteState } from "@/hooks/use-chat-issue-mutation"
import { EntityCardFrame } from "./entity-card-frame"
import { useChatAgent } from "./chat-agent-context"
import { ChatIssuePullRequests } from "./issue-pull-requests"

export { EntityCardFrame } from "./entity-card-frame"

type Props = {
  reference: EntityRef; chatId: string; userId?: string; rechecking: boolean
  live: boolean; checkedAt?: number; onOpen: (ref: EntityRef) => void; reconcile: () => void
}

export function CardWriteFeedback({ state, message, retry, resolveConflict }: {
  state: CardWriteState; message: string; retry: () => Promise<boolean>; resolveConflict: () => void
}) {
  if (state === "idle") return null
  return <div role={state === "saved" || state === "saving" ? "status" : "alert"} className="space-y-2 text-xs">
    {state === "saving" && "Saving…"}
    {state === "saved" && "Saved"}
    {state === "validation" && message}
    {state === "rechecking" && "Checking current access…"}
    {state === "unknown" && <><p>The outcome is unknown. Your draft is kept.</p><Button size="sm" variant="outline" className="coarse:min-h-[44px]" onClick={() => void retry()}>Check and retry the same operation</Button></>}
    {state === "conflict" && <><p>This issue changed. Your draft is kept.</p><Button size="sm" variant="outline" className="coarse:min-h-[44px]" onClick={resolveConflict}>Reload current values; keep my draft</Button></>}
  </div>
}

export function IssueEntityCard({ reference: ref, chatId, userId, rechecking, live, checkedAt, onOpen, reconcile }: Props) {
  const agent = useChatAgent()
  const [pullRequestsOpen, setPullRequestsOpen] = useState(false)
  const [editor, setEditor] = useState<"fields" | "comment" | null>(null)
  const [draft, setDraft] = useState({ title: ref.current?.title ?? "", status: ref.current?.status ?? "TODO", priority: ref.current?.priority ?? "none", assignee_id: ref.current?.assignee_id ?? "" })
  const baseline = useRef(draft)
  const [draftRevision, setDraftRevision] = useState(ref.current?.revision)
  const [comment, setComment] = useState("")
  const mutation = useChatIssueMutation(reconcile, reconcile, JSON.stringify([ref.entity.workspace_id, userId, ref.entity.id]))
  useEffect(() => { if (mutation.state === "saved") { setEditor(null); if (mutation.savedMethod === "POST") setComment("") } }, [mutation.state, mutation.savedMethod])
  const detail = useQuery({
    queryKey: [...chatEntityKeys.detail(ref.entity.workspace_id, chatId, userId), ref.entity.id],
    enabled: (!!editor || pullRequestsOpen) && !rechecking && !ref.entity.deleted,
    retry: false, gcTime: 0,
    queryFn: async ({ signal }) => {
      const r = await apiFetch(`/api/v1/issues/${encodeURIComponent(ref.entity.identifier ?? ref.entity.id)}?workspace_id=${encodeURIComponent(ref.entity.workspace_id)}`, { signal })
      if (!r.ok) { if ([401, 403, 404].includes(r.status)) reconcile(); throw new Error("Could not verify current access") }
      return r.json() as Promise<Mission>
    },
  })
  const refetchDetail = detail.refetch
  useEffect(() => { if (mutation.state === "conflict") void refetchDetail() }, [mutation.state, refetchDetail])
  const roster = useQuery({
    queryKey: ["chat-issue-agents", ref.entity.workspace_id, userId],
    enabled: editor === "fields" && !rechecking && !!ref.current?.allowed_actions.includes("assign"),
    queryFn: async ({ signal }) => {
      const r = await apiFetch(`/api/v1/agents?workspace_id=${encodeURIComponent(ref.entity.workspace_id)}`, { signal })
      if (!r.ok) throw new Error("Could not load assignees")
      const body = await r.json()
      return (Array.isArray(body) ? body : body.agents ?? []) as { id: string; name: string }[]
    },
  })
  if (rechecking && !ref.current) return <EntityCardFrame><p role="status" className="text-muted-foreground">Checking current access…</p><Button size="sm" variant="outline" className="coarse:min-h-[44px]" onClick={reconcile}>Retry verification</Button></EntityCardFrame>
  if (ref.kind !== "issue") return <EntityCardFrame><p>Unavailable</p></EntityCardFrame>
  if (ref.entity.deleted) return <EntityCardFrame><p className="text-muted-foreground">Issue deleted</p></EntityCardFrame>
  const current = ref.current
  if (!current || !current.allowed_actions.includes("view")) return <EntityCardFrame><p>Unavailable</p></EntityCardFrame>
  const can = (action: string) => current.allowed_actions.some((a) => a === action) && (!editor || detail.data?.allowed_actions?.some((a) => a === action))
  const actor = ref.provenance.actor
  const actorName = actor.type === "agent" ? actor.id === agent?.id ? agent.name : "Agent" : "Person"
  const verb = ({ created: "Created", updated: "Updated", commented: "Commented", shown: "Shown" } as Record<string, string>)[ref.verb] ?? "Changed"
  const locked = mutation.state === "saving" || mutation.state === "unknown" || mutation.state === "conflict"
  const path = detail.data ? `/api/v1/crews/${encodeURIComponent(detail.data.crew_id)}/issues/${encodeURIComponent(ref.entity.identifier ?? ref.entity.id)}?workspace_id=${encodeURIComponent(ref.entity.workspace_id)}` : null
  const openEditor = (kind: "fields" | "comment") => {
    setEditor(kind)
    if (kind === "fields") { const initial = { title: current.title, status: current.status, priority: current.priority, assignee_id: current.assignee_id ?? "" }; baseline.current = initial; setDraft(initial); setDraftRevision(current.revision) }
  }
  const reloadConflict = async () => {
    const fresh = await detail.refetch()
    if (fresh.error || !fresh.data?.revision) return
    const value = fresh.data
    const server = { title: value.title, status: value.status, priority: value.priority ?? "none", assignee_id: value.assignee_id ?? "" }
    const previous = baseline.current
    // Keep only actual local edits; accepting a new revision must not turn
    // untouched old fields into edits that overwrite somebody else's work.
    setDraft(local => ({
      title: local.title !== previous.title ? local.title : server.title,
      status: local.status !== previous.status ? local.status : server.status,
      priority: local.priority !== previous.priority ? local.priority : server.priority,
      assignee_id: local.assignee_id !== previous.assignee_id ? local.assignee_id : server.assignee_id,
    }))
    baseline.current = server
    setDraftRevision(value.revision)
    mutation.resolveConflict()
  }
  return <EntityCardFrame>
    {rechecking && <p role="status" className="text-xs text-muted-foreground">Checking current access…</p>}
    <div className="flex flex-wrap items-center gap-2 text-xs text-muted-foreground">
      <span>Issue</span><span className="font-mono">{ref.entity.identifier}</span>
      <span className="ml-auto">{live ? "Live" : checkedAt ? `Verified ${new Date(checkedAt).toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" })}` : "Not verified"}</span>
    </div>
    <h3 className="break-words font-medium">{current.title}</h3>
    <div className="flex flex-wrap items-center gap-x-4 gap-y-2 text-xs">
      <span className="inline-flex items-center gap-1.5"><StatusIcon status={current.status} className="size-3.5" />{statusLabel[current.status] ?? current.status}</span>
      <span className="inline-flex items-center gap-1.5"><PriorityIcon priority={current.priority} className="size-3.5" />{priorityLabel[current.priority]}</span>
      <span>{current.assignee_id ? "Assigned" : "Unassigned"}</span>
    </div>
    <p className="text-xs text-muted-foreground">{verb} by {actorName} · {new Date(ref.provenance.at).toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" })}</p>
    <div className="flex flex-wrap gap-1.5">
      {current.allowed_actions.some((a) => ["edit_fields", "assign", "change_status"].includes(a)) && <Button size="sm" variant="ghost" className="coarse:min-h-[44px]" onClick={() => openEditor("fields")}><Pencil className="size-3.5" />Edit</Button>}
      {current.allowed_actions.includes("comment") && <Button size="sm" variant="ghost" className="coarse:min-h-[44px]" onClick={() => openEditor("comment")}><MessageSquare className="size-3.5" />Comment</Button>}
      <Button size="sm" variant="ghost" className="coarse:min-h-[44px]" aria-expanded={pullRequestsOpen} onClick={() => setPullRequestsOpen(open => !open)}>Pull requests</Button>
      <Button size="sm" variant="outline" className="ml-auto coarse:min-h-[44px]" onClick={() => onOpen(ref)}>Open on right<ArrowRight className="size-3.5" /></Button>
    </div>
    {pullRequestsOpen && <div className="border-t pt-3">
      {detail.isPending ? <p role="status">Checking current access…</p> : detail.error ? <p role="alert">Pull requests unavailable. <button type="button" className="underline" onClick={() => void detail.refetch()}>Retry verification</button></p> : detail.data?.crew_id ? <ChatIssuePullRequests crewId={detail.data.crew_id} identifier={ref.entity.identifier ?? ref.entity.id} revision={current.aggregate_revision} /> : <p role="status">Pull requests unavailable</p>}
    </div>}
    {editor && <form className="space-y-3 border-t pt-3" onSubmit={async (event) => {
      event.preventDefault()
      if (!path || locked || rechecking) return
      const body: Record<string, unknown> = editor === "comment" ? { body: comment } : {
        expected_revision: draftRevision,
        ...(can("edit_fields") && draft.title !== baseline.current.title ? { title: draft.title } : {}),
        ...(can("edit_fields") && draft.priority !== baseline.current.priority ? { priority: draft.priority } : {}),
        ...(can("change_status") && draft.status !== baseline.current.status ? { status: draft.status } : {}),
        ...(can("assign") && draft.assignee_id !== baseline.current.assignee_id ? { assignee_type: draft.assignee_id ? "agent" : "", assignee_id: draft.assignee_id } : {}),
      }
      const target = editor === "comment" ? path.replace("?", "/comments?") : path
      if (await mutation.submit(target, editor === "comment" ? "POST" : "PATCH", body)) { if (editor === "comment") setComment(""); setEditor(null) }
    }}>
      <fieldset disabled={locked} className="min-w-0">{detail.isPending ? <p role="status">Checking current access…</p> : detail.error ? <p role="alert">Could not verify current access. <button type="button" className="underline" onClick={() => void detail.refetch()}>Try again</button></p> : editor === "comment" ? can("comment") && <label className="block space-y-1 text-xs">Comment<textarea required value={comment} onChange={(e) => setComment(e.target.value)} className="block min-h-24 w-full rounded-md border bg-background p-2 text-sm" /></label> : <div className="grid gap-3 sm:grid-cols-2">
        {can("edit_fields") && <><label className="space-y-1 text-xs sm:col-span-2">Title<input required value={draft.title} onChange={(e) => setDraft({ ...draft, title: e.target.value })} className="block h-9 w-full rounded-md border bg-background px-2 coarse:min-h-[44px]" /></label><label className="space-y-1 text-xs">Priority<select value={draft.priority} onChange={(e) => setDraft({ ...draft, priority: e.target.value as typeof draft.priority })} className="block h-9 w-full rounded-md border bg-background px-2 coarse:min-h-[44px]">{ALL_PRIORITIES.map((p) => <option key={p} value={p}>{priorityLabel[p]}</option>)}</select></label></>}
        {can("change_status") && <label className="space-y-1 text-xs">Status<select value={draft.status} onChange={(e) => setDraft({ ...draft, status: e.target.value as typeof draft.status })} className="block h-9 w-full rounded-md border bg-background px-2 coarse:min-h-[44px]">{ISSUE_STATUSES.map((s) => <option key={s} value={s}>{statusLabel[s]}</option>)}</select></label>}
        {can("assign") && <label className="space-y-1 text-xs">Assignee<select value={draft.assignee_id} disabled={!roster.data} onChange={(e) => setDraft({ ...draft, assignee_id: e.target.value })} className="block h-9 w-full rounded-md border bg-background px-2 coarse:min-h-[44px]"><option value="">Unassigned</option>{baseline.current.assignee_id && !roster.data?.some((a) => a.id === baseline.current.assignee_id) && <option value={baseline.current.assignee_id}>{detail.data?.assignee_name ?? "Current assignee"}</option>}{roster.data?.map((a) => <option key={a.id} value={a.id}>{a.name}</option>)}</select>{roster.error && <span role="alert">Could not load assignees</span>}</label>}
      </div>}</fieldset>
      {mutation.state === "conflict" && detail.data && <div className="space-y-1 text-xs" aria-label="Current server values"><p>Current server values</p><p>Title: {detail.data.title}</p><p>Status: {detail.data.status} · Priority: {detail.data.priority} · Assignee: {detail.data.assignee_id ?? "Unassigned"}</p><p>Your draft remains in the fields above.</p></div>}
      <CardWriteFeedback {...mutation} resolveConflict={() => void reloadConflict()} />
      <div className="flex gap-2"><Button type="submit" size="sm" className="coarse:min-h-[44px]" disabled={!detail.data || locked || rechecking || (editor === "comment" && !can("comment"))}>{mutation.state === "saving" ? <RefreshCw className="size-3.5 animate-spin" /> : null}Save</Button><Button type="button" size="sm" variant="ghost" className="coarse:min-h-[44px]" onClick={() => setEditor(null)} disabled={locked}>Cancel</Button></div>
    </form>}
  </EntityCardFrame>
}
