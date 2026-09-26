"use client"

import { useEffect, useRef, useState, type ReactNode } from "react"
import { motion } from "motion/react"
import { ChevronDown, ChevronRight, MailOpen, Plus, Radio, X } from "lucide-react"
import { AgentAvatar } from "@/components/ui/agent-avatar"
import { Button } from "@/components/ui/button"
import {
  SidebarCollapseButton,
  SidebarFacet,
  SidebarFacetOption,
  SidebarFilterPopover,
  SidebarSearch,
  SidebarToolbar,
} from "@/components/layout/sidebar-kit"
import { filterUnifiedConversationRows, UnifiedConversationSection, useUnifiedConversations } from "@/components/features/conversations/unified-chat"
import { CHAT_SCOPES, classifyThread, KIND_META } from "./chat-kind"
import { useFavoriteRooms } from "./use-favorite-rooms"
import { useSidebarSessionSearch } from "./use-sidebar-session-search"
import { FavoriteButton, useChatFavorites } from "./chat-favorites"
import { ChatCrewPicker } from "./chat-crew-picker"
import type { Props, ConversationRow } from "./conversations-sidebar"
import type { ChatTreeAgent } from "./chat-tree-data"
import { cn } from "@/lib/utils"

function Section({ title, children, count, open, onToggle, className }: {
  title: string
  children: ReactNode
  count?: number
  open: boolean
  onToggle: () => void
  className?: string
}) {
  return <section aria-label={title} className={cn("border-b border-white/[0.06]", className)}>
    <button type="button" aria-expanded={open} onClick={onToggle} className="kit-tap flex min-h-8 w-full items-center gap-1.5 px-3 py-1.5 text-left hover:bg-white/[0.02]">
      <ChevronDown aria-hidden="true" className={cn("size-3 shrink-0 text-muted-foreground/60 transition-transform duration-150", !open && "-rotate-90")} />
      <span className="min-w-0 flex-1 truncate text-[10px] font-semibold uppercase tracking-wider text-foreground/50">{title}</span>
      {count !== undefined && <span className="text-[10px] tabular-nums text-muted-foreground">{count}</span>}
    </button>
    <motion.div initial={false} animate={{ height: open ? "auto" : 0, opacity: open ? 1 : 0 }} transition={{ duration: 0.18 }} aria-hidden={!open} inert={!open} className="overflow-hidden">{children}</motion.div>
  </section>
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

function ScopedChatSidebar({ props, rows: loadedRows, query, setQuery, stateKey }: SidebarProps & { stateKey: string }) {
  const { agents, activeThreadId: previousThreadId, draftConversation: previousDraft, onStartConversation, onSelectThread, onUnifiedSelect, scope, onScopeChange, threadsByAgent, totalsByAgent, onShowAll, pickerSignal, onPickerHandled } = props
  const chat = useUnifiedConversations()
  const sessionSearch = useSidebarSessionSearch(chat?.workspaceId, props.agents ?? [], query, props.scope)
  const rows = query.trim() ? sessionSearch.rows : loadedRows
  const updateSearch = chat?.setSearchQuery
  useEffect(() => { const timer = setTimeout(() => updateSearch?.(query.trim()), 250); return () => clearTimeout(timer) }, [query, updateSearch])
  const activeThreadId = chat?.selectedId ? null : previousThreadId
  const draftConversation = chat?.selectedId ? null : previousDraft
  const [picking, setPicking] = useState(false)
  const [expanded, setExpanded] = useState<Record<string, boolean>>(() => { const saved = readFlags(`${stateKey}:agents`); const id = Object.keys(saved).find((id) => saved[id]); return id ? { [id]: true } : {} })
  const { favorites, toggle: toggleFavorite } = useChatFavorites(stateKey)
  const [closed, setClosed] = useState<Record<string, boolean>>(() => readFlags(`${stateKey}:sections`))
  const [unread, setUnread] = useState(false)
  const [live, setLive] = useState(false)
  const [crewFilter, setCrewFilter] = useState<string | null>(null)
  const [sectionFilter, setSectionFilter] = useState<"all" | "agents" | "people" | "rooms">("all")
  const [fullHistory, setFullHistory] = useState<string | null>(null)
  const [loading, setLoading] = useState<string | null>(null)
  const [loadError, setLoadError] = useState<string | null>(null)

  useEffect(() => {
    try {
      sessionStorage.setItem(`${stateKey}:agents`, JSON.stringify(expanded))
      sessionStorage.setItem(`${stateKey}:sections`, JSON.stringify(closed))
    } catch { /* Storage may be disabled. Navigation still works. */ }
  }, [stateKey, expanded, closed])

  const selectedRoom = chat?.list.data?.pages.flatMap((page) => page.conversations).find((room) => room.id === chat.selectedId)
  const selectedSection = selectedRoom ? selectedRoom.is_direct ? "people" : "rooms" : null
  useEffect(() => {
    if (!chat?.selectedId || !selectedSection) return
    setClosed((old) => old[selectedSection] ? { ...old, [selectedSection]: false } : old)
  }, [chat?.selectedId, selectedSection])
  useEffect(() => { if (chat?.selectedId) setPicking(false) }, [chat?.selectedId, chat?.selectionVersion])
  useEffect(() => {
    if (pickerSignal === undefined || pickerSignal === 0) return
    setPicking(true)
    onPickerHandled?.()
    setQuery("")
    setClosed((old) => ({ ...old, agents: false }))
  }, [pickerSignal, onPickerHandled, setQuery])
  useEffect(() => {
    if (!draftConversation?.id) return
    setPicking(false)
    setQuery("")
    setExpanded({ [draftConversation.agent.id]: true })
    setClosed((old) => ({ ...old, agents: false }))
  }, [draftConversation?.id, draftConversation?.agent.id, setQuery])
  const revealedThreadId = useRef<string | null>(null)
  useEffect(() => {
    if (!activeThreadId) { revealedThreadId.current = null; return }
    if (revealedThreadId.current === activeThreadId) return
    const selected = rows.find((row) => row.thread.id === activeThreadId)
    if (!selected) return
    revealedThreadId.current = activeThreadId
    setExpanded({ [selected.agent.id]: true })
    setClosed((old) => old.agents ? { ...old, agents: false } : old)
  }, [activeThreadId, rows])

  const roster = agents ?? []
  const q = query.trim().toLowerCase()
  const pickerMatches = roster.filter((agent) => (!crewFilter || agent.crew_id === crewFilter) && (!q || agent.name.toLowerCase().includes(q)))
  const matching = roster.filter((agent) =>
    (!crewFilter || agent.crew_id === crewFilter) &&
    ((scope !== "routine" && scope !== "issue") || !props.threadsLoaded || !Object.hasOwn(threadsByAgent, agent.id) || props.threadErrors?.[agent.id] || rows.some((row) => row.agent.id === agent.id)) &&
    (!unread || rows.some((row) => row.agent.id === agent.id && (row.thread.unread_count ?? 0) > 0) || rows.some((row) => row.agent.id === agent.id && row.thread.id === activeThreadId)) &&
    (!live || agent.status === "RUNNING" || rows.some((row) => row.agent.id === agent.id && row.thread.id === activeThreadId)) &&
    (!q || agent.name.toLowerCase().includes(q) || rows.some((row) => row.agent.id === agent.id && (row.thread.title || "").toLowerCase().includes(q))),
  )
  const loadedRooms = chat?.list.data?.pages.flatMap((page) => page.conversations) ?? []
  const favoriteRooms = useFavoriteRooms(chat?.workspaceId, chat?.userId, favorites, loadedRooms)
  const rooms = [...loadedRooms, ...favoriteRooms]
  const peopleCount = filterUnifiedConversationRows(rooms, query, "people").filter((room) => !favorites.includes(`room:${room.id}`)).length
  const roomCount = filterUnifiedConversationRows(rooms, query, "rooms").filter((room) => !favorites.includes(`room:${room.id}`)).length
  const filterCount = Number(unread) + Number(live) + Number(!!crewFilter) + Number(sectionFilter !== "all") + Number(scope !== "direct")
  const showAgents = sectionFilter === "all" || sectionFilter === "agents"
  const showPeople = sectionFilter === "all" || sectionFilter === "people"
  const showTeamSpaces = sectionFilter === "all" || sectionFilter === "rooms"
  const pinnedAgents = matching.filter((agent) => favorites.includes(`agent:${agent.id}`))
  const otherAgents = matching.filter((agent) => !favorites.includes(`agent:${agent.id}`))
  const sharedFavoriteCount = rooms.filter((room) => favorites.includes(`room:${room.id}`) && (room.is_direct ? showPeople : showTeamSpaces)).length
  const hasFavorites = (showAgents && pinnedAgents.length > 0) || sharedFavoriteCount > 0
  const sharedProps = { extraRooms: favoriteRooms, favorites, onFavorite: toggleFavorite, showLoadMore: false, onSelect: onUnifiedSelect }
  function resetFilters() { setUnread(false); setLive(false); setCrewFilter(null); setSectionFilter("all"); onScopeChange("direct") }
  const sectionState = (name: string) => ({ open: !closed[name], onToggle: () => setClosed((old) => ({ ...old, [name]: !old[name] })) })

  function start(agent: ChatTreeAgent) {
    setPicking(false)
    setQuery("")
    setExpanded({ [agent.id]: true })
    onStartConversation(agent)
  }

  function renderAgents(list: ChatTreeAgent[]) { return list.map((agent) => {
            const sessions = rows.filter((row) => row.agent.id === agent.id && (!unread || !!row.thread.unread_count || row.thread.id === activeThreadId) && (!live || agent.status === "RUNNING" || row.thread.id === activeThreadId) && (!q || agent.name.toLowerCase().includes(q) || (row.thread.title || "").toLowerCase().includes(q)))
            const shortHistory = !q && fullHistory !== agent.id && sessions.length > 5
            const activeSession = sessions.find((row) => row.thread.id === activeThreadId)
            const firstSessions = sessions.slice(0, 5)
            const visibleSessions = shortHistory ? activeSession && !firstSessions.includes(activeSession) ? [...firstSessions.slice(0, 4), activeSession] : firstSessions : sessions
            const isDraft = draftConversation?.agent.id === agent.id
            const selected = isDraft || sessions.some((row) => row.thread.id === activeThreadId)
            const open = expanded[agent.id] || !!q
            const known = Object.hasOwn(threadsByAgent, agent.id)
            const total = totalsByAgent?.[agent.id] ?? threadsByAgent[agent.id]?.length ?? 0
            const unreadCount = sessions.reduce((sum, row) => sum + (row.thread.unread_count ?? 0), 0)
            return <div key={agent.id} className="px-1"><div className={cn("relative flex items-center rounded-md transition-colors hover:bg-white/[0.04]", selected && open && "bg-primary/10 before:absolute before:inset-y-1 before:left-0 before:w-0.5 before:rounded-full before:bg-primary")}>
              <button type="button" aria-label={`${open ? "Hide" : "Show"} ${agent.name} sessions`} aria-expanded={!!open} onClick={() => setExpanded(open ? {} : { [agent.id]: true })} className="kit-tap flex size-7 shrink-0 items-center justify-center text-muted-foreground">{open ? <ChevronDown className="size-3" /> : <ChevronRight className="size-3" />}</button>
              <button type="button" aria-label={`Open ${agent.name} chat`} aria-current={selected ? "page" : undefined} onClick={() => { if (selected && open) { setExpanded({}); return }; setExpanded({ [agent.id]: true }); const latest = sessions[0]; if (latest) onSelectThread(agent, latest.thread); else if ((scope === "direct" || scope === "all") && known && !props.threadErrors?.[agent.id] && props.threadsLoaded) start(agent) }} className="kit-tap flex min-h-9 min-w-0 flex-1 items-center gap-2 text-left text-xs"><AgentAvatar seed={agent.avatar_seed || agent.slug} style={agent.avatar_style} avatarUrl={agent.avatar_url} agentId={agent.id} className="size-6" /><span className="min-w-0 flex-1 truncate">{agent.name}</span><span className="text-[9px] text-muted-foreground">AI</span>{unreadCount > 0 && <span className="text-[10px] tabular-nums text-primary">{unreadCount}</span>}</button>
              <FavoriteButton name={agent.name} active={favorites.includes(`agent:${agent.id}`)} onClick={() => toggleFavorite(`agent:${agent.id}`)} />
            </div>
            {open && <motion.div initial={{ height: 0, opacity: 0 }} animate={{ height: "auto", opacity: 1 }} transition={{ duration: 0.18 }} className="ml-7 overflow-hidden border-l border-white/[0.08] pl-2">
              {isDraft && <div role="status" aria-current="page" className="rounded-md bg-primary/10 px-2 py-2 text-xs">{agent.name} · Draft</div>}
              {props.threadErrors?.[agent.id] && <p role="alert" className="p-2 text-xs">Sessions unavailable. <button type="button" className="underline" onClick={props.onRetryThreads}>Retry</button></p>}
              {visibleSessions.map(({ thread }) => <button type="button" key={thread.id} aria-current={thread.id === activeThreadId ? "page" : undefined} onClick={() => onSelectThread(agent, thread)} className={cn("kit-tap block min-h-8 w-full truncate rounded-md px-2 text-left text-xs transition-colors hover:bg-accent", thread.id === activeThreadId && "bg-primary/10 text-primary")}>{classifyThread(thread) !== "direct" && <span className="mr-1 text-[9px] text-muted-foreground">{KIND_META[classifyThread(thread)].label} ·</span>}{thread.title || "Untitled session"}{!!thread.unread_count && <span className="ml-2">{thread.unread_count}</span>}</button>)}
              {!q && !sessions.length && known && props.threadsLoaded && !props.threadErrors?.[agent.id] && !isDraft && <p className="px-2 py-2 text-xs text-muted-foreground">{unread || live || scope === "routine" || scope === "issue" ? "No matching sessions." : "Start a conversation with this agent."}</p>}
              {shortHistory && <button type="button" className="kit-tap px-2 py-1 text-[11px] text-muted-foreground hover:text-foreground" onClick={() => setFullHistory(agent.id)}>Show all {sessions.length} loaded sessions</button>}
              <Button variant="ghost" size="sm" aria-label={`New session with ${agent.name}`} onClick={() => start(agent)}><Plus className="size-3" />New session</Button>
              {onShowAll && !q && (!known || total > (threadsByAgent[agent.id]?.length ?? 0)) && <Button variant="ghost" size="sm" disabled={loading === agent.id} onClick={async () => { setLoading(agent.id); setLoadError(null); try { await onShowAll(agent.id) } catch { setLoadError(agent.id) } finally { setLoading(null) } }}>{known ? "Load older sessions" : "Load sessions"}</Button>}
              {loadError === agent.id && <p role="alert" className="text-xs">Unable to load older sessions. Try again.</p>}
            </motion.div>}
          </div>
  }) }

  return <div className={cn("flex h-full min-h-0 flex-col", props.className)}>
    <SidebarToolbar>
      <div data-chat-search className="min-w-0 flex-1">
        <SidebarSearch value={query} onValueChange={setQuery} aria-label={picking ? "Search agents" : "Search conversations"} placeholder={picking ? "Find an agent…" : "Search names, sessions…"} onKeyDown={(event) => { if (event.key === "Escape") { setPicking(false); setQuery("") } }} />
      </div>
      {!picking && <SidebarFilterPopover label="Filter chats" activeCount={filterCount} onClear={resetFilters}>
        <SidebarFacet label="Show" resetLabel="All conversations" resetActive={sectionFilter === "all"} onReset={() => setSectionFilter("all")} first>
          {([['agents', 'Agent sessions'], ['people', 'People'], ['rooms', 'Team spaces']] as const).map(([id, label]) => <SidebarFacetOption key={id} active={sectionFilter === id} onToggle={() => setSectionFilter(sectionFilter === id ? "all" : id)}>{label}</SidebarFacetOption>)}
        </SidebarFacet>
        <SidebarFacet label="Agent session type" resetLabel="Direct conversations" resetActive={scope === "direct"} onReset={() => { onScopeChange("direct"); setSectionFilter("all") }}>
          {CHAT_SCOPES.filter((item) => item.id !== "direct").map((item) => <SidebarFacetOption key={item.id} active={scope === item.id} onToggle={() => { const next = scope === item.id ? "direct" : item.id; onScopeChange(next); setSectionFilter(next === "routine" || next === "issue" ? "agents" : "all") }}><item.icon className="size-3.5" />{item.id === "all" ? "All sessions" : item.label}</SidebarFacetOption>)}
        </SidebarFacet>
        <SidebarFacet label="Crew · agents only" resetLabel="All crews" resetActive={!crewFilter} onReset={() => setCrewFilter(null)}>
          <ChatCrewPicker workspaceId={chat?.workspaceId ?? null} agents={roster} value={crewFilter} onChange={(next) => { setCrewFilter(next) }} />
        </SidebarFacet>
        <SidebarFacet label="Agent status" resetLabel="Any status" resetActive={!unread && !live} onReset={() => { setUnread(false); setLive(false) }}>
          <SidebarFacetOption active={unread} onToggle={() => { setUnread(!unread); setSectionFilter("agents") }}><MailOpen className="size-3.5" />Unread agent sessions</SidebarFacetOption>
          <SidebarFacetOption active={live} onToggle={() => { setLive(!live); setSectionFilter("agents") }}><Radio className="size-3.5" />Running agents</SidebarFacetOption>
        </SidebarFacet>
        <p className="px-3 py-2 text-[11px] text-muted-foreground">Crew and session type apply to agents. Choose All conversations to include people and team spaces.</p>
      </SidebarFilterPopover>}
      {props.onToggleCollapse && <SidebarCollapseButton collapsed={false} onToggle={props.onToggleCollapse} {...(props.collapseLabel ? { "aria-label": props.collapseLabel, title: props.collapseLabel } : {})} />}
    </SidebarToolbar>
    {!picking && filterCount > 0 && <div className="mx-2 mb-2 flex items-center gap-2 rounded-md border border-primary/20 px-2 py-1 text-[10px] text-muted-foreground"><span className="min-w-0 flex-1 truncate">{sectionFilter === "agents" ? "Agents only" : sectionFilter === "people" ? "People" : sectionFilter === "rooms" ? "Team spaces" : "All conversations"}{scope !== "direct" && ` · ${scope === "all" ? "All sessions" : CHAT_SCOPES.find((item) => item.id === scope)?.label}`}{crewFilter && " · Crew filter"}</span><button type="button" aria-label="Clear chat filters" className="kit-tap" onClick={resetFilters}><X className="size-3" /></button></div>}
    {q && !picking && <div className="px-3 pb-2 text-[11px] text-muted-foreground">{sessionSearch.loading ? <span role="status">Searching session titles…</span> : sessionSearch.error ? <button type="button" onClick={sessionSearch.retry}>Session search unavailable. Retry</button> : sessionSearch.more ? <button type="button" onClick={sessionSearch.loadMore} className="text-primary">Load more matching sessions</button> : "Matching names and session titles"}</div>}
    <div className="min-h-0 flex-1 overflow-y-auto pb-3">
      {picking ? <section aria-label="Choose an agent"><div className="flex items-center justify-between px-3 py-1"><h3 className="text-[10px] font-semibold uppercase tracking-wider text-foreground/50">New session with</h3><Button variant="ghost" size="sm" onClick={() => setPicking(false)}>Cancel</Button></div>{pickerMatches.map((agent) => <button key={agent.id} type="button" onClick={() => start(agent)} className="kit-tap flex min-h-10 w-full items-center gap-2 px-3 text-left text-xs hover:bg-accent"><AgentAvatar seed={agent.avatar_seed || agent.slug} style={agent.avatar_style} avatarUrl={agent.avatar_url} agentId={agent.id} className="size-6" /><span className="truncate">{agent.name}</span></button>)}{!pickerMatches.length && <p className="p-3 text-xs text-muted-foreground">No agents found.</p>}</section> : <>
        {hasFavorites && <Section title="Favorites" {...sectionState("favorites")}>
          {showTeamSpaces && <UnifiedConversationSection query={query} section="rooms" {...sharedProps} favoritesOnly hideEmpty />}
          {showPeople && <UnifiedConversationSection query={query} section="people" {...sharedProps} favoritesOnly hideEmpty />}
          {showAgents && renderAgents(pinnedAgents)}
        </Section>}
        {showTeamSpaces && (roomCount > 0 || !rooms.some((room) => !room.is_direct && favorites.includes(`room:${room.id}`))) && <Section title="Team spaces" count={roomCount} {...sectionState("rooms")}><UnifiedConversationSection query={query} section="rooms" {...sharedProps} /></Section>}
        {showPeople && (peopleCount > 0 || !rooms.some((room) => room.is_direct && favorites.includes(`room:${room.id}`))) && <Section title="People" count={peopleCount} {...sectionState("people")}><UnifiedConversationSection query={query} section="people" {...sharedProps} /></Section>}
        {showAgents && (otherAgents.length > 0 || !pinnedAgents.length || props.loadError) && <Section title="Agents" count={otherAgents.length} {...sectionState("agents")}>
          {props.loadError && <p role="alert" className="p-3 text-xs">Agents could not be loaded. <button type="button" className="underline" onClick={props.onRetryRoster}>Retry</button></p>}
          {!props.threadsLoaded && <p className="px-3 py-2 text-xs text-muted-foreground">Loading sessions…</p>}
          {!props.loadError && !!agents && matching.length === 0 && <p className="px-3 py-2 text-xs text-muted-foreground">No matching agents.</p>}
          {renderAgents(otherAgents)}
        </Section>}
        {(showPeople || showTeamSpaces) && chat?.list.hasNextPage && <Button variant="ghost" size="sm" disabled={chat.list.isFetchingNextPage} onClick={() => { void chat.list.fetchNextPage() }}>Load more conversations</Button>}
        {(showPeople || showTeamSpaces) && chat?.list.isFetchNextPageError && <p role="alert" className="px-3 text-xs">More conversations could not be loaded. Try again.</p>}
      </>}
    </div>
  </div>
}
