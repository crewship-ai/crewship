"use client"

import { useEffect, useRef, useState } from "react"
import { useSearchParams } from "next/navigation"
import { useQuery, useQueryClient } from "@tanstack/react-query"
import { ArrowLeft, Bell, BellOff, MessageSquare, Plus, Send, Users } from "lucide-react"
import { useWorkspace } from "@/hooks/use-workspace"
import { useSessionSafe } from "@/hooks/use-auth"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Textarea } from "@/components/ui/textarea"
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from "@/components/ui/dialog"
import { MentionAutocomplete, type CrewMember } from "@/components/features/chat/composer/mention-autocomplete"
import { ConversationIdentity, ConversationIcon, conversationTitle } from "./conversation-identity"
import { ConversationTranscript } from "./conversation-transcript"
import { ConversationExpansion } from "./conversation-expansion"
import { ConversationActivity } from "./conversation-activity"
import { DirectMessagePicker } from "./direct-message-picker"
import { ConversationAgents, type JoinedAgent } from "./conversation-agents"
import { apiFetch } from "@/lib/api-fetch"
import { setSoundReadingConversation } from "@/lib/notification-sound-coordinator"
import { randomUUIDv4 } from "@/lib/random-id"
import { cn } from "@/lib/utils"
import { ConversationRequestError, conversationRequest, useConversationMessages, useWorkspaceConversations, type ConversationParticipant, type ConversationPerson, type WorkspaceConversation, type WorkspaceMessage } from "@/hooks/use-workspace-conversations"

export function WorkspaceConversations() {
  const { workspaceId, loading } = useWorkspace()
  const { data: session } = useSessionSafe()
  if (loading) return <p className="p-6">Loading workspace…</p>
  if (!workspaceId || !session?.user.id) return <p className="p-6">Select a workspace to open conversations.</p>
  return <ConversationWorkspace key={`${workspaceId}:${session.user.id}`} workspaceId={workspaceId} userId={session.user.id} />
}

