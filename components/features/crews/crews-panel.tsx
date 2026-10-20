"use client"

import { useCallback, useEffect, useMemo, useState } from "react"
import { CircleDot, Clock, Hand, LayoutGrid, Moon, Play, Plus, SearchX } from "lucide-react"
import type { LucideIcon } from "lucide-react"

import { usePagedList } from "@/hooks/use-paged-list"
import { AgentAvatar } from "@/components/ui/agent-avatar"
import { InlineEmpty } from "@/components/ui/inline-empty"
import { StatusPill } from "@/components/ui/status-pill"
import { SidebarRow, SidebarSearch, SidebarSection, SidebarToolbar } from "@/components/layout/sidebar-kit"
import { cn } from "@/lib/utils"

import { CrSegment, LeadMark, LeadStar } from "./cr-card"
import { CrewTile } from "./crew-tile"
import type { ProvisioningState } from "./explorer-groups"
import {
  CREW_VIEWS,
  DEFAULT_PANEL_STATE,
  buildPanel,
  panelStateFromParams,
  panelStateToParams,
  tempHireLabel,
  type CrewGrouping,
  type CrewView,
  type PanelAgent,
  type PanelCrew,
  type PanelCrewRow,
  type PanelState,
} from "./crews-panel-model"

export interface CrewsPanelCrew extends PanelCrew {
  description?: string | null
}

export interface CrewsPanelAgent extends PanelAgent {
  avatar_seed?: string | null
  avatar_style?: string | null
  avatar_url?: string | null
  crew?: { avatar_style?: string | null } | null
}

export interface CrewsPanelProps {
  workspaceId?: string
  crews: CrewsPanelCrew[]
  agents: CrewsPanelAgent[]
  /** The crew whose agents are listed under it: the open crew, or the open agent's crew. */
  openCrewId: string | null
  selectedCrewId: string | null
  selectedAgentId: string | null
  onCrewSelect: (slug: string) => void
  onAgentSelect: (agent: { slug: string; crewSlug: string | null }) => void
  onNewCrew?: () => void
  /** Crews past the loaded page. */
  hasMore?: boolean
  loadingMore?: boolean
  onLoadMore?: () => void
  crewsTotal?: number | null
  provisioningByCrew?: ReadonlyMap<string, ProvisioningState>
  gapsByCrew?: ReadonlyMap<string, number>
}

const VIEW_ICON: Record<CrewView, LucideIcon> = {
  all: LayoutGrid,
  needs: Hand,
  working: Play,
  idle: Moon,
  temp: Clock,
}

/** ?status= and ?group= next to ?crew= and ?agent=, written with replaceState. */
export function usePanelUrlState(): [PanelState, (next: PanelState) => void] {
  const read = () =>
    typeof window === "undefined" ? DEFAULT_PANEL_STATE : panelStateFromParams(new URLSearchParams(window.location.search))
  const [state, setState] = useState<PanelState>(read)
  useEffect(() => {
    const onPop = () => setState(read())
    window.addEventListener("popstate", onPop)
    return () => window.removeEventListener("popstate", onPop)
  }, [])
  const write = useCallback((next: PanelState) => {
    setState(next)
    const params = new URLSearchParams(window.location.search)
    for (const [k, v] of Object.entries(panelStateToParams(next))) {
      if (v == null) params.delete(k)
      else params.set(k, v)
    }
    const qs = params.toString()
    const url = qs ? `${window.location.pathname}?${qs}` : window.location.pathname
    if (window.location.pathname + window.location.search !== url) window.history.replaceState(null, "", url)
  }, [])
  return [state, write]
}

function typing(t: EventTarget | null): boolean {
  const el = t as HTMLElement | null
  return !!el && (el.tagName === "INPUT" || el.tagName === "TEXTAREA" || el.tagName === "SELECT" || el.isContentEditable)
}

/**
 * The /crews secondary panel (Harbor 2): Views, then the crews grouped by
 * status or A–Z, the open crew's agents under it, and "Without a crew".
 */
