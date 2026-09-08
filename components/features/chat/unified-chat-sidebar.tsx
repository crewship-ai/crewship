"use client"

import { useEffect, useState } from "react"
import { ChevronDown, ChevronRight, Plus, Search } from "lucide-react"
import { AgentAvatar } from "@/components/ui/agent-avatar"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { SidebarCollapseButton } from "@/components/layout/sidebar-kit"
import { UnifiedConversationSection, UnifiedNewChatMenu, useUnifiedConversations } from "@/components/features/conversations/unified-chat"
import { CHAT_SCOPES } from "./chat-kind"
import type { Props, ConversationRow } from "./conversations-sidebar"
import type { ChatTreeAgent } from "./chat-tree-data"
import { cn } from "@/lib/utils"

function Section({ title, children, count, open, onToggle }: { title: string; children: React.ReactNode; count?: number; open: boolean; onToggle: () => void }) {
  return <section aria-label={title} className="mb-2"><button type="button" aria-expanded={open} onClick={onToggle} className="flex min-h-9 w-full items-center gap-2 px-3 text-xs font-medium text-muted-foreground hover:text-foreground">{open ? <ChevronDown className="size-3.5" /> : <ChevronRight className="size-3.5" />}<span className="flex-1 text-left">{title}</span>{count !== undefined && <span>{count}</span>}</button>{open && children}</section>
}