function ConversationWorkspace({ workspaceId, userId }: { workspaceId: string; userId: string }) {
  const params = useSearchParams()
  const [selectedId, setSelectedId] = useState<string | null>(params.get("conversation"))
  const [creating, setCreating] = useState(false)
  const [startingDirect, setStartingDirect] = useState(false)
  const queryClient = useQueryClient()
  const [search, setSearch] = useState("")
  const { list, refresh } = useWorkspaceConversations(workspaceId, userId)
  const conversations = list.data?.pages.flatMap((page) => page.conversations)
  const selectedQuery = useQuery({ queryKey: ["workspace-conversations", workspaceId, userId, selectedId, "detail"], enabled: !!selectedId, queryFn: ({ signal }) => conversationRequest<WorkspaceConversation>(workspaceId, `conversations/${encodeURIComponent(selectedId!)}`, undefined, signal) })
  const selected = selectedQuery.isError ? undefined : selectedQuery.data ?? conversations?.find((c) => c.id === selectedId)
  useEffect(() => {
    const read = () => setSelectedId(new URLSearchParams(window.location.search).get("conversation"))
    window.addEventListener("popstate", read)
    return () => window.removeEventListener("popstate", read)
  }, [])
  function select(id: string | null) {
    setSelectedId(id)
    const url = new URL(window.location.href)
    if (id) url.searchParams.set("conversation", id)
    else url.searchParams.delete("conversation")
    window.history.replaceState(null, "", url)
  }
  function openedDirect(conversation: WorkspaceConversation) {
    queryClient.setQueryData(["workspace-conversations", workspaceId, userId, conversation.id, "detail"], conversation)
    select(conversation.id)
    setStartingDirect(false)
    refresh()
  }
  return <div className="flex h-[calc(100dvh-var(--app-header-h)-var(--mobile-tab-bar-h))] min-h-0 overflow-hidden">
    <aside aria-label="Workspace conversations" className={cn("w-full shrink-0 flex-col border-r bg-muted/10 md:flex md:w-72", selectedId ? "hidden" : "flex")}>
      <div className="space-y-3 border-b p-4">
        <a href="/chat" className="inline-flex items-center gap-2 text-sm text-muted-foreground hover:text-foreground"><ArrowLeft className="size-4" />Agent chats</a>
        <div className="flex items-center justify-between"><h1 className="font-semibold">People &amp; channels</h1><Button type="button" size="icon" variant="ghost" aria-label="New group or channel" onClick={() => setCreating(true)}><Plus className="size-4" /></Button></div>
        <Button type="button" variant="outline" className="w-full justify-start" onClick={() => setStartingDirect(true)}><MessageSquare className="mr-2 size-4" />New direct message</Button>
        <Input aria-label="Search conversations" placeholder="Find a conversation…" value={search} onChange={(e) => setSearch(e.target.value)} />
      </div>
      <div className="min-h-0 flex-1 overflow-y-auto p-2">
        {list.isPending && <p className="p-3 text-sm">Loading conversations…</p>}
        {list.error && <ErrorNotice error={list.error} retry={() => { void list.refetch() }} />}
        {conversations?.filter((c) => c.title.toLowerCase().includes(search.toLowerCase())).map((c) => <button key={c.id} onClick={() => select(c.id)} aria-current={selectedId === c.id ? "page" : undefined} className={cn("mb-1 flex min-h-12 w-full items-center gap-3 rounded-lg px-3 text-left text-sm hover:bg-accent", selectedId === c.id && "bg-accent")}>
          <ConversationIcon conversation={c} /><span className="min-w-0 flex-1"><span className="block truncate">{conversationTitle(c)}</span><span className="block text-xs text-muted-foreground">{conversationLabel(c)}</span></span>{c.unread_count > 0 && <span aria-label={`${c.unread_count} unread`} className="rounded-full bg-primary px-2 py-0.5 text-xs text-primary-foreground">{c.unread_count}</span>}
        </button>)}
        {list.hasNextPage && <Button type="button" variant="ghost" disabled={list.isFetchingNextPage} onClick={() => { void list.fetchNextPage() }}>Load more conversations</Button>}
        {conversations?.length === 0 && <div className="space-y-3 p-3 text-sm text-muted-foreground"><p>Start a group for selected people, or a channel everyone in this workspace can read.</p><Button type="button" variant="outline" onClick={() => setCreating(true)}>New conversation</Button></div>}
      </div>
    </aside>
    <main className={cn("min-w-0 flex-1", selectedId ? "flex flex-col" : "hidden md:flex md:flex-col")}>
      {selected ? <ConversationThread key={selected.id} conversation={selected} workspaceId={workspaceId} userId={userId} refresh={refresh} onBack={() => select(null)} onOpenConversation={(c) => { refresh(); select(c.id) }} /> : <div className="flex flex-1 flex-col items-center justify-center gap-3 p-8 text-center"><Users className="size-10 text-muted-foreground" /><h2 className="text-lg font-medium">Conversations for your workspace</h2><p className="max-w-md text-sm text-muted-foreground">Choose a conversation or bring people together in a new group.</p>{selectedId && !list.isPending && <p role="alert" className="text-sm">This conversation is unavailable or you no longer have access.</p>}<Button type="button" onClick={() => setCreating(true)}>New conversation</Button><Button type="button" variant="ghost" className="md:hidden" onClick={() => select(null)}>Back to conversations</Button></div>}
    </main>
    <Dialog open={startingDirect} onOpenChange={setStartingDirect}><DialogContent><DialogHeader><DialogTitle>New direct message</DialogTitle><DialogDescription>Choose a colleague. Your existing direct conversation opens automatically; only the two of you can access it.</DialogDescription></DialogHeader>{startingDirect && <DirectMessagePicker workspaceId={workspaceId} userId={userId} onOpened={openedDirect} />}</DialogContent></Dialog>
    <Dialog open={creating} onOpenChange={setCreating}><DialogContent><DialogHeader><DialogTitle>New conversation</DialogTitle><DialogDescription>Groups are for selected people. Channels are visible to everyone in this workspace.</DialogDescription></DialogHeader>{creating && <CreateConversation workspaceId={workspaceId} userId={userId} onCreated={(c) => { refresh(); select(c.id); setCreating(false) }} />}</DialogContent></Dialog>
  </div>
}