export function CrewsPanel({
  workspaceId,
  crews,
  agents,
  openCrewId,
  selectedCrewId,
  selectedAgentId,
  onCrewSelect,
  onAgentSelect,
  onNewCrew,
  hasMore = false,
  loadingMore = false,
  onLoadMore,
  crewsTotal = null,
  provisioningByCrew,
  gapsByCrew,
}: CrewsPanelProps) {
  const [state, setState] = usePanelUrlState()
  const [search, setSearch] = useState("")
  const [query, setQuery] = useState("")
  useEffect(() => { const t = setTimeout(() => setQuery(search.trim()), 200); return () => clearTimeout(t) }, [search])

  // Past the loaded page the server searches; a found row may not be loaded.
  const remote = Boolean(workspaceId && hasMore && search.trim())
  const searchQuery = remote && query ? `workspace_id=${encodeURIComponent(workspaceId ?? "")}&q=${encodeURIComponent(query)}` : null
  const foundAgents = usePagedList<CrewsPanelAgent>({ url: searchQuery ? `/api/v1/agents?${searchQuery}` : null })
  const foundCrews = usePagedList<CrewsPanelCrew>({ url: searchQuery ? `/api/v1/crews?${searchQuery}` : null })

  const [now, setNow] = useState(() => Date.now())
  useEffect(() => { const t = setInterval(() => setNow(Date.now()), 60_000); return () => clearInterval(t) }, [])

  const model = useMemo(
    () => buildPanel({ crews, agents, provisioningByCrew, gapsByCrew, state, search }),
    [crews, agents, provisioningByCrew, gapsByCrew, state, search],
  )

  // `/` jumps to the search; 1–5 pick a view (PNL-02).
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.metaKey || e.ctrlKey || e.altKey || typing(e.target)) return
      if (e.key === "/") {
        const el = document.querySelector<HTMLInputElement>("[data-crews-search] input")
        if (el) { e.preventDefault(); el.focus(); el.select() }
        return
      }
      const i = Number(e.key)
      if (Number.isInteger(i) && i >= 1 && i <= CREW_VIEWS.length && !document.querySelector("[role=dialog]")) {
        setState({ ...state, view: CREW_VIEWS[i - 1].id })
      }
    }
    document.addEventListener("keydown", onKey)
    return () => document.removeEventListener("keydown", onKey)
  }, [state, setState])

  const crewSlugById = useMemo(() => new Map(crews.map((c) => [c.id, c.slug])), [crews])

  const agentRow = (a: CrewsPanelAgent, opts: { indent?: boolean; onPick?: () => void } = {}) => {
    const temp = tempHireLabel(a, now)
    const lead = a.agent_role === "LEAD"
    const ghost = Boolean(a.expired_at)
    return (
      <SidebarRow
        key={a.id}
        as="div"
        indent={opts.indent}
        selected={selectedAgentId === a.id}
        aria-label={a.name}
        onSelect={opts.onPick ?? (() => onAgentSelect({ slug: a.slug, crewSlug: a.crew_id ? crewSlugById.get(a.crew_id) ?? null : null }))}
        className={cn("min-h-[30px]", ghost && "opacity-60")}
      >
        <span className="relative shrink-0">
          <AgentAvatar
            seed={a.avatar_seed || a.name}
            style={a.avatar_style || a.crew?.avatar_style}
            avatarUrl={a.avatar_url}
            className="size-[18px] rounded-md"
          />
          {lead && <LeadStar className="-right-1 -top-1 size-2.5" />}
        </span>
        <span className="min-w-0 flex-1 truncate">{a.name}</span>
        {lead ? (
          <LeadMark />
        ) : temp ? (
          <StatusPill className="font-sans" tone={temp.tone === "warn" ? "warn" : "muted"} label={temp.label} icon={<Clock className="h-3 w-3" aria-hidden />} />
        ) : a.status === "RUNNING" ? (
          <span aria-label="Working" className="size-1.5 shrink-0 animate-pulse rounded-full bg-primary" />
        ) : null}
      </SidebarRow>
    )
  }

  const crewRow = (row: PanelCrewRow<CrewsPanelCrew, CrewsPanelAgent>) => {
    const { crew, status } = row
    const open = openCrewId === crew.id
    // Under a search the matching agents show without opening their crew.
    const q = search.trim().toLowerCase()
    const shown = open
      ? row.agents
      : q ? row.agents.filter((a) => [a.name, a.slug, a.role_title].some((f) => f?.toLowerCase().includes(q))) : []
    return (
      <div key={crew.id} data-crew-row={crew.slug}>
        <SidebarRow
          as="div"
          selected={selectedCrewId === crew.id && !selectedAgentId}
          aria-label={crew.name}
          aria-expanded={open}
          onSelect={() => onCrewSelect(crew.slug)}
          className="min-h-[30px]"
        >
          <CrewTile icon={crew.icon} color={crew.color} size={20} />
          <span className="min-w-0 flex-1 truncate font-medium">{crew.name}</span>
          {status.needs > 0 ? (
            <StatusPill className="font-sans" tone="warn" label={String(status.needs)} aria-label={`${status.needs} need you`} />
          ) : status.state === "working" ? (
            <span aria-label="Working" className="size-1.5 shrink-0 animate-pulse rounded-full bg-primary" />
          ) : null}
        </SidebarRow>
        {shown.map((a) => agentRow(a, { indent: true }))}
      </div>
    )
  }

  const clear = () => { setSearch(""); setState({ ...state, view: "all" }) }
  const nothing = model.shown === 0 && model.unassigned.length === 0 && (search.trim() !== "" || state.view !== "all")

  return (
    <div className="flex h-full min-h-0 flex-col overflow-hidden">
      <SidebarToolbar>
        <div data-crews-search className="min-w-0 flex-1">
          <SidebarSearch value={search} onValueChange={setSearch} placeholder="Search crews and agents…" />
        </div>
      </SidebarToolbar>

      <div className="flex-1 overflow-y-auto pb-2">
        {remote ? (
          <div className="py-1">
            {foundAgents.error || foundCrews.error ? (
              <p role="alert" className="px-3 type-nav">Search could not be loaded. <button type="button" onClick={() => { void foundAgents.refresh(); void foundCrews.refresh() }} className="text-primary">Retry</button></p>
            ) : query !== search.trim() || foundAgents.loading || foundCrews.loading ? (
              <p role="status" className="px-3 type-nav text-muted-foreground">Searching…</p>
            ) : (
              <>
                {foundCrews.items.length > 0 && (
                  <SidebarSection label="Crews" count={foundCrews.total ?? foundCrews.items.length}>
                    {foundCrews.items.map((c) => (
                      <SidebarRow key={c.id} as="div" aria-label={c.name} onSelect={() => onCrewSelect(c.slug)}>
                        <CrewTile icon={c.icon} color={c.color} size={20} />
                        <span className="min-w-0 flex-1 truncate font-medium">{c.name}</span>
                      </SidebarRow>
                    ))}
                  </SidebarSection>
                )}
                {foundAgents.items.length > 0 && (
                  <SidebarSection label="Agents" count={foundAgents.total ?? foundAgents.items.length}>
                    {foundAgents.items.map((a) => agentRow(a, { onPick: () => onAgentSelect({ slug: a.slug, crewSlug: null }) }))}
                  </SidebarSection>
                )}
                {!foundAgents.items.length && !foundCrews.items.length && (
                  <InlineEmpty icon={SearchX} className="mx-2 my-2" text="No crews or agents match" action={<button type="button" className="text-primary-hover hover:underline" onClick={clear}>Clear</button>} />
                )}
              </>
            )}
          </div>
        ) : (
          <>
            <SidebarSection label="Views">
              {CREW_VIEWS.map((v, i) => {
                const Icon = VIEW_ICON[v.id]
                return (
                  <SidebarRow
                    key={v.id}
                    as="div"
                    selected={state.view === v.id}
                    data-testid={`crews-view-${v.id}`}
                    aria-keyshortcuts={String(i + 1)}
                    className="min-h-[30px]"
                    onSelect={() => setState({ ...state, view: v.id })}
                  >
                    <Icon className="size-3.5 shrink-0 text-muted-foreground" aria-hidden />
                    <span className="min-w-0 flex-1 truncate">{v.label}</span>
                    <span className="text-micro tabular-nums text-muted-foreground-soft">{model.counts[v.id]}</span>
                  </SidebarRow>
                )
              })}
            </SidebarSection>

            <SidebarSection
              label="Crews"
              count={crewsTotal ?? crews.length}
              actions={
                <span className="flex items-center gap-1">
                  <CrSegment<CrewGrouping>
                    label="Group crews"
                    value={state.group}
                    onChange={(group) => setState({ ...state, group })}
                    options={[{ id: "status", label: "Status" }, { id: "name", label: "A–Z" }]}
                  />
                  {onNewCrew && (
                    <button
                      type="button"
                      onClick={onNewCrew}
                      aria-label="New crew"
                      title="New crew"
                      className="kit-tap grid size-6 place-items-center rounded-md text-muted-foreground hover:bg-foreground/[0.06] hover:text-foreground"
                    >
                      <Plus className="size-3.5" aria-hidden />
                    </button>
                  )}
                </span>
              }
            >
              {nothing ? (
                <InlineEmpty
                  icon={SearchX}
                  className="mx-2 my-2"
                  text="No crews or agents match"
                  action={<button type="button" className="text-primary-hover hover:underline" onClick={clear}>Clear</button>}
                />
              ) : (
                model.sections.map((s) => (
                  <div key={s.key} data-group={s.key}>
                    {s.label && (
                      <div className="flex items-center gap-1.5 px-3.5 pb-0.5 pt-2 text-micro text-muted-foreground-soft">
                        {s.key === "needs" ? <Hand className="size-3" aria-hidden /> : s.key === "working" ? <Play className="size-3" aria-hidden /> : <CircleDot className="size-3" aria-hidden />}
                        <span>{s.label}</span>
                        <span className="tabular-nums">{s.rows.length}</span>
                      </div>
                    )}
                    {s.rows.map(crewRow)}
                  </div>
                ))
              )}
            </SidebarSection>

            {model.unassigned.length > 0 && (
              <SidebarSection label="Without a crew" count={model.unassigned.length}>
                {model.unassigned.map((a) => agentRow(a))}
              </SidebarSection>
            )}

            {hasMore && onLoadMore && (
              <button
                type="button"
                onClick={onLoadMore}
                disabled={loadingMore}
                className="kit-tap mx-2 my-2 flex w-[calc(100%-1rem)] items-center justify-between rounded-md border border-dashed border-border px-2.5 py-1.5 text-left type-nav-sub text-muted-foreground hover:bg-foreground/[0.03] disabled:opacity-60"
              >
                <span>
                  {crewsTotal != null && crewsTotal > crews.length
                    ? `${crewsTotal - crews.length} more ${crewsTotal - crews.length === 1 ? "crew" : "crews"} not loaded`
                    : "More agents not loaded"}
                </span>
                <span className="text-primary-hover">{loadingMore ? "Loading…" : "Load more"}</span>
              </button>
            )}
          </>
        )}
      </div>
    </div>
  )
}
