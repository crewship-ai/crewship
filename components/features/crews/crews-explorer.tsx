"use client"

import Link from "next/link"
import { useCallback, useEffect, useMemo, useState } from "react"
import {
  Activity,
  AlertTriangle,
  ArrowDownUp,
  ChevronRight,
  Clock,
  Crown,
  Ghost,
  List,
  MessageSquare,
  Moon,
  Plus,
  SearchX,
} from "lucide-react"
import { usePagedList } from "@/hooks/use-paged-list"
import { CrewIcon } from "@/components/ui/crew-icon"
import { cn } from "@/lib/utils"
import { AgentAvatar } from "@/components/ui/agent-avatar"
import { InlineEmpty } from "@/components/ui/inline-empty"
import { StatusPill } from "@/components/ui/status-pill"
import {
  DropdownMenu,
  DropdownMenuCheckboxItem,
  DropdownMenuContent,
  DropdownMenuLabel,
  DropdownMenuRadioGroup,
  DropdownMenuRadioItem,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu"
import { isGhost, effectiveStatus, ttlRemaining } from "@/lib/agent-ephemeral"
import {
  SidebarActiveChip,
  SidebarActiveChips,
  SidebarCollapseButton,
  SidebarFacet,
  SidebarFacetOption,
  SidebarFilterPopover,
  SidebarRow,
  SidebarSearch,
  SidebarSection,
  SidebarToolbar,
  SidebarViewButton,
} from "@/components/layout/sidebar-kit"
import {
  EXPLORER_FOLD,
  explorerCountLine,
  foldRows,
  groupExplorerCrews,
  type ExplorerCrew,
  type ExplorerCrewRow,
  type ExplorerGroupKey,
  type ProvisioningState,
} from "./explorer-groups"
import {
  AGENT_BUCKETS,
  DEFAULT_VIEW,
  EMPTY_FILTERS,
  activeFilterCount,
  agentBucket,
  agentMatches,
  bucketCounts,
  crewLastActive,
  crewSubline,
  explorerFacets,
  explorerStateFromParams,
  explorerStateToParams,
  filterChips,
  isNarrowing,
  removeChip,
  shortAgo,
  sortAgents,
  toggleFacet,
  type AgentBucket,
  type ExplorerFilterAgent,
  type ExplorerFilterContext,
  type ExplorerFilters,
  type ExplorerView,
} from "./explorer-filters"

interface CrewData extends ExplorerCrew {
  _count?: { agents: number }
}

interface AgentData extends ExplorerFilterAgent {
  avatar_seed?: string | null
  avatar_style?: string | null
  /** Stored avatar render (#1297); null means generate from the seed. */
  avatar_url?: string | null
  crew?: { name?: string; avatar_style?: string | null } | null
}

export interface CrewsExplorerProps {
  workspaceId?: string
  onAgentSlugSelect?: (slug: string) => void
  onCrewSlugSelect?: (slug: string) => void
  crews: CrewData[]
  agents: AgentData[]
  selectedCrewId: string | null
  selectedAgentId: string | null
  collapsed: boolean
  onToggleCollapse: () => void
  onCrewSelect: (crewId: string) => void
  onAgentSelect: (agentId: string) => void
  /** "Add agent" on a crew row; opens New agent with that crew picked. */
  onAddAgent?: (crewSlug: string) => void
  /** Server totals (X-Total-Count); null on a server that does not page. */
  crewsTotal?: number | null
  agentsTotal?: number | null
  /** More rows exist past what is loaded. */
  hasMore?: boolean
  loadingMore?: boolean
  onLoadMore?: () => void
  provisioningByCrew?: ReadonlyMap<string, ProvisioningState>
  gapsByCrew?: ReadonlyMap<string, number>
  /** Missions in progress per crew, for the line under the crew's name. */
  runningMissionsByCrew?: ReadonlyMap<string, number>
}

const BUCKET_ICON: Record<AgentBucket, { icon: typeof List; tone: string }> = {
  all: { icon: List, tone: "text-foreground/70" },
  needs: { icon: AlertTriangle, tone: "text-warn" },
  working: { icon: Activity, tone: "text-primary" },
  idle: { icon: Moon, tone: "text-muted-foreground" },
  expired: { icon: Ghost, tone: "text-muted-foreground-soft" },
}

/** How many agents a flat list (grouped by status, or not at all) shows before "N more". */
const FLAT_FOLD = 30

/**
 * Filters and view live in the URL next to ?crew= and ?agent=, written with
 * replaceState like useCrewsSelection, so a link opens the same list and a
 * pick never re-renders the dashboard layout.
 */
function useExplorerUrlState() {
  const read = () =>
    typeof window === "undefined"
      ? { filters: EMPTY_FILTERS, view: DEFAULT_VIEW }
      : explorerStateFromParams(new URLSearchParams(window.location.search))
  const [state, setState] = useState(read)

  useEffect(() => {
    const onPop = () => setState(read())
    window.addEventListener("popstate", onPop)
    return () => window.removeEventListener("popstate", onPop)
  }, [])

  const write = useCallback((next: { filters: ExplorerFilters; view: ExplorerView }) => {
    setState(next)
    const params = new URLSearchParams(window.location.search)
    for (const [k, v] of Object.entries(explorerStateToParams(next.filters, next.view))) {
      if (v == null) params.delete(k)
      else params.set(k, v)
    }
    const qs = params.toString()
    const url = qs ? `${window.location.pathname}?${qs}` : window.location.pathname
    if (window.location.pathname + window.location.search !== url) window.history.replaceState(null, "", url)
  }, [])

  return {
    filters: state.filters,
    view: state.view,
    setFilters: (filters: ExplorerFilters) => write({ filters, view: state.view }),
    setView: (view: ExplorerView) => write({ filters: state.filters, view }),
  }
}

/** A clock that moves once a minute — enough for "5m" and "Active 2h ago". */
function useMinuteClock() {
  const [now, setNow] = useState(() => Date.now())
  useEffect(() => {
    const t = setInterval(() => setNow(Date.now()), 60_000)
    return () => clearInterval(t)
  }, [])
  return now
}

const DOT: Partial<Record<string, string>> = {
  RUNNING: "bg-primary animate-pulse",
  ERROR: "bg-destructive",
  WAITING: "bg-warn",
  PENDING_REVIEW: "bg-warn",
  AWAITING_APPROVAL: "bg-warn",
  PAUSED: "bg-warn",
}

/**
 * The left column of /crews, on the Routines/Skills skeleton (#3043):
 * search · Filter · View, a Status section that counts agents, then the
 * crews (attention first, idle folded after six) or a flat list of agents.
 * The derivations are pure (explorer-groups.ts, explorer-filters.ts).
 */
export function CrewsExplorer({
  workspaceId, onAgentSlugSelect, onCrewSlugSelect,
  crews,
  agents,
  selectedCrewId,
  selectedAgentId,
  collapsed,
  onToggleCollapse,
  onCrewSelect,
  onAgentSelect,
  onAddAgent,
  crewsTotal = null,
  agentsTotal = null,
  hasMore = false,
  loadingMore = false,
  onLoadMore,
  provisioningByCrew,
  gapsByCrew,
  runningMissionsByCrew,
}: CrewsExplorerProps) {
  const [search, setSearch] = useState("")
  const [query, setQuery] = useState("")
  useEffect(() => { const timer = setTimeout(() => setQuery(search.trim()), 200); return () => clearTimeout(timer) }, [search])
  const remote = Boolean(workspaceId && search.trim())
  const searchQuery = workspaceId && query ? `workspace_id=${encodeURIComponent(workspaceId)}&q=${encodeURIComponent(query)}` : null
  const foundAgents = usePagedList<AgentData>({ url: searchQuery ? `/api/v1/agents?${searchQuery}` : null })
  const foundCrews = usePagedList<CrewData>({ url: searchQuery ? `/api/v1/crews?${searchQuery}` : null })

  const { filters, view, setFilters, setView } = useExplorerUrlState()
  const now = useMinuteClock()
  const ctx: ExplorerFilterContext = useMemo(() => ({ now, provisioningByCrew, gapsByCrew }), [now, provisioningByCrew, gapsByCrew])
  const filtering = filters.bucket !== "all" || activeFilterCount(filters) > 0
  const narrowed = isNarrowing(filters, search)

  // `/` jumps to the search, as on Routines and Issues.
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      const t = e.target as HTMLElement | null
      if (e.key !== "/" || (t && (t.tagName === "INPUT" || t.tagName === "TEXTAREA" || t.isContentEditable))) return
      const el = document.querySelector<HTMLInputElement>("[data-crews-search] input")
      if (el) { e.preventDefault(); el.focus(); el.select() }
    }
    document.addEventListener("keydown", onKey)
    return () => document.removeEventListener("keydown", onKey)
  }, [])

  // Every group folds after six (README §4: priority, cap, fold). On a host
  // without a container runtime every crew "needs a rebuild", and an
  // unfolded attention group of a hundred is the wall the fold exists for.
  const [showAll, setShowAll] = useState<Partial<Record<string, boolean>>>({})
  const [statusOpen, setStatusOpen] = useState(true)

  const crewName = useMemo(() => new Map(crews.map((c) => [c.id, c.name])), [crews])
  const agentsByCrew = useMemo(() => {
    const m = new Map<string, AgentData[]>()
    for (const a of agents) if (a.crew_id) m.set(a.crew_id, [...(m.get(a.crew_id) ?? []), a])
    return m
  }, [agents])

  const grouped = useMemo(() => {
    const sortKey = view.sort === "active" ? "active" : "name"
    const crewOrder =
      view.sort === "name"
        ? (a: ExplorerCrewRow, b: ExplorerCrewRow) => a.crew.name.localeCompare(b.crew.name)
        : view.sort === "active"
          ? (a: ExplorerCrewRow, b: ExplorerCrewRow) =>
              (crewLastActive(agentsByCrew.get(b.crew.id) ?? []) ?? -Infinity) - (crewLastActive(agentsByCrew.get(a.crew.id) ?? []) ?? -Infinity) ||
              a.crew.name.localeCompare(b.crew.name)
          : undefined
    return groupExplorerCrews<AgentData>({
      crews, agents, search, provisioningByCrew, gapsByCrew,
      match: filtering ? (a) => agentMatches(a, filters, ctx) : undefined,
      sortAgents: (list) => sortAgents(list, sortKey),
      crewOrder,
    })
  }, [crews, agents, search, provisioningByCrew, gapsByCrew, filtering, filters, ctx, view.sort, agentsByCrew])

  const counts = useMemo(() => {
    const q = search.trim().toLowerCase()
    const hit = (a: AgentData) => !q || [a.name, a.slug, a.role_title].some((f) => f?.toLowerCase().includes(q))
    return bucketCounts(agents.filter(hit), filters, ctx)
  }, [agents, search, filters, ctx])
  const facets = useMemo(() => explorerFacets(agents, filters, ctx), [agents, filters, ctx])
  const chips = filterChips(filters)

  // Open the first few crews that need a look and the selected one; the rest
  // stay closed until asked. A crew that ENTERS the visible attention rows
  // opens too.
  const [expandedCrews, setExpandedCrews] = useState<Set<string>>(() => new Set())
  useEffect(() => {
    setExpandedCrews((prev) => {
      const next = new Set(prev)
      let changed = false
      const attention = grouped.groups.find((g) => g.key === "attention")
      for (const r of attention?.rows.slice(0, EXPLORER_FOLD) ?? []) {
        if (!next.has(r.crew.id)) { next.add(r.crew.id); changed = true }
      }
      if (selectedCrewId && !next.has(selectedCrewId)) { next.add(selectedCrewId); changed = true }
      return changed ? next : prev
    })
  }, [grouped, selectedCrewId])

  const toggleCrew = useCallback((crewId: string) => {
    setExpandedCrews((prev) => {
      const next = new Set(prev)
      if (next.has(crewId)) next.delete(crewId)
      else next.add(crewId)
      return next
    })
  }, [])

  const flat = useMemo(() => {
    if (view.group === "crew") return []
    const q = search.trim().toLowerCase()
    const list = agents.filter((a) => (!q || [a.name, a.slug, a.role_title].some((f) => f?.toLowerCase().includes(q))) && agentMatches(a, filters, ctx))
    return sortAgents(list, view.sort === "active" ? "active" : "name", { leadFirst: false })
  }, [view.group, view.sort, agents, search, filters, ctx])

  const countLine = explorerCountLine({
    search,
    narrowed,
    crewsTotal,
    agentsTotal,
    matchedCrews: view.group === "crew" ? grouped.matchedCrews : new Set(flat.map((a) => a.crew_id).filter(Boolean)).size,
    matchedAgents: view.group === "crew" ? grouped.matchedAgents : flat.length,
  })
  const nothingMatches =
    narrowed && (view.group === "crew" ? grouped.matchedCrews === 0 && grouped.unassigned.length === 0 : flat.length === 0)
  const clearAll = () => { setSearch(""); setFilters(EMPTY_FILTERS) }

  if (collapsed) {
    // The rail keeps the crews in reach, with a dot on the ones that need a
    // person — an empty strip told nothing.
    const rows = grouped.groups.flatMap((g) => g.rows)
    return (
      <div className="flex h-full flex-col items-center gap-1.5 overflow-y-auto border-r border-foreground/[0.1] bg-card py-2">
        <SidebarCollapseButton collapsed onToggle={onToggleCollapse} />
        {rows.map((r) => (
          <button
            key={r.crew.id}
            type="button"
            title={r.pill ? `${r.crew.name} · ${r.pill.label}` : r.crew.name}
            aria-label={r.pill ? `${r.crew.name}, ${r.pill.label}` : r.crew.name}
            onClick={() => { onCrewSelect(r.crew.id); onToggleCollapse() }}
            className={cn("kit-tap relative rounded-lg", selectedCrewId === r.crew.id && "ring-2 ring-primary/50")}
          >
            <CrewIcon icon={r.crew.icon || "briefcase"} color={r.crew.color} size="sm" />
            {r.pill && (
              <span
                aria-hidden
                className={cn(
                  "absolute -right-0.5 -top-0.5 h-2 w-2 rounded-full ring-2 ring-card",
                  r.pill.tone === "danger" ? "bg-destructive" : r.pill.tone === "warn" ? "bg-warn" : "bg-primary",
                )}
              />
            )}
          </button>
        ))}
      </div>
    )
  }

  const renderAgent = (
    agent: AgentData,
    { showCrew = false, onPick }: { showCrew?: boolean; onPick?: () => void } = {},
  ) => {
    const ghost = isGhost(agent)
    const isAgentSelected = selectedAgentId === agent.id
    const lead = agent.agent_role === "LEAD"
    const ttl = agent.ephemeral && !ghost ? ttlRemaining(agent.expires_at, now) : ""
    const ago = shortAgo(agent.last_active_at, now)
    const dot = ghost ? null : DOT[agent.status]
    const crewLabel = showCrew ? (agent.crew_id ? crewName.get(agent.crew_id) ?? agent.crew?.name : "No crew") : null
    const sub = [crewLabel, ttl, agent.role_title || agent.agent_role].filter(Boolean).join(" · ")
    return (
      <SidebarRow
        key={agent.id}
        as="div"
        selected={isAgentSelected}
        aria-label={agent.name}
        onSelect={onPick ?? (() => onAgentSelect(agent.id))}
        className={cn("group/agent", ghost && "opacity-55 grayscale-[0.4] hover:opacity-90 hover:grayscale-0")}
      >
        <span className="relative shrink-0">
          <AgentAvatar
            seed={agent.avatar_seed || agent.name}
            style={agent.avatar_style || agent.crew?.avatar_style}
            avatarUrl={agent.avatar_url}
            className="h-8 w-8 rounded-lg shrink-0"
          />
          {dot && <span aria-hidden className={cn("absolute -bottom-0.5 -right-0.5 h-2 w-2 rounded-full ring-2 ring-card", dot)} />}
        </span>
        <div className="min-w-0 flex-1">
          <span className="type-nav flex items-center gap-1 font-medium">
            <span className="truncate">{agent.name}</span>
            {lead && <Crown className="h-3 w-3 shrink-0 text-warn" aria-label="Lead" />}
            {ttl && <Clock className="h-2.5 w-2.5 shrink-0 text-notice/80" aria-label="Temporary hire" />}
          </span>
          {view.details && sub && <span className="type-nav-sub block truncate text-muted-foreground">{sub}</span>}
        </div>
        {!ghost && (
          <Link
            href={`/chat/${encodeURIComponent(agent.slug)}`}
            onClick={(e) => e.stopPropagation()}
            aria-label={`Chat with ${agent.name}`}
            title={`Chat with ${agent.name}`}
            className="kit-tap hidden h-6 w-6 shrink-0 place-items-center rounded-md text-muted-foreground hover:bg-foreground/[0.06] hover:text-foreground group-hover/agent:grid group-focus-within/agent:grid"
          >
            <MessageSquare className="h-3 w-3" />
          </Link>
        )}
        {agent.status !== "IDLE" || ghost ? (
          <StatusPill status={effectiveStatus(agent)} live={agent.status === "RUNNING" && !ghost} />
        ) : (
          ago && <span className="type-nav-sub font-mono tabular-nums text-muted-foreground-soft shrink-0" title="Last active">{ago}</span>
        )}
      </SidebarRow>
    )
  }

  const renderCrew = (row: ExplorerCrewRow) => {
    const { crew } = row
    // Under a search or a filter the matches show without a click: a list of
    // closed crews would hide exactly what was asked for.
    const expanded = expandedCrews.has(crew.id) || (narrowed && row.agents.length > 0)
    const isSelected = selectedCrewId === crew.id && !selectedAgentId
    const roster = agentsByCrew.get(crew.id) ?? []
    return (
      <div
        key={crew.id}
        className="mb-0.5"
        onKeyDown={(e) => {
          if (e.key === "ArrowRight" && !expanded) { e.preventDefault(); toggleCrew(crew.id) }
          if (e.key === "ArrowLeft" && expanded) { e.preventDefault(); toggleCrew(crew.id) }
        }}
      >
        <SidebarRow
          as="div"
          selected={isSelected}
          aria-label={crew.name}
          aria-expanded={expanded}
          className="group/crew"
          onSelect={() => {
            onCrewSelect(crew.id)
            if (!expanded) toggleCrew(crew.id)
          }}
        >
          {/* Presentational: the row itself carries the expanded state and
              the arrow keys; the chevron only adds a mouse way to collapse. */}
          <span aria-hidden="true" className="shrink-0" onClick={(e) => { e.stopPropagation(); toggleCrew(crew.id) }}>
            <ChevronRight
              className={cn(
                "h-3 w-3 text-muted-foreground-soft transition-all",
                expanded ? "rotate-90 opacity-0 group-hover/crew:opacity-100 group-focus-within/crew:opacity-100" : "opacity-100",
              )}
            />
          </span>
          <CrewIcon icon={crew.icon || "briefcase"} color={crew.color} size="sm" />
          <div className="min-w-0 flex-1">
            <span className="type-nav block truncate font-semibold">{crew.name}</span>
            {view.details && (
              <span className="type-nav-sub block truncate text-muted-foreground">
                {crewSubline(roster, runningMissionsByCrew?.get(crew.id) ?? 0, now)}
              </span>
            )}
          </div>
          {onAddAgent && (
            <button
              type="button"
              onClick={(e) => { e.stopPropagation(); onAddAgent(crew.slug) }}
              aria-label={`Add agent to ${crew.name}`}
              title={`Add agent to ${crew.name}`}
              className="kit-tap hidden h-6 w-6 shrink-0 place-items-center rounded-md text-muted-foreground hover:bg-foreground/[0.06] hover:text-foreground group-hover/crew:grid group-focus-within/crew:grid"
            >
              <Plus className="h-3 w-3" />
            </button>
          )}
          {row.pill && <StatusPill tone={row.pill.tone} label={row.pill.label} live={row.pill.tone === "blue"} />}
          <span className="type-nav-sub text-muted-foreground-soft tabular-nums shrink-0">
            {narrowed ? `${row.agents.length}/${row.agentCount}` : row.agentCount}
          </span>
        </SidebarRow>

        {expanded && row.agents.length > 0 && (
          <div className="relative ml-[1.1rem] border-l border-border/70 pl-1">
            {row.agents.map((a) => renderAgent(a as AgentData))}
          </div>
        )}
      </div>
    )
  }

  const moreButton = (key: string, hidden: number, noun: string, rest: string) => (
    <button
      type="button"
      onClick={() => setShowAll((prev) => ({ ...prev, [key]: true }))}
      className="kit-tap mx-1 my-1 flex w-[calc(100%-0.5rem)] items-center justify-between rounded-md border border-border/60 px-2.5 py-1.5 text-left type-nav-sub hover:bg-foreground/[0.03]"
    >
      <span className="text-muted-foreground"><span className="font-medium text-foreground/90">{hidden} more {noun}</span>{rest && ` · ${rest}`}</span>
      <span className="inline-flex items-center gap-1 text-primary-hover">Show all <ChevronRight className="h-3 w-3" /></span>
    </button>
  )

  const crewsBody = (
    <SidebarSection label="Crews" count={grouped.matchedCrews} className="mt-1">
      {grouped.groups.map((group) => {
        const { visible, hidden } = foldRows(group.rows, showAll[group.key] === true)
        const rest = group.key === "attention" ? "need attention" : group.key === "running" ? "running" : "idle"
        return (
          <div key={group.key} data-group={group.key as ExplorerGroupKey}>
            {visible.map(renderCrew)}
            {hidden > 0 && moreButton(group.key, hidden, hidden === 1 ? "crew" : "crews", rest)}
          </div>
        )
      })}
    </SidebarSection>
  )

  const flatBody =
    view.group === "status"
      ? AGENT_BUCKETS.filter((b) => b.id !== "all").map((b) => {
          const list = flat.filter((a) => agentBucket(a) === b.id)
          if (list.length === 0) return null
          const { visible, hidden } = foldRows(list, showAll[`flat-${b.id}`] === true, FLAT_FOLD)
          return (
            <SidebarSection key={b.id} label={b.label} count={list.length} className="mt-1">
              {visible.map((a) => renderAgent(a, { showCrew: true }))}
              {hidden > 0 && moreButton(`flat-${b.id}`, hidden, hidden === 1 ? "agent" : "agents", "")}
            </SidebarSection>
          )
        })
      : (() => {
          const { visible, hidden } = foldRows(flat, showAll.flat === true, FLAT_FOLD)
          return (
            <SidebarSection label="Agents" count={flat.length} className="mt-1">
              {visible.map((a) => renderAgent(a, { showCrew: true }))}
              {hidden > 0 && moreButton("flat", hidden, hidden === 1 ? "agent" : "agents", "")}
            </SidebarSection>
          )
        })()

  return (
    <div className="flex flex-col h-full bg-card border-r border-foreground/[0.1] overflow-hidden">
      <div className="flex-1 min-h-0 flex flex-col">
        <SidebarToolbar>
          <div data-crews-search className="min-w-0 flex-1">
            <SidebarSearch value={search} onValueChange={setSearch} placeholder="Search crews, agents…" />
          </div>
          <DropdownMenu>
            <DropdownMenuTrigger asChild>
              <SidebarViewButton aria-label="View: group and sort">
                <ArrowDownUp className="h-3.5 w-3.5" />
              </SidebarViewButton>
            </DropdownMenuTrigger>
            <DropdownMenuContent align="end" className="w-56">
              <DropdownMenuLabel>Group by</DropdownMenuLabel>
              <DropdownMenuRadioGroup value={view.group} onValueChange={(v) => setView({ ...view, group: v as ExplorerView["group"] })}>
                <DropdownMenuRadioItem value="crew">Crew</DropdownMenuRadioItem>
                <DropdownMenuRadioItem value="status">Status</DropdownMenuRadioItem>
                <DropdownMenuRadioItem value="none">Nothing (A–Z)</DropdownMenuRadioItem>
              </DropdownMenuRadioGroup>
              <DropdownMenuSeparator />
              <DropdownMenuLabel>Sort</DropdownMenuLabel>
              <DropdownMenuRadioGroup value={view.sort} onValueChange={(v) => setView({ ...view, sort: v as ExplorerView["sort"] })}>
                {view.group === "crew" && <DropdownMenuRadioItem value="size">Crew size</DropdownMenuRadioItem>}
                <DropdownMenuRadioItem value="name">Name</DropdownMenuRadioItem>
                <DropdownMenuRadioItem value="active">Last active</DropdownMenuRadioItem>
              </DropdownMenuRadioGroup>
              <DropdownMenuSeparator />
              <DropdownMenuCheckboxItem checked={view.details} onCheckedChange={(c) => setView({ ...view, details: c === true })}>
                Show roles and activity
              </DropdownMenuCheckboxItem>
            </DropdownMenuContent>
          </DropdownMenu>
          <SidebarFilterPopover
            label="Filter agents"
            activeCount={activeFilterCount(filters)}
            onClear={() => setFilters({ ...EMPTY_FILTERS, bucket: filters.bucket })}
            panelClassName="min-w-[230px] max-h-[440px]"
            triggerLabel={<span className="sr-only">Filter</span>}
          >
            {facets.length === 0 && (
              <p className="px-3 py-2 type-nav-sub text-muted-foreground">Every agent here is alike; there is nothing to filter on yet.</p>
            )}
            {facets.map((f, i) => (
              <SidebarFacet
                key={f.key}
                first={i === 0}
                label={f.label}
                resetLabel={`Any ${f.label.toLowerCase()}`}
                resetActive={filters[f.key].length === 0}
                onReset={() => setFilters({ ...filters, [f.key]: [] })}
              >
                {f.options.map((o) => (
                  <SidebarFacetOption
                    key={o.value}
                    active={(filters[f.key] as string[]).includes(o.value)}
                    onToggle={() => setFilters(toggleFacet(filters, f.key, o.value))}
                  >
                    <span className="flex-1 truncate">{o.label}</span>
                    <span className="type-nav-sub font-mono tabular-nums text-muted-foreground-soft">{o.count}</span>
                  </SidebarFacetOption>
                ))}
              </SidebarFacet>
            ))}
          </SidebarFilterPopover>
          <SidebarCollapseButton collapsed={false} onToggle={onToggleCollapse} />
        </SidebarToolbar>

        {/* The count is the server's total, not the page: "100 crews" on a
            workspace with 103 was the audit's first finding. */}
        <div className="flex items-center justify-between gap-2 px-3 pb-1 type-nav-sub text-muted-foreground" aria-live="polite">
          <span className="truncate" data-testid="explorer-count">{remote ? `${foundCrews.total ?? "—"} crews · ${foundAgents.total ?? "—"} agents match` : countLine}</span>
          {narrowed && (
            <button type="button" className="shrink-0 text-primary-hover hover:underline kit-tap" onClick={clearAll}>
              Clear
            </button>
          )}
        </div>

        <SidebarActiveChips>
          {chips.map((c) => (
            <SidebarActiveChip key={`${c.key}:${c.value}`} onRemove={() => setFilters(removeChip(filters, c))}>
              {c.label}
            </SidebarActiveChip>
          ))}
        </SidebarActiveChips>

        <div className="flex-1 overflow-y-auto px-1 pb-2">
          {remote ? (
            <div className="space-y-0.5 py-1">
              {foundAgents.error || foundCrews.error ? (
                <p role="alert" className="px-2 type-nav">Search could not be loaded. <button onClick={() => { void foundAgents.refresh(); void foundCrews.refresh() }} className="text-primary">Retry</button></p>
              ) : query !== search.trim() || foundAgents.loading || foundCrews.loading ? (
                <p role="status" className="px-2 type-nav text-muted-foreground">Searching…</p>
              ) : (
                <>
                  {foundCrews.items.length > 0 && (
                    <SidebarSection label="Crews" count={foundCrews.total ?? foundCrews.items.length}>
                      {foundCrews.items.map((crew) => (
                        <SidebarRow key={crew.id} as="div" aria-label={crew.name} onSelect={() => onCrewSlugSelect?.(crew.slug)}>
                          <CrewIcon icon={crew.icon || "briefcase"} color={crew.color} size="sm" />
                          <span className="type-nav flex-1 truncate font-semibold">{crew.name}</span>
                          {crew._count && <span className="type-nav-sub text-muted-foreground-soft tabular-nums">{crew._count.agents}</span>}
                        </SidebarRow>
                      ))}
                    </SidebarSection>
                  )}
                  {foundAgents.items.length > 0 && (
                    <SidebarSection label="Agents" count={foundAgents.total ?? foundAgents.items.length}>
                      {/* A found agent may be past the loaded page: select by slug. */}
                      {foundAgents.items.map((agent) => renderAgent(agent, { showCrew: true, onPick: () => onAgentSlugSelect?.(agent.slug) }))}
                    </SidebarSection>
                  )}
                  {!foundAgents.items.length && !foundCrews.items.length && <p className="px-2 type-nav text-muted-foreground">No matching crews or agents.</p>}
                  {(foundAgents.hasMore || foundCrews.hasMore) && <button className="px-2 type-nav text-primary" disabled={foundAgents.loadingMore || foundCrews.loadingMore} onClick={() => { void foundAgents.loadMore(); void foundCrews.loadMore() }}>Load more results</button>}
                </>
              )}
            </div>
          ) : (
            <>
              {/* Status — single-select buckets, counted over agents. */}
              <SidebarSection
                label="Status"
                count={AGENT_BUCKETS.length}
                collapsible
                collapsed={!statusOpen}
                onToggle={() => setStatusOpen(!statusOpen)}
                className="border-b border-foreground/[0.06] pb-1"
              >
                {AGENT_BUCKETS.map((b) => {
                  const { icon: Icon, tone } = BUCKET_ICON[b.id]
                  const n = counts[b.id]
                  const selected = filters.bucket === b.id
                  return (
                    <SidebarRow
                      key={b.id}
                      as="div"
                      selected={selected}
                      data-testid={`crews-bucket-${b.id}`}
                      onSelect={() => setFilters({ ...filters, bucket: b.id })}
                    >
                      <Icon className={cn("h-3.5 w-3.5 shrink-0", tone, n === 0 && !selected && "opacity-40")} />
                      <span className={cn("flex-1 truncate", n === 0 && !selected ? "text-muted-foreground-soft" : "text-foreground/80")}>{b.label}</span>
                      <span
                        className={cn(
                          "type-nav-sub rounded-full px-1.5 py-px tabular-nums",
                          n === 0 ? "text-muted-foreground-soft" : selected ? "bg-primary/15 text-primary-hover" : "bg-foreground/[0.05] text-muted-foreground",
                        )}
                      >
                        {n}
                      </span>
                    </SidebarRow>
                  )
                })}
              </SidebarSection>

              {nothingMatches ? (
                <InlineEmpty
                  icon={SearchX}
                  className="mx-1 my-2"
                  text={search.trim()
                    ? <>Nothing matches “{search.trim()}”{filtering ? " with these filters" : ""}. Search looks at crew and agent names, slugs and roles.</>
                    : <>No agent matches these filters.</>}
                  action={<button type="button" className="text-primary-hover hover:underline" onClick={clearAll}>Clear</button>}
                />
              ) : view.group === "crew" ? (
                <>
                  {crewsBody}
                  {grouped.unassigned.length > 0 && (
                    <SidebarSection label="Unassigned" count={grouped.unassigned.length} className="mt-2 border-t border-border pt-1">
                      {grouped.unassigned.map((a) => renderAgent(a as AgentData))}
                    </SidebarSection>
                  )}
                </>
              ) : (
                flatBody
              )}

              {hasMore && onLoadMore && (
                // Shown under a no-match too: the search covers only what is
                // loaded, and the rest is one click away.
                <button
                  type="button"
                  onClick={onLoadMore}
                  disabled={loadingMore}
                  className="kit-tap mx-1 my-2 flex w-[calc(100%-0.5rem)] items-center justify-between rounded-md border border-dashed border-border/60 px-2.5 py-1.5 text-left type-nav-sub text-muted-foreground hover:bg-foreground/[0.03] disabled:opacity-60"
                >
                  <span>
                    {crewsTotal != null && crewsTotal > crews.length
                      ? `${crewsTotal - crews.length} more ${crewsTotal - crews.length === 1 ? "crew" : "crews"} not loaded`
                      : "More agents not loaded"}
                  </span>
                  <span className="text-primary-hover">{loadingMore ? "Loading…" : "Load"}</span>
                </button>
              )}
            </>
          )}
        </div>
      </div>
    </div>
  )
}