function ErrorNotice({ error, retry }: { error: Error; retry: () => void }) {
  return <div role="alert" className="space-y-2 rounded-lg border p-3 text-sm"><p>{error.message}</p><Button type="button" variant="outline" size="sm" onClick={retry}>Retry</Button></div>
}

export function CreateConversation({ workspaceId, userId, onCreated, initialKind = "group" }: { workspaceId: string; userId: string; initialKind?: "group" | "channel"; onCreated: (c: WorkspaceConversation) => void }) {
  const [title, setTitle] = useState("")
  const [kind, setKind] = useState<"group" | "channel">(initialKind)
  const [members, setMembers] = useState<string[]>([])
  const [busy, setBusy] = useState(false)
  const creatingRef = useRef(false)
  const requestRef = useRef<AbortController | null>(null)
  useEffect(() => () => { requestRef.current?.abort(); creatingRef.current = false }, [workspaceId, userId])
  const [error, setError] = useState<Error | null>(null)
  const people = useQuery({ queryKey: ["workspace-conversation-people", workspaceId, userId], queryFn: ({ signal }) => conversationRequest<{ user: ConversationPerson }[]>(workspaceId, `workspaces/${encodeURIComponent(workspaceId)}/members`, undefined, signal) })
  async function create() {
    if (creatingRef.current || !title.trim()) return
    creatingRef.current = true
    const controller = new AbortController()
    requestRef.current = controller
    setBusy(true); setError(null)
    try {
      const result = await conversationRequest<WorkspaceConversation>(workspaceId, "conversations", { title: title.trim(), kind, member_ids: kind === "group" ? members : [] }, controller.signal)
      if (!controller.signal.aborted) onCreated(result)
    }
    catch (e) { if (!controller.signal.aborted) setError(e instanceof Error ? e : new Error("Unable to create conversation.")) }
    finally { if (requestRef.current === controller) creatingRef.current = false; if (!controller.signal.aborted) setBusy(false) }
  }
  return <form className="space-y-4" onSubmit={(e) => { e.preventDefault(); void create() }}>
    <label className="block space-y-1 text-sm"><span>Name</span><Input autoFocus maxLength={120} required value={title} onChange={(e) => setTitle(e.target.value)} placeholder="Design team" /></label>
    <label className="block space-y-1 text-sm"><span>Who can access this conversation?</span><select className="w-full rounded-md border bg-background p-2" value={kind} onChange={(e) => setKind(e.target.value as "group" | "channel")}><option value="group">Selected people</option><option value="channel">Everyone in the workspace</option></select></label>
    {kind === "group" && <fieldset className="space-y-2"><legend className="mb-2 text-sm font-medium">Add people · you are included</legend>{people.isPending && <p className="text-sm">Loading people…</p>}{people.error && <ErrorNotice error={people.error} retry={() => { void people.refetch() }} />}<div className="max-h-52 overflow-y-auto">{people.data?.filter((p) => p.user.id !== userId).map(({ user }) => <label key={user.id} className="flex min-h-10 items-center gap-3 text-sm"><input type="checkbox" checked={members.includes(user.id)} onChange={(e) => setMembers((old) => e.target.checked ? [...old, user.id] : old.filter((id) => id !== user.id))} /><ConversationIdentity id={user.id} name={user.full_name || user.email} avatarUrl={user.avatar_url} className="size-7" /><span>{user.full_name || user.email}</span></label>)}</div></fieldset>}
    {error && <p role="alert" className="text-sm text-destructive">{error.message}</p>}<Button type="submit" disabled={busy || !title.trim() || (kind === "group" && (people.isPending || !!people.error))}>{busy ? "Creating…" : "Create conversation"}</Button>
  </form>
}

