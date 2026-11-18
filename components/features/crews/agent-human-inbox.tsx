"use client"

import { useCallback, useEffect, useRef, useState } from "react"
import Link from "next/link"
import { useQuery } from "@tanstack/react-query"
import { apiFetch } from "@/lib/api-fetch"
import { useAccessMode } from "@/hooks/use-access-mode"
import { useSessionSafe } from "@/hooks/use-auth"
import { useRealtimeEvent } from "@/hooks/use-realtime"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Textarea } from "@/components/ui/textarea"
import { CrCard, CrNote } from "./cr-card"

type Entry = { id: string; kind: string; content: string; href: string; state: string; read_at: string | null }
type Room = { id: string; kind: string; title: string }

export function AgentHumanInbox({ enabled, workspaceId, agentId, agentSlug, crewId }: { enabled: boolean; workspaceId: string; agentId: string; agentSlug: string; crewId?: string | null }) {
  const { data: session } = useSessionSafe()
  const user = session?.user?.id ?? ""
  const accessMode = useAccessMode()
  // Remounting resets composers and pending request generations on identity or
  // workspace changes; no previous viewer's source text is used as placeholder.
  if (!enabled) return null
  if (accessMode !== "trusted") return <CrCard title="Human inbox" concept="chat"><CrNote>Human messages are unavailable in this session.</CrNote></CrCard>
  return <HumanInbox key={`${user}:${workspaceId}:${agentId}`} workspaceId={workspaceId} agentId={agentId} agentSlug={agentSlug} crewId={crewId} user={user} />
}

