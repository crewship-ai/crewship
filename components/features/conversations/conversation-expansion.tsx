"use client"

import { useEffect, useRef, useState } from "react"
import { useQuery } from "@tanstack/react-query"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { randomUUIDv4 } from "@/lib/random-id"
import { ConversationRequestError, conversationRequest, type ConversationParticipant, type ConversationPerson, type WorkspaceConversation } from "@/hooks/use-workspace-conversations"
import { ConversationIdentity } from "./conversation-identity"

type Continuation = { client_id: string; kind: "group" | "channel"; title: string; member_ids: string[]; agent_id?: string }

/** Persist ambiguous attempts so reopening the dialog retries the same operation. */
export function ConversationExpansion({ workspaceId, userId, conversation, participants, kind, onCreated }: {
  workspaceId: string; userId: string; conversation: WorkspaceConversation; participants: ConversationParticipant[];
  kind: "group" | "channel"; onCreated: (conversation: WorkspaceConversation) => void
}) {
  const storageKey = JSON.stringify(["chat-continuation", workspaceId, userId, conversation.id, kind])
  const [attempt, setAttempt] = useState<Continuation | null>(() => {
    try {
      const value = JSON.parse(sessionStorage.getItem(storageKey) || "null")
      return value && value.kind === kind && typeof value.client_id === "string" && typeof value.title === "string" && Array.isArray(value.member_ids) ? value : null
    } catch { return null }
  })
  const [title, setTitle] = useState(attempt?.title || (kind === "group" ? "Group chat" : "Team chat"))
  const [memberIds, setMemberIds] = useState<string[]>(attempt?.member_ids || [])
  const [agentId, setAgentId] = useState(attempt?.agent_id || "")
  const [error, setError] = useState<string | null>(attempt ? "The previous creation was not confirmed. Retry to open the same chat safely." : null)
  const [busy, setBusy] = useState(false)
  const inFlight = useRef(false)
  const controller = useRef<AbortController | null>(null)
  useEffect(() => () => controller.current?.abort(), [])
  const people = useQuery({ queryKey: ["workspace-conversation-people", workspaceId, userId], enabled: kind === "group", queryFn: ({ signal }) => conversationRequest<{ user: ConversationPerson }[]>(workspaceId, `workspaces/${encodeURIComponent(workspaceId)}/members`, undefined, signal) })
  const agents = useQuery({ queryKey: ["conversation-agent-roster", workspaceId], enabled: kind === "channel", queryFn: ({ signal }) => conversationRequest<{ id: string; name: string; slug: string }[]>(workspaceId, "agents?limit=500", undefined, signal) })
  const rosterError = kind === "group" ? people.error : agents.error
  const loading = kind === "group" ? people.isPending : agents.isPending
  async function create() {
    if (inFlight.current || (!attempt && (!title.trim() || (kind === "group" ? !memberIds.length : !agentId)))) return
    const request: Continuation = attempt || { client_id: randomUUIDv4(), kind, title: title.trim(), member_ids: kind === "group" ? memberIds : [], ...(kind === "channel" ? { agent_id: agentId } : {}) }
    inFlight.current = true
    setBusy(true); setError(null); setAttempt(request)
    try { sessionStorage.setItem(storageKey, JSON.stringify(request)) } catch { /* In-memory retry remains available. */ }
    const abort = new AbortController(); controller.current = abort
    try {
      const result = await conversationRequest<WorkspaceConversation>(workspaceId, `conversations/${encodeURIComponent(conversation.id)}/continue`, request, abort.signal)
      if (abort.signal.aborted) return
      try { sessionStorage.removeItem(storageKey) } catch { /* Optional storage. */ }
      onCreated(result)
    } catch (cause) {
      if (abort.signal.aborted) return
      if (cause instanceof ConversationRequestError && cause.status >= 400 && cause.status < 500 && ![408, 409, 429].includes(cause.status)) {
        setAttempt(null)
        try { sessionStorage.removeItem(storageKey) } catch { /* Optional storage. */ }
      }
      setError(cause instanceof Error ? cause.message : "Unable to create chat. Please retry.")
    } finally { inFlight.current = false; if (!abort.signal.aborted) setBusy(false) }
  }
  return <form className="space-y-4" onSubmit={(event) => { event.preventDefault(); void create() }}>
    <p className="text-sm text-muted-foreground">{kind === "group" ? "The original two participants are included. Your direct message and its history stay private; the new group starts with an empty history." : "The current participants are included. The new channel and the agent’s work will be visible to everyone in this workspace. Your private chat and its history stay private. The agent responds when you mention it in the new channel."}</p>
    <label className="block space-y-1 text-sm"><span>Name</span><Input autoFocus required maxLength={120} value={title} disabled={busy || !!attempt} onChange={(event) => setTitle(event.target.value)} /></label>
    {loading && !attempt && <p className="text-sm">Loading {kind === "group" ? "people" : "agents"}…</p>}
    {rosterError && <p role="alert" className="text-sm">Unable to load {kind === "group" ? "people" : "agents"}. <Button type="button" variant="ghost" onClick={() => { void (kind === "group" ? people.refetch() : agents.refetch()) }}>Retry loading</Button></p>}
    {kind === "group" ? <fieldset disabled={busy || !!attempt} className="space-y-2"><legend className="text-sm font-medium">Add people</legend><div className="max-h-52 overflow-y-auto">{people.data?.filter(({ user }) => user.id !== userId && !participants.some((participant) => participant.user_id === user.id)).map(({ user }) => <label key={user.id} className="flex min-h-11 items-center gap-3 text-sm"><input type="checkbox" checked={memberIds.includes(user.id)} onChange={(event) => setMemberIds((old) => event.target.checked ? [...old, user.id] : old.filter((id) => id !== user.id))} /><ConversationIdentity id={user.id} name={user.full_name || user.email} avatarUrl={user.avatar_url} className="size-7" /><span>{user.full_name || user.email}</span></label>)}</div></fieldset> : <label className="block space-y-1 text-sm"><span>Agent</span><select aria-label="Agent" className="w-full rounded-md border bg-background p-2" disabled={busy || !!attempt} value={agentId} onChange={(event) => setAgentId(event.target.value)}><option value="">Choose an agent…</option>{agents.data?.map((agent) => <option key={agent.id} value={agent.id}>{agent.name}</option>)}</select>{agents.data?.length === 500 && <span className="text-xs text-muted-foreground">Showing the first 500 agents.</span>}</label>}
    {error && <p role="alert" className="text-sm text-destructive">{error}</p>}
    <Button type="submit" disabled={busy || (!attempt && (loading || !!rosterError || !title.trim() || (kind === "group" ? !memberIds.length : !agentId)))}>{busy ? "Creating…" : attempt ? "Retry creation" : kind === "group" ? "Create group" : "Create workspace channel"}</Button>
  </form>
}