export function ConversationThread({ conversation, workspaceId, userId, refresh, onBack, onOpenConversation }: { conversation: WorkspaceConversation; workspaceId: string; userId: string; refresh: () => void; onBack: () => void; onOpenConversation?: (c: WorkspaceConversation) => void }) {
  const messages = useConversationMessages(workspaceId, userId, conversation.id)
  const draftKey = JSON.stringify(["workspace-conversation-draft", workspaceId, userId, conversation.id])
  const [text, setText] = useState(() => readDraft(draftKey).text)
  const [pending, setPending] = useState<PendingSend | null>(() => readDraft(draftKey).pending)
  const [mentionedAgents, setMentionedAgents] = useState<string[]>([])
  const textareaRef = useRef<HTMLTextAreaElement>(null)
  const automaticMentions = useRef(new Map<string, string>())
  const [sending, setSending] = useState(false)
  const [error, setError] = useState<Error | null>(() => readDraft(draftKey).pending ? new Error("The previous send was not confirmed. Retry safely with the same message ID.") : null)
  useEffect(() => {
    try { sessionStorage.setItem(draftKey, JSON.stringify({ text, pending })) } catch { /* Storage may be unavailable. */ }
  }, [draftKey, text, pending])
  const agentRoster = useQuery({ queryKey: ["workspace-conversations", workspaceId, userId, conversation.id, "agents"], enabled: conversation.kind === "channel", queryFn: ({ signal }) => conversationRequest<{ agents: JoinedAgent[] }>(workspaceId, `conversations/${encodeURIComponent(conversation.id)}/agents`, undefined, signal) })
  const jobs = useQuery({ queryKey: ["workspace-conversations", workspaceId, userId, conversation.id, "agent-jobs"], enabled: conversation.kind === "channel", queryFn: ({ signal }) => conversationRequest<{ jobs: { id: string; agent_id: string; state: string; error?: string }[] }>(workspaceId, `conversations/${encodeURIComponent(conversation.id)}/agent-jobs`, undefined, signal), refetchInterval: 120_000 })
  const [showPeople, setShowPeople] = useState(false)
  const [expanding, setExpanding] = useState<"group" | "channel" | null>(null)
  const [readError, setReadError] = useState(false)
  const [muting, setMuting] = useState(false)
  const [muteError, setMuteError] = useState<string | null>(null)
  const readCursor = useRef(0)
  const bottom = useRef<HTMLDivElement>(null)
  const [atBottom, setAtBottom] = useState(true)
  useEffect(() => {
    const scope = JSON.stringify([userId, workspaceId])
    setSoundReadingConversation(scope, conversation.id, atBottom && !messages.isError)
    return () => setSoundReadingConversation(scope, conversation.id, false)
  }, [userId, workspaceId, conversation.id, atBottom, messages.isError])
  const people = useQuery({ queryKey: ["workspace-conversations", workspaceId, userId, conversation.id, "participants"], queryFn: ({ signal }) => conversationRequest<{ participants: ConversationParticipant[] }>(workspaceId, `conversations/${encodeURIComponent(conversation.id)}/participants`, undefined, signal), enabled: showPeople })
  const history = messages.data ? [...new Map(messages.data.pages.flatMap((page) => page.messages).map((m) => [m.id, m])).values()].sort((a, b) => a.sequence - b.sequence) : undefined
  const lastSequence = history?.at(-1)?.sequence ?? 0
  useEffect(() => { if (atBottom) bottom.current?.scrollIntoView({ block: "end" }) }, [lastSequence, atBottom])
  useEffect(() => {
    const markRead = () => {
      if (!atBottom || document.visibilityState !== "visible" || !document.hasFocus() || messages.isError || lastSequence <= readCursor.current) return
      readCursor.current = lastSequence
      void conversationRequest(workspaceId, `conversations/${encodeURIComponent(conversation.id)}/read`, { last_read_sequence: lastSequence }).then(() => { setReadError(false); refresh() }).catch(() => { readCursor.current = 0; setReadError(true) })
    }
    markRead()
    document.addEventListener("visibilitychange", markRead)
    window.addEventListener("focus", markRead)
    return () => { document.removeEventListener("visibilitychange", markRead); window.removeEventListener("focus", markRead) }
  }, [conversation.id, workspaceId, lastSequence, messages.isError, refresh, atBottom])
  async function toggleMute() {
    if (muting) return
    setMuting(true); setMuteError(null)
    try {
      await conversationRequest(workspaceId, `conversations/${encodeURIComponent(conversation.id)}/mute`, { muted: !conversation.muted })
      refresh()
    } catch { setMuteError("Unable to update notifications. Please retry.") }
    finally { setMuting(false) }
  }
  function updateText(value: string) {
    setText(value)
    for (const [id, slug] of automaticMentions.current) {
      const escaped = slug.replace(/[.*+?^${}()|[\]\\]/g, "\\$&")
      if (!new RegExp(`(?:^|\\s)@${escaped}(?=\\s|$|[.,!?;:])`).test(value)) {
        automaticMentions.current.delete(id)
        setMentionedAgents((old) => old.filter((target) => target !== id))
      }
    }
  }
  function pickMention(member: CrewMember, atIndex: number) {
    const caret = textareaRef.current?.selectionStart ?? text.length
    const next = `${text.slice(0, atIndex)}@${member.slug} ${text.slice(caret)}`
    setText(next)
    automaticMentions.current.set(member.id, member.slug)
    setMentionedAgents((old) => old.includes(member.id) ? old : [...old, member.id])
    requestAnimationFrame(() => {
      textareaRef.current?.focus()
      textareaRef.current?.setSelectionRange(atIndex + member.slug.length + 2, atIndex + member.slug.length + 2)
    })
  }
  async function send(retry = false) {
    if (sending) return
    const request = retry ? pending : { client_id: randomUUIDv4(), content: text.trim(), ...(mentionedAgents.length ? { mentioned_agent_ids: mentionedAgents } : {}) }
    if (!request?.content) return
    if (new TextEncoder().encode(request.content).length > 32768) { setError(new Error("Message is too long. Shorten it to 32 KB or less.")); return }
    setPending(request); setSending(true); setError(null)
    try {
      await conversationRequest<WorkspaceMessage>(workspaceId, `conversations/${encodeURIComponent(conversation.id)}/messages`, request)
      setText(""); setPending(null); setMentionedAgents([]); automaticMentions.current.clear(); refresh()
    } catch (e) {
      if (e instanceof ConversationRequestError && e.status >= 400 && e.status < 500 && e.status !== 408 && e.status !== 429) setPending(null)
      setError(e instanceof Error ? e : new Error("Unable to send message."))
    }
    finally { setSending(false) }
  }
  return <>
    <header className="flex min-h-16 items-center gap-3 border-b px-4"><Button type="button" variant="ghost" size="icon" className="md:hidden" aria-label="Back to conversations" onClick={onBack}><ArrowLeft className="size-4" /></Button><ConversationIcon conversation={conversation} /><div className="min-w-0 flex-1"><h2 className="truncate font-semibold">{conversationTitle(conversation)}</h2><p className="text-xs text-muted-foreground line-clamp-2">{conversationLabel(conversation)} · {conversation.access_scope === "workspace" ? "Visible to everyone in this workspace" : conversation.is_direct ? "Only the two participants have access" : "Only conversation members can access this group"}</p></div><Button type="button" variant="ghost" disabled={muting} aria-pressed={!!conversation.muted} title="Mute inbox notifications for this conversation. Messages and unread counts remain visible." aria-label={conversation.muted ? "Unmute" : "Mute"} onClick={() => { void toggleMute() }}>{conversation.muted ? <BellOff className="size-4" /> : <Bell className="size-4" />}<span className="hidden lg:inline">{conversation.muted ? "Unmute" : "Mute"}</span></Button><Button type="button" variant="ghost" aria-label="People" onClick={() => setShowPeople(true)}><Users className="size-4" /><span className="hidden sm:inline">People</span></Button></header>
    <div className="min-h-0 flex-1 overflow-y-auto px-4 py-6" onScroll={(e) => { const el = e.currentTarget; setAtBottom(el.scrollHeight - el.scrollTop - el.clientHeight < 80) }}><div className="mx-auto max-w-4xl">
      {messages.isPending && <p className="text-sm">Loading messages…</p>}{messages.error && <ErrorNotice error={messages.error} retry={() => { void messages.refetch() }} />}
      {messages.hasNextPage && <Button type="button" variant="outline" disabled={messages.isFetchingNextPage} onClick={() => { void messages.fetchNextPage() }}>Load older messages</Button>}
      {!messages.isError && history && <ConversationTranscript history={history} userId={userId} agentNames={new Map(agentRoster.data?.agents.map((agent) => [agent.agent_id, agent.name]))} />}
      {history?.length === 0 && <p className="py-10 text-center text-sm text-muted-foreground">Start the conversation with a message.</p>}
      {error && <div className="space-y-2 rounded-lg border p-3"><p className="whitespace-pre-wrap text-sm">{pending?.content}</p>{pending ? <ErrorNotice error={error} retry={() => { void send(true) }} /> : <p role="alert" className="text-sm text-destructive">{error.message}</p>}</div>}
      <div ref={bottom} />
    </div></div>
    <form className="mx-auto w-full max-w-4xl space-y-2 px-4 pb-4 pt-2" onSubmit={(e) => { e.preventDefault(); void send() }}>
      {!atBottom && <Button type="button" variant="outline" size="sm" onClick={() => { setAtBottom(true); bottom.current?.scrollIntoView({ block: "end" }) }}>Jump to latest</Button>}
      {muteError && <p role="alert" className="text-xs text-destructive">{muteError}</p>}
      {conversation.muted && <p className="text-xs text-muted-foreground">Inbox notifications are muted for you. Messages remain visible.</p>}
      {readError && <p role="status" className="text-xs text-muted-foreground">Read status could not be saved. It will retry when you return to this conversation.</p>}
      {conversation.kind === "channel" && agentRoster.data && agentRoster.data.agents.length > 0 && <fieldset className="flex flex-wrap gap-3 text-xs"><legend className="mb-2 text-muted-foreground">Ask agents to respond to this message</legend>{agentRoster.data.agents.map((agent) => <label key={agent.agent_id} className={cn("flex min-h-9 cursor-pointer items-center gap-2 rounded-full border px-2 py-1 transition-colors", mentionedAgents.includes(agent.agent_id) ? "border-primary/40 bg-primary/10 text-foreground" : "border-border bg-muted/30 text-muted-foreground", (sending || !!pending) && "cursor-not-allowed opacity-60")}><input type="checkbox" className="size-3.5 accent-primary" disabled={sending || !!pending} checked={mentionedAgents.includes(agent.agent_id)} onChange={(e) => { automaticMentions.current.delete(agent.agent_id); setMentionedAgents((old) => e.target.checked ? [...old, agent.agent_id] : old.filter((id) => id !== agent.agent_id)) }} /><ConversationIdentity id={agent.agent_id} name={agent.name} slug={agent.avatar_seed || agent.slug} avatarUrl={agent.avatar_url} avatarStyle={agent.avatar_style} agent className="size-6" />@{agent.slug || agent.name}</label>)}</fieldset>}
      {agentRoster.error && <p role="status" className="text-xs text-muted-foreground">Channel agents could not be loaded. You can still send a message to people.</p>}
      {jobs.data?.jobs.filter((job) => job.state !== "completed").slice(0, 5).map((job) => <p key={job.id} role="status" className="text-xs text-muted-foreground">{agentRoster.data?.agents.find((a) => a.agent_id === job.agent_id)?.name || "Agent"}: {job.state}{job.error ? ` · ${job.error}` : ""}</p>)}
      {jobs.error && <p role="status" className="text-xs text-muted-foreground">Agent response status is unavailable. <button type="button" className="underline" onClick={() => { void jobs.refetch() }}>Retry</button></p>}
      <div className="relative rounded-xl border bg-muted/30 p-1 shadow-sm focus-within:border-primary/50">{conversation.kind === "channel" && !sending && !pending && !messages.isError && <MentionAutocomplete text={text} textareaRef={textareaRef} members={(agentRoster.data?.agents ?? []).map((agent) => ({ id: agent.agent_id, slug: agent.slug || agent.name, name: agent.name, kind: "agent" }))} onPick={pickMention} />}<label className="sr-only" htmlFor="workspace-message">Message {conversationTitle(conversation)}</label><Textarea ref={textareaRef} id="workspace-message" className="min-h-24 resize-y border-0 bg-transparent shadow-none focus-visible:ring-0" value={text} disabled={sending || !!pending || messages.isError} maxLength={32000} placeholder={`Message ${conversation.kind === "channel" ? "#" : ""}${conversationTitle(conversation)}${conversation.kind === "channel" ? " · @ to ask an agent" : ""}`} onChange={(e) => updateText(e.target.value)} onKeyDown={(e) => { if (e.key === "Enter" && !e.shiftKey && !e.nativeEvent.isComposing) { e.preventDefault(); void send() } }} /></div>
      <div className="flex items-center justify-between gap-3"><span className="text-xs text-muted-foreground">Enter to send · Shift+Enter for a new line</span><Button type="submit" disabled={sending || !!pending || !text.trim() || messages.isError}><Send className="mr-2 size-4" />{sending ? "Sending…" : "Send"}</Button></div>
    </form>
    <Dialog open={showPeople} onOpenChange={setShowPeople}><DialogContent><DialogHeader><DialogTitle>{conversation.is_direct ? "Direct message participants" : "Conversation people"}</DialogTitle><DialogDescription>{conversation.access_scope === "workspace" ? "All workspace members can read and write in this channel." : conversation.is_direct ? "This direct message is between two people. To include others, create a separate group." : "These people have access to the conversation and its history."}</DialogDescription></DialogHeader>{people.isPending && <p>Loading people…</p>}{people.error && <ErrorNotice error={people.error} retry={() => { void people.refetch() }} />}<div className="max-h-72 space-y-2 overflow-y-auto">{people.data?.participants.map((person) => <div key={person.user_id} className="flex items-center gap-3 text-sm"><ConversationIdentity id={person.user_id} name={person.name || "Workspace member"} avatarUrl={person.avatar_url} className="size-8" /><span className="min-w-0 flex-1">{person.name || "Workspace member"}</span><span className="text-muted-foreground">{person.role}</span></div>)}</div>{conversation.kind === "group" && people.data && onOpenConversation && <div className="flex flex-wrap gap-2 border-t pt-3">{conversation.is_direct && <Button type="button" variant="outline" onClick={() => { setShowPeople(false); setExpanding("group") }}>Add people</Button>}<Button type="button" variant="outline" onClick={() => { setShowPeople(false); setExpanding("channel") }}>Invite agent</Button></div>}{conversation.kind === "group" && !conversation.is_direct && conversation.created_by === userId && people.data && <ManagePeople workspaceId={workspaceId} userId={userId} conversation={conversation} participants={people.data.participants} refresh={refresh} />}{conversation.kind === "channel" && <ConversationActivity workspaceId={workspaceId} userId={userId} conversationId={conversation.id} canManage={conversation.created_by === userId} />} {conversation.kind === "channel" && agentRoster.data && <ConversationAgents workspaceId={workspaceId} conversationId={conversation.id} agents={agentRoster.data.agents} canManage={conversation.created_by === userId} refresh={refresh} />}</DialogContent></Dialog>
    <Dialog open={!!expanding} onOpenChange={(open) => { if (!open) setExpanding(null) }}><DialogContent><DialogHeader><DialogTitle>{expanding === "group" ? "Continue in a group" : "Continue with an agent"}</DialogTitle><DialogDescription>{expanding === "group" ? "Bring more people into a new private group." : "Start a workspace channel with the selected agent."}</DialogDescription></DialogHeader>{expanding && people.data && <ConversationExpansion key={expanding} workspaceId={workspaceId} userId={userId} conversation={conversation} participants={people.data.participants} kind={expanding} onCreated={(c) => { setExpanding(null); refresh(); onOpenConversation?.(c) }} />}</DialogContent></Dialog>
  </>
}