function HumanInbox({ workspaceId, agentId, agentSlug, crewId, user }: { workspaceId: string; agentId: string; agentSlug: string; crewId?: string | null; user: string }) {
  const generation = useRef(0)
  const disabled = useRef(false)
  const [revision, setRevision] = useState(0)
  const key = ["agent-human-inbox", workspaceId, agentId, user, revision]
  const qs = `workspace_id=${encodeURIComponent(workspaceId)}`
  const [mode, setMode] = useState<"message" | "task" | null>(null)
  const [room, setRoom] = useState("")
  const [text, setText] = useState("")
  const [title, setTitle] = useState("")
  const [notice, setNotice] = useState<string | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [busy, setBusy] = useState(false)
  const alive = useRef(true)
  const request = useRef<AbortController | null>(null)
  const clientId = useRef(crypto.randomUUID())
  const acceptedIssue = useRef<string | null>(null)
  useEffect(() => { alive.current = true; return () => { alive.current = false; request.current?.abort() } }, [])
  const invalidate = useCallback(() => { generation.current += 1; setRevision(generation.current) }, [])
  const refreshEnabledInbox = useCallback(() => {
    if (!disabled.current) invalidate()
  }, [invalidate])
  useRealtimeEvent("inbox.updated", refreshEnabledInbox)
  useRealtimeEvent("conversation.updated", refreshEnabledInbox)
  useRealtimeEvent("issue.updated", refreshEnabledInbox)
  useRealtimeEvent("run.completed", refreshEnabledInbox)
  const items = useQuery<{ items: Entry[] } | null>({
    queryKey: key, enabled: Boolean(user), gcTime: 0, staleTime: 0, retry: false,
    refetchInterval: query => query.state.data ? 30000 : false,
    queryFn: async ({ signal }) => {
      const res = await apiFetch(`/api/v1/agents/${encodeURIComponent(agentId)}/human-inbox?${qs}`, { signal })
      if (signal.aborted || !alive.current || revision !== generation.current) throw new Error("Source audience changed")
      if (res.status === 404) {
        disabled.current = true
        return null
      }
      if (!res.ok) throw new Error("Messages could not be loaded")
      disabled.current = false
      const data = await res.json()
      if (signal.aborted || !alive.current || revision !== generation.current) throw new Error("Source audience changed")
      return data
    },
  })
  const rooms = useQuery<{ conversations: Room[] }>({
    queryKey: ["agent-inbox-compose-rooms", workspaceId, user], enabled: mode === "message" && Boolean(user), gcTime: 0,
    queryFn: async ({ signal }) => { const res = await apiFetch(`/api/v1/conversations?${qs}`, { signal }); if (!res.ok) throw new Error("Channels could not be loaded"); return res.json() },
  })
  async function submit() {
    if (!text.trim() || busy || (mode === "message" && !room) || (mode === "task" && (!crewId || !title.trim()))) return
    const abort = new AbortController(); request.current = abort; setBusy(true); setError(null); setNotice(null)
    const post = async (path: string, body: unknown) => {
      const res = await apiFetch(`${path}?${qs}`, { method: "POST", signal: abort.signal, headers: { "Content-Type": "application/json", "Idempotency-Key": clientId.current + (path.endsWith("/start") ? ":start" : "") }, body: JSON.stringify(body) })
      const data = await res.json().catch(() => ({})); if (!res.ok) throw Object.assign(new Error(data.error || "The request could not be sent"), { status: res.status }); return data
    }
    try {
      if (mode === "message") {
        await post(`/api/v1/conversations/${encodeURIComponent(room)}/messages`, { content: text, client_id: clientId.current, mentioned_agent_ids: [agentId] })
      } else if (mode === "task" && crewId) {
        if (!acceptedIssue.current) {
          const created = await post(`/api/v1/crews/${encodeURIComponent(crewId)}/issues`, { title, description: text, delegate_agent_id: agentId })
          if (!created.identifier) throw new Error("The created issue has no identifier")
          acceptedIssue.current = created.identifier
        }
        if (!alive.current) return
        const path = `/api/v1/crews/${encodeURIComponent(crewId)}/issues/${encodeURIComponent(acceptedIssue.current!)}`
        try { await post(`${path}/start`, {}) }
        catch (failure) {
          const status = (failure as { status?: number }).status
          if (status !== undefined && status !== 400) throw failure
          // A lost response is not proof of a new execution. Read back only the
          // current canonical issue; do not retry start or suppress auth denial.
          const current = await apiFetch(`${path}?${qs}`, { signal: abort.signal })
          if (!current.ok) throw failure
          const observed = await current.json()
          if (observed.delegate_agent_id !== agentId || !["IN_PROGRESS", "REVIEW", "DONE", "CANCELLED"].includes(observed.status)) throw failure
          if (!alive.current) return
          setNotice(`Issue ${acceptedIssue.current} is currently ${observed.status}. Existing state observed; start acknowledgement unavailable.`)
        }
      }
      if (!alive.current) return
      setMode(null); setText(""); setTitle(""); clientId.current = crypto.randomUUID(); acceptedIssue.current = null
      invalidate()
    } catch (e) { if (alive.current && !abort.signal.aborted) setError(e instanceof Error ? e.message : "Send failed") }
    finally { if (alive.current) setBusy(false) }
  }
  if (items.data === null || items.isPending || !user) return null
  return <CrCard title="Human inbox" concept="chat" data-testid="agent-human-inbox">
    <div className="flex flex-wrap gap-2"><Button variant="outline" size="sm" disabled={busy || Boolean(acceptedIssue.current)} onClick={() => setMode("message")}>Send message</Button><Button variant="outline" size="sm" disabled={!crewId || busy} onClick={() => setMode("task")}>Assign task</Button><Button asChild size="sm" variant="ghost"><Link href={`/chat/${encodeURIComponent(agentSlug)}?new=1&${qs}`}>Private chat</Link></Button></div>
    {notice && <p role="status" className="text-sm">{notice}</p>}
    {mode && <form className="space-y-2" onSubmit={e => { e.preventDefault(); void submit() }}>
      {mode === "message" ? <label className="block text-sm">Workspace channel<select aria-label="Workspace channel" value={room} onChange={e => { setRoom(e.target.value); clientId.current = crypto.randomUUID() }} className="block w-full rounded border bg-background p-2"><option value="">Choose an existing channel</option>{rooms.data?.conversations.filter(c => c.kind === "channel").map(c => <option key={c.id} value={c.id}>{c.title}</option>)}</select><span className="text-xs text-muted-foreground">The agent must already be joined to this channel. Its existing audience can read the message.</span></label> : <Input aria-label="Task title" placeholder="Task title" value={title} disabled={Boolean(acceptedIssue.current)} onChange={e => { setTitle(e.target.value); clientId.current = crypto.randomUUID() }} />}
      <Textarea aria-label={mode === "message" ? "Message" : "Task instructions"} value={text} disabled={Boolean(acceptedIssue.current)} onChange={e => { setText(e.target.value); clientId.current = crypto.randomUUID() }} />
      {error && <p role="alert">{error}{acceptedIssue.current && ` Issue ${acceptedIssue.current} was created; retry checks or starts that same issue.`}</p>}
      <Button type="submit" disabled={busy}>{busy ? "Sending…" : mode === "task" ? "Create and start" : "Send"}</Button><Button type="button" variant="ghost" disabled={busy} onClick={() => { setMode(null); acceptedIssue.current = null; clientId.current = crypto.randomUUID() }}>Cancel</Button>
    </form>}
    {items.error ? <CrNote tone="error">Human inbox could not be loaded.</CrNote> : items.isFetching ? <CrNote>Loading human messages…</CrNote> : !items.data?.items.length ? <CrNote>No visible human messages or assigned tasks.</CrNote> : <ul className="space-y-3">{items.data.items.map(e => <li key={`${e.kind}:${e.id}`}><Link href={e.href} className="text-sm underline">{e.content.slice(0, 240)}</Link><p className="text-xs text-muted-foreground">{e.kind === "issue" ? "Task" : "Message"} · {e.state} · {e.read_at ? `Agent read ${e.read_at}` : "Agent read not yet verified"}</p></li>)}</ul>}
  </CrCard>
}