type SidebarProps = { props: Props; rows: ConversationRow[]; query: string; setQuery: (query: string) => void }
function readFlags(key: string): Record<string, boolean> {
  try {
    const value = JSON.parse(sessionStorage.getItem(key) || "{}")
    if (!value || typeof value !== "object" || Array.isArray(value)) return {}
    return Object.fromEntries(Object.entries(value).filter(([, flag]) => typeof flag === "boolean")) as Record<string, boolean>
  } catch { return {} }
}
export function UnifiedChatSidebar(props: SidebarProps) {
  const chat = useUnifiedConversations()
  const stateKey = JSON.stringify(["chat-sidebar", chat?.workspaceId, chat?.userId])
  return <ScopedChatSidebar key={stateKey} {...props} stateKey={stateKey} />
}
function ScopedChatSidebar({ props, rows, query, setQuery, stateKey }: SidebarProps & { stateKey: string }) {
  const { agents, activeThreadId: previousThreadId, draftConversation: previousDraft, onStartConversation, onSelectThread, onUnifiedSelect, scope, onScopeChange, threadsByAgent, totalsByAgent, onShowAll } = props
  const chat = useUnifiedConversations()
  const activeThreadId = chat?.selectedId ? null : previousThreadId
  const draftConversation = chat?.selectedId ? null : previousDraft
  const [picking, setPicking] = useState(false)
  const [expanded, setExpanded] = useState<Record<string, boolean>>(() => readFlags(`${stateKey}:agents`))
  const [closed, setClosed] = useState<Record<string, boolean>>(() => readFlags(`${stateKey}:sections`))
  useEffect(() => { try { sessionStorage.setItem(`${stateKey}:agents`, JSON.stringify(expanded)); sessionStorage.setItem(`${stateKey}:sections`, JSON.stringify(closed)) } catch { /* Storage may be disabled. Navigation still works. */ } }, [stateKey, expanded, closed])
  const selectedRoom = chat?.list.data?.pages.flatMap((page) => page.conversations).find((room) => room.id === chat.selectedId)
  const selectedSection = selectedRoom ? selectedRoom.is_direct ? "people" : "rooms" : null
  useEffect(() => {
    if (!chat?.selectedId || !selectedSection) return
    setClosed((old) => old[selectedSection] ? { ...old, [selectedSection]: false } : old)
  }, [chat?.selectedId, selectedSection])
  function sectionState(section: string) { return { open: !closed[section], onToggle: () => setClosed((old) => ({ ...old, [section]: !old[section] })) } }
  const [loading, setLoading] = useState<string | null>(null)
  const [loadError, setLoadError] = useState<string | null>(null)
  const [unread, setUnread] = useState(false)
  const [live, setLive] = useState(false)
  useEffect(() => { if (chat?.selectedId) setPicking(false) }, [chat?.selectedId, chat?.selectionVersion])
  useEffect(() => { if (draftConversation?.id) { setPicking(false); setQuery(""); setExpanded((old) => ({ ...old, [draftConversation.agent.id]: true })); setClosed((old) => ({ ...old, agents: false })) } }, [draftConversation?.id, draftConversation?.agent.id, setQuery])
  useEffect(() => { const selected = rows.find((row) => row.thread.id === activeThreadId); if (selected) { setExpanded((old) => old[selected.agent.id] ? old : { ...old, [selected.agent.id]: true }); setClosed((old) => old.agents ? { ...old, agents: false } : old) } }, [activeThreadId, rows])
  useEffect(() => { setUnread(false); setLive(false) }, [scope])
  const roster = agents ?? []
  const matching = roster.filter((agent) => !query.trim() || agent.name.toLowerCase().includes(query.toLowerCase()) || rows.some((row) => row.agent.id === agent.id && (row.thread.title || "").toLowerCase().includes(query.toLowerCase())))
  function start(agent: ChatTreeAgent) { setPicking(false); setQuery(""); setExpanded((old) => ({ ...old, [agent.id]: true })); onStartConversation(agent) }
  return <div className={cn("flex h-full min-h-0 flex-col", props.className)}>
    <div className="space-y-3 border-b p-3"><div className="flex items-center justify-between"><h2 className="text-base font-semibold">Chat</h2>{props.onToggleCollapse && <SidebarCollapseButton collapsed={false} onToggle={props.onToggleCollapse} {...(props.collapseLabel ? { "aria-label": props.collapseLabel } : {})} />}</div><UnifiedNewChatMenu onAgent={() => { setPicking(true); setQuery("") }} /><div data-chat-search className="relative"><Search aria-hidden="true" className="absolute left-3 top-3 size-3.5 text-muted-foreground" /><Input className="pl-8" aria-label={picking ? "Search agents" : "Search conversations"} placeholder={picking ? "Find an agent…" : "Search chats…"} value={query} onChange={(e) => setQuery(e.target.value)} onKeyDown={(e) => { if (e.key === "Escape") { setPicking(false); setQuery("") } }} /></div></div>
    <div className="min-h-0 flex-1 overflow-y-auto py-2">
      {picking ? <section aria-label="Choose an agent"><div className="flex items-center justify-between px-3"><h3 className="text-xs text-muted-foreground">New session with</h3><Button variant="ghost" size="sm" onClick={() => setPicking(false)}>Cancel</Button></div>{matching.map((agent) => <button key={agent.id} type="button" onClick={() => start(agent)} className="flex min-h-11 w-full items-center gap-3 px-3 text-sm hover:bg-accent"><AgentAvatar seed={agent.avatar_seed || agent.slug} style={agent.avatar_style} avatarUrl={agent.avatar_url} className="size-7" /><span>{agent.name}</span></button>)}{!matching.length && <p className="p-3 text-xs text-muted-foreground">No agents found.</p>}</section> : <>
        <Section title="People" {...sectionState("people")}><UnifiedConversationSection query={query} section="people" onSelect={onUnifiedSelect} /></Section>
        <Section title="Agents" {...sectionState("agents")} count={matching.length}>
          {props.loadError && <p role="alert" className="p-3 text-xs">Agents could not be loaded. <button className="underline" onClick={props.onRetryRoster}>Retry</button></p>}
          {!props.threadsLoaded && <p className="px-3 py-2 text-xs text-muted-foreground">Loading sessions…</p>}
          {matching.map((agent) => {
            const sessions = rows.filter((row) => row.agent.id === agent.id && (!unread || !!row.thread.unread_count || row.thread.id === activeThreadId) && (!live || agent.status === "RUNNING" || row.thread.id === activeThreadId))
            const isDraft = draftConversation?.agent.id === agent.id
            const selected = isDraft || sessions.some((row) => row.thread.id === activeThreadId)
            const open = expanded[agent.id] || !!query
            const known = Object.hasOwn(threadsByAgent, agent.id)
            const total = totalsByAgent?.[agent.id] ?? threadsByAgent[agent.id]?.length ?? 0
            const unreadCount = sessions.reduce((sum, row) => sum + (row.thread.unread_count ?? 0), 0)
            return <div key={agent.id} className="px-1"><div className={cn("flex items-center rounded-lg", selected && "bg-accent")}><button type="button" aria-label={`${open ? "Hide" : "Show"} ${agent.name} sessions`} aria-expanded={!!open} onClick={() => setExpanded((old) => ({ ...old, [agent.id]: !open }))} className="flex h-10 w-6 shrink-0 items-center justify-center text-muted-foreground">{open ? <ChevronDown className="size-3" /> : <ChevronRight className="size-3" />}</button><button type="button" aria-label={`Open ${agent.name} chat`} aria-current={selected ? "page" : undefined} onClick={() => { setExpanded((old) => ({ ...old, [agent.id]: true })); const latest = rows.find((row) => row.agent.id === agent.id); if (latest) onSelectThread(agent, latest.thread); else if (scope === "direct" && Object.hasOwn(threadsByAgent, agent.id) && !props.threadErrors?.[agent.id] && props.threadsLoaded) start(agent) }} className="flex min-h-11 min-w-0 flex-1 items-center gap-2 text-left text-sm"><AgentAvatar seed={agent.avatar_seed || agent.slug} style={agent.avatar_style} avatarUrl={agent.avatar_url} className="size-7" /><span className="min-w-0 flex-1 truncate">{agent.name}</span>{unreadCount > 0 && <span className="text-xs text-primary">{unreadCount}</span>}</button><Button type="button" variant="ghost" size="icon" className="size-8 shrink-0" aria-label={`New session with ${agent.name}`} title={`New session with ${agent.name}`} onClick={() => start(agent)}><Plus className="size-3.5" /></Button></div>
              {open && <div className="ml-7 border-l pl-2">{isDraft && <div role="status" aria-current="page" className="rounded-md bg-primary/10 px-2 py-2 text-xs">{agent.name} · Draft</div>}{props.threadErrors?.[agent.id] && <p role="alert" className="p-2 text-xs">Sessions unavailable. <button className="underline" onClick={props.onRetryThreads}>Retry</button></p>}{sessions.map(({ thread }) => <button type="button" key={thread.id} aria-current={thread.id === activeThreadId ? "page" : undefined} onClick={() => onSelectThread(agent, thread)} className={cn("block min-h-9 w-full truncate rounded-md px-2 text-left text-xs hover:bg-accent", thread.id === activeThreadId && "bg-primary/10 text-primary")}>{thread.title || "Untitled session"}{!!thread.unread_count && <span className="ml-2">{thread.unread_count}</span>}</button>)}{!sessions.length && known && props.threadsLoaded && !props.threadErrors?.[agent.id] && !isDraft && <p className="px-2 py-2 text-xs text-muted-foreground">{unread || live ? "No matching sessions." : "No sessions yet."}</p>}{onShowAll && (!known || total > (threadsByAgent[agent.id]?.length ?? 0)) && <Button variant="ghost" size="sm" disabled={loading === agent.id} onClick={async () => { setLoading(agent.id); setLoadError(null); try { await onShowAll(agent.id) } catch { setLoadError(agent.id) } finally { setLoading(null) } }}>{known ? "Load older sessions" : "Load sessions"}</Button>}{loadError === agent.id && <p role="alert" className="text-xs">Unable to load older sessions. Try again.</p>}</div>}
            </div>
          })}
        </Section>
        <Section title="Team spaces" {...sectionState("rooms")}><UnifiedConversationSection query={query} section="rooms" onSelect={onUnifiedSelect} /></Section>
        <details className="border-t px-3 py-2"><summary className="cursor-pointer py-1 text-xs text-muted-foreground">Activity &amp; filters{scope !== "direct" ? ` · ${CHAT_SCOPES.find((item) => item.id === scope)?.label}` : ""}</summary><p className="py-2 text-xs text-muted-foreground">Agent session history</p>{CHAT_SCOPES.map((item) => <button key={item.id} type="button" aria-pressed={scope === item.id} onClick={() => onScopeChange(item.id)} className={cn("flex min-h-9 w-full items-center gap-2 rounded-md px-2 text-xs", scope === item.id && "bg-accent")}><item.icon className="size-3.5" />{item.label}</button>)}<label className="flex min-h-9 items-center gap-2 text-xs"><input type="checkbox" checked={unread} onChange={(e) => setUnread(e.target.checked)} />Unread agent sessions</label><label className="flex min-h-9 items-center gap-2 text-xs"><input type="checkbox" checked={live} onChange={(e) => setLive(e.target.checked)} />Running agents</label></details>
      </>}
    </div>
  </div>
}
