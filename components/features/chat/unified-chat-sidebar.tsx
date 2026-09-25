"use client"

import { useEffect, useRef, useState, type ReactNode } from "react"
import { motion } from "motion/react"
import { ChevronDown, ChevronRight, MailOpen, Plus, Radio } from "lucide-react"
import { AgentAvatar } from "@/components/ui/agent-avatar"
import { Button } from "@/components/ui/button"
import {
  SidebarCollapseButton,
  SidebarFacet,
  SidebarFacetOption,
  SidebarFilterPopover,
  SidebarRow,
  SidebarSearch,
  SidebarToolbar,
} from "@/components/layout/sidebar-kit"
import { filterUnifiedConversationRows, UnifiedConversationSection, useUnifiedConversations } from "@/components/features/conversations/unified-chat"
import { CHAT_SCOPES, scopeCount } from "./chat-kind"
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

function ScopedChatSidebar({ props, rows, query, setQuery, stateKey }: SidebarProps & { stateKey: string }) {
  const { agents, activeThreadId: previousThreadId, draftConversation: previousDraft, onStartConversation, onSelectThread, onUnifiedSelect, scope, onScopeChange, threadsByAgent, totalsByAgent, onShowAll } = props
  const chat = useUnifiedConversations()
  const activeThreadId = chat?.selectedId ? null : previousThreadId
  const draftConversation = chat?.selectedId ? null : previousDraft
  const [picking, setPicking] = useState(false)
  const [expanded, setExpanded] = useState<Record<string, boolean>>(() => readFlags(`${stateKey}:agents`))
  const [closed, setClosed] = useState<Record<string, boolean>>(() => readFlags(`${stateKey}:sections`))
  const [unread, setUnread] = useState(false)
  const [live, setLive] = useState(false)
  const [agentFilter, setAgentFilter] = useState<string | null>(null)
  const [sectionFilter, setSectionFilter] = useState<"all" | "agents" | "people" | "rooms">("all")
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
    if (props.pickerSignal === undefined || props.pickerSignal === 0) return
    setPicking(true)
    setQuery("")
    setClosed((old) => ({ ...old, agents: false }))
  }, [props.pickerSignal, setQuery])
  useEffect(() => {
    if (!draftConversation?.id) return
    setPicking(false)
    setQuery("")
    setExpanded((old) => ({ ...old, [draftConversation.agent.id]: true }))
    setClosed((old) => ({ ...old, agents: false }))
  }, [draftConversation?.id, draftConversation?.agent.id, setQuery])
  const revealedThreadId = useRef<string | null>(null)
  useEffect(() => {
    if (!activeThreadId) { revealedThreadId.current = null; return }
    if (revealedThreadId.current === activeThreadId) return
    const selected = rows.find((row) => row.thread.id === activeThreadId)
    if (!selected) return
    revealedThreadId.current = activeThreadId
    setExpanded((old) => old[selected.agent.id] ? old : { ...old, [selected.agent.id]: true })
    setClosed((old) => old.agents ? { ...old, agents: false } : old)
  }, [activeThreadId, rows])
  useEffect(() => { setUnread(false); setLive(false) }, [scope])

  const roster = agents ?? []
  const q = query.trim().toLowerCase()
  const pickerMatches = roster.filter((agent) => !q || agent.name.toLowerCase().includes(q))
  const matching = roster.filter((agent) =>
    (!agentFilter || agentFilter === agent.id) &&
    (!unread || rows.some((row) => row.agent.id === agent.id && (row.thread.unread_count ?? 0) > 0) || rows.some((row) => row.agent.id === agent.id && row.thread.id === activeThreadId)) &&
    (!live || agent.status === "RUNNING" || rows.some((row) => row.agent.id === agent.id && row.thread.id === activeThreadId)) &&
    (!q || agent.name.toLowerCase().includes(q) || rows.some((row) => row.agent.id === agent.id && (row.thread.title || "").toLowerCase().includes(q))),
  )
  const rooms = chat?.list.data?.pages.flatMap((page) => page.conversations) ?? []
  const peopleCount = filterUnifiedConversationRows(rooms, query, "people").length
  const roomCount = filterUnifiedConversationRows(rooms, query, "rooms").length
  const filterCount = Number(unread) + Number(live) + Number(!!agentFilter) + Number(sectionFilter !== "all")
  // Workspace conversations have no agent association in their list payload.
  // Agent-only predicates therefore show the agent roster alone; an empty
  // Team Spaces section would imply those conversations were actually checked.
  const showShared = !unread && !live && !agentFilter
  const showAgents = sectionFilter === "all" || sectionFilter === "agents"
  const showPeople = showShared && (sectionFilter === "all" || sectionFilter === "people") && (!q || peopleCount > 0)
  const showTeamSpaces = showShared && (sectionFilter === "all" || sectionFilter === "rooms") && (!q || roomCount > 0)
  const sectionState = (name: string) => ({ open: !closed[name], onToggle: () => setClosed((old) => ({ ...old, [name]: !old[name] })) })

  function start(agent: ChatTreeAgent) {
    setPicking(false)
    setQuery("")
    setAgentFilter(null)
    setExpanded((old) => ({ ...old, [agent.id]: true }))
    onStartConversation(agent)
  }

  return <div className={cn("flex h-full min-h-0 flex-col", props.className)}>
    <SidebarToolbar>
      <div data-chat-search className="min-w-0 flex-1">
        <SidebarSearch value={query} onValueChange={(value) => { setQuery(value); if (value.trim() && scope !== "all") onScopeChange("all") }} aria-label={picking ? "Search agents" : "Search conversations"} placeholder={picking ? "Find an agent…" : "Search chats, channels…"} onKeyDown={(event) => { if (event.key === "Escape") { setPicking(false); setQuery("") } }} />
      </div>
      {!picking && <SidebarFilterPopover label="Filter chats" activeCount={filterCount} onClear={() => { setUnread(false); setLive(false); setAgentFilter(null); setSectionFilter("all") }}>
        <SidebarFacet label="Show" resetLabel="All conversations" resetActive={sectionFilter === "all" && !unread && !live && !agentFilter} onReset={() => { setSectionFilter("all"); setUnread(false); setLive(false); setAgentFilter(null) }} first>
          <SidebarFacetOption active={sectionFilter === "agents"} onToggle={() => { if (sectionFilter === "agents") { setSectionFilter("all"); setUnread(false); setLive(false); setAgentFilter(null) } else setSectionFilter("agents") }}>Agent sessions</SidebarFacetOption>
          <SidebarFacetOption active={sectionFilter === "people"} onToggle={() => { setUnread(false); setLive(false); setAgentFilter(null); setSectionFilter((value) => value === "people" ? "all" : "people") }}>People</SidebarFacetOption>
          <SidebarFacetOption active={sectionFilter === "rooms"} onToggle={() => { setUnread(false); setLive(false); setAgentFilter(null); setSectionFilter((value) => value === "rooms" ? "all" : "rooms") }}>Team spaces</SidebarFacetOption>
        </SidebarFacet>
        <SidebarFacet label="Agent sessions" resetLabel="All sessions" resetActive={!unread && !live} onReset={() => { setUnread(false); setLive(false); if (!agentFilter) setSectionFilter("all") }}>
          <SidebarFacetOption active={unread} onToggle={() => { setSectionFilter(unread && !live && !agentFilter ? "all" : "agents"); setUnread((value) => !value) }}><MailOpen className="size-3.5" /><span className="flex-1">Unread agent sessions</span></SidebarFacetOption>
          <SidebarFacetOption active={live} onToggle={() => { setSectionFilter(live && !unread && !agentFilter ? "all" : "agents"); setLive((value) => !value) }}><Radio className="size-3.5" /><span className="flex-1">Running agents</span></SidebarFacetOption>
        </SidebarFacet>
        <SidebarFacet label="Agent" resetLabel="All agents" resetActive={!agentFilter} onReset={() => { setAgentFilter(null); if (!unread && !live) setSectionFilter("all") }}>
          {roster.map((agent) => <SidebarFacetOption key={agent.id} active={agentFilter === agent.id} onToggle={() => { setSectionFilter(agentFilter === agent.id && !unread && !live ? "all" : "agents"); setAgentFilter((value) => value === agent.id ? null : agent.id) }}><AgentAvatar seed={agent.avatar_seed || agent.slug} style={agent.avatar_style} avatarUrl={agent.avatar_url} agentId={agent.id} alt="" className="size-4 shrink-0" /><span className="min-w-0 flex-1 truncate">{agent.name}</span></SidebarFacetOption>)}
        </SidebarFacet>
      </SidebarFilterPopover>}
      {props.onToggleCollapse && <SidebarCollapseButton collapsed={false} onToggle={props.onToggleCollapse} {...(props.collapseLabel ? { "aria-label": props.collapseLabel, title: props.collapseLabel } : {})} />}
    </SidebarToolbar>
    <div className="min-h-0 flex-1 overflow-y-auto pb-3">
      {picking ? <section aria-label="Choose an agent"><div className="flex items-center justify-between px-3 py-1"><h3 className="text-[10px] font-semibold uppercase tracking-wider text-foreground/50">New session with</h3><Button variant="ghost" size="sm" onClick={() => setPicking(false)}>Cancel</Button></div>{pickerMatches.map((agent) => <button key={agent.id} type="button" onClick={() => start(agent)} className="kit-tap flex min-h-10 w-full items-center gap-2 px-3 text-left text-xs hover:bg-accent"><AgentAvatar seed={agent.avatar_seed || agent.slug} style={agent.avatar_style} avatarUrl={agent.avatar_url} agentId={agent.id} className="size-6" /><span className="truncate">{agent.name}</span></button>)}{!pickerMatches.length && <p className="p-3 text-xs text-muted-foreground">No agents found.</p>}</section> : <>
        {showAgents && <Section title="Agent activity" count={CHAT_SCOPES.length} {...sectionState("activity")}>
          {CHAT_SCOPES.map((item) => {
            const Icon = item.icon
            const count = scopeCount(item.id, props.kindCounts) ?? (scope === item.id ? rows.length : null)
            return <SidebarRow key={item.id} selected={scope === item.id} onSelect={() => {
              if (item.id === "all") {
                setQuery("")
                setUnread(false)
                setLive(false)
                setAgentFilter(null)
                setSectionFilter("all")
              }
              onScopeChange(scope === item.id && item.id !== "all" ? "all" : item.id)
            }}><Icon className="size-3.5 shrink-0 text-foreground/70" /><span className="min-w-0 flex-1 truncate">{item.label}</span>{count !== null && <span className="rounded-full bg-white/[0.05] px-1.5 text-[10px] tabular-nums text-muted-foreground">{count}</span>}</SidebarRow>
          })}
        </Section>}
        {showPeople && <Section title="People" count={peopleCount} {...sectionState("people")}><UnifiedConversationSection query={query} section="people" showLoadMore={!showTeamSpaces} onSelect={onUnifiedSelect} /></Section>}
        {showAgents && <Section title="Agents" count={matching.length} {...sectionState("agents")}>
          {props.loadError && <p role="alert" className="p-3 text-xs">Agents could not be loaded. <button type="button" className="underline" onClick={props.onRetryRoster}>Retry</button></p>}
          {!props.threadsLoaded && <p className="px-3 py-2 text-xs text-muted-foreground">Loading sessions…</p>}
          {!props.loadError && !!agents && matching.length === 0 && <p className="px-3 py-2 text-xs text-muted-foreground">No matching agents.</p>}
          {matching.map((agent) => {
            const sessions = rows.filter((row) => row.agent.id === agent.id && (!unread || !!row.thread.unread_count || row.thread.id === activeThreadId) && (!live || agent.status === "RUNNING" || row.thread.id === activeThreadId) && (!q || agent.name.toLowerCase().includes(q) || (row.thread.title || "").toLowerCase().includes(q)))
            const isDraft = draftConversation?.agent.id === agent.id
            const selected = isDraft || sessions.some((row) => row.thread.id === activeThreadId)
            const open = expanded[agent.id] || !!query
            const known = Object.hasOwn(threadsByAgent, agent.id)
            const total = totalsByAgent?.[agent.id] ?? threadsByAgent[agent.id]?.length ?? 0
            const unreadCount = sessions.reduce((sum, row) => sum + (row.thread.unread_count ?? 0), 0)
            return <div key={agent.id} className="px-1"><div className={cn("relative flex items-center rounded-md transition-colors hover:bg-white/[0.04]", selected && open && "bg-primary/10 before:absolute before:inset-y-1 before:left-0 before:w-0.5 before:rounded-full before:bg-primary")}>
              <button type="button" aria-label={`${open ? "Hide" : "Show"} ${agent.name} sessions`} aria-expanded={!!open} onClick={() => setExpanded((old) => ({ ...old, [agent.id]: !open }))} className="kit-tap flex size-7 shrink-0 items-center justify-center text-muted-foreground">{open ? <ChevronDown className="size-3" /> : <ChevronRight className="size-3" />}</button>
              <button type="button" aria-label={`Open ${agent.name} chat`} aria-current={selected ? "page" : undefined} onClick={() => { if (selected && open) { setExpanded((old) => ({ ...old, [agent.id]: false })); return }; setExpanded((old) => ({ ...old, [agent.id]: true })); const latest = rows.find((row) => row.agent.id === agent.id); if (latest) onSelectThread(agent, latest.thread); else if ((scope === "direct" || scope === "all") && known && !props.threadErrors?.[agent.id] && props.threadsLoaded) start(agent) }} className="kit-tap flex min-h-9 min-w-0 flex-1 items-center gap-2 text-left text-xs"><AgentAvatar seed={agent.avatar_seed || agent.slug} style={agent.avatar_style} avatarUrl={agent.avatar_url} agentId={agent.id} className="size-6" /><span className="min-w-0 flex-1 truncate">{agent.name}</span>{unreadCount > 0 && <span className="text-[10px] tabular-nums text-primary">{unreadCount}</span>}</button>
              <Button type="button" variant="ghost" size="icon" className="size-7 shrink-0" aria-label={`New session with ${agent.name}`} title={`New session with ${agent.name}`} onClick={() => start(agent)}><Plus className="size-3.5" /></Button>
            </div>
            {open && <motion.div initial={{ height: 0, opacity: 0 }} animate={{ height: "auto", opacity: 1 }} transition={{ duration: 0.18 }} className="ml-7 overflow-hidden border-l border-white/[0.08] pl-2">
              {isDraft && <div role="status" aria-current="page" className="rounded-md bg-primary/10 px-2 py-2 text-xs">{agent.name} · Draft</div>}
              {props.threadErrors?.[agent.id] && <p role="alert" className="p-2 text-xs">Sessions unavailable. <button type="button" className="underline" onClick={props.onRetryThreads}>Retry</button></p>}
              {sessions.map(({ thread }) => <button type="button" key={thread.id} aria-current={thread.id === activeThreadId ? "page" : undefined} onClick={() => onSelectThread(agent, thread)} className={cn("kit-tap block min-h-8 w-full truncate rounded-md px-2 text-left text-xs transition-colors hover:bg-accent", thread.id === activeThreadId && "bg-primary/10 text-primary")}>{thread.title || "Untitled session"}{!!thread.unread_count && <span className="ml-2">{thread.unread_count}</span>}</button>)}
              {!sessions.length && known && props.threadsLoaded && !props.threadErrors?.[agent.id] && !isDraft && <p className="px-2 py-2 text-xs text-muted-foreground">{unread || live ? "No matching sessions." : "No sessions yet."}</p>}
              {onShowAll && (!known || total > (threadsByAgent[agent.id]?.length ?? 0)) && <Button variant="ghost" size="sm" disabled={loading === agent.id} onClick={async () => { setLoading(agent.id); setLoadError(null); try { await onShowAll(agent.id) } catch { setLoadError(agent.id) } finally { setLoading(null) } }}>{known ? "Load older sessions" : "Load sessions"}</Button>}
              {loadError === agent.id && <p role="alert" className="text-xs">Unable to load older sessions. Try again.</p>}
            </motion.div>}
          </div>
          })}
        </Section>}
        {showTeamSpaces && <Section title="Team spaces" count={roomCount} {...sectionState("rooms")}><UnifiedConversationSection query={query} section="rooms" onSelect={onUnifiedSelect} /></Section>}
      </>}
    </div>
  </div>
}