function ManagePeople({ workspaceId, userId, conversation, participants, refresh }: { workspaceId: string; userId: string; conversation: WorkspaceConversation; participants: ConversationParticipant[]; refresh: () => void }) {
  const [adding, setAdding] = useState("")
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<Error | null>(null)
  const people = useQuery({ queryKey: ["workspace-conversation-people", workspaceId, userId], queryFn: ({ signal }) => conversationRequest<{ user: ConversationPerson }[]>(workspaceId, `workspaces/${encodeURIComponent(workspaceId)}/members`, undefined, signal) })
  async function change(removeId?: string) {
    if (busy) return
    setBusy(true); setError(null)
    try {
      if (removeId) {
        const response = await apiFetch(`/api/v1/conversations/${encodeURIComponent(conversation.id)}/participants/${encodeURIComponent(removeId)}?workspace_id=${encodeURIComponent(workspaceId)}`, { method: "DELETE" })
        if (!response.ok) throw new Error("Unable to remove this person. Please retry.")
      } else await conversationRequest(workspaceId, `conversations/${encodeURIComponent(conversation.id)}/participants`, { user_id: adding })
      setAdding(""); refresh()
    } catch (e) { setError(e instanceof Error ? e : new Error("Unable to update people.")) }
    finally { setBusy(false) }
  }
  return <section className="space-y-3 border-t pt-3"><h3 className="text-sm font-medium">Manage people</h3><p className="text-xs text-muted-foreground">Adding someone grants access to all existing history. Removing someone revokes future access to a group; workspace channels remain readable by everyone in the workspace.</p>{people.error && <ErrorNotice error={people.error} retry={() => { void people.refetch() }} />}<div className="flex gap-2"><select aria-label="Person to add" className="min-w-0 flex-1 rounded-md border bg-background p-2 text-sm" value={adding} onChange={(e) => setAdding(e.target.value)}><option value="">Choose a person…</option>{people.data?.filter(({ user }) => !participants.some((p) => p.user_id === user.id)).map(({ user }) => <option key={user.id} value={user.id}>{user.full_name || user.email}</option>)}</select><Button type="button" disabled={!adding || busy} onClick={() => { void change() }}>Add</Button></div>{participants.filter((p) => p.user_id !== conversation.created_by).map((p) => <div key={p.user_id} className="flex items-center justify-between gap-2 text-sm"><span>{p.name}</span><Button type="button" size="sm" variant="ghost" disabled={busy} onClick={() => { if (window.confirm(`Remove ${p.name} from this conversation?`)) void change(p.user_id) }}>Remove</Button></div>)}{error && <p role="alert" className="text-sm text-destructive">{error.message}</p>}</section>
}


type PendingSend = { client_id: string; content: string; mentioned_agent_ids?: string[] }
function readDraft(key: string): { text: string; pending: PendingSend | null } {
  try {
    const value = JSON.parse(sessionStorage.getItem(key) || "null")
    if (value && typeof value.text === "string") {
      const pending = value.pending
      return { text: value.text, pending: pending && typeof pending.client_id === "string" && typeof pending.content === "string" ? pending : null }
    }
  } catch { /* No browser storage during static export, or storage disabled. */ }
  return { text: "", pending: null }
}


function conversationLabel(conversation: WorkspaceConversation): string {
  return conversation.is_direct ? "Direct message" : conversation.kind === "channel" ? "Workspace channel" : "Group"
}
