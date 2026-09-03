"use client"

import { useCallback, useEffect, useMemo, useState } from "react"
import { AlertTriangle, ChevronRight, Clock, SearchX } from "lucide-react"
import { CrewIcon } from "@/components/ui/crew-icon"
import { cn } from "@/lib/utils"
import { AgentAvatar } from "@/components/ui/agent-avatar"
import { InlineEmpty } from "@/components/ui/inline-empty"
import { StatusPill } from "@/components/ui/status-pill"
import { isGhost, effectiveStatus } from "@/lib/agent-ephemeral"
import { SidebarToolbar, SidebarSearch, SidebarRow, SidebarCollapseButton, SidebarSection } from "@/components/layout/sidebar-kit"
import {
  explorerCountLine,
  foldRows,
  groupExplorerCrews,
  type ExplorerAgent,
  type ExplorerCrew,
  type ExplorerCrewRow,
  type ProvisioningState,
} from "./explorer-groups"

interface CrewData extends ExplorerCrew {
  _count?: { agents: number }
}

interface AgentData extends ExplorerAgent {
  avatar_seed?: string | null
  avatar_style?: string | null
  /** Stored avatar render (#1297); null means generate from the seed. */
  avatar_url?: string | null
  crew?: { avatar_style?: string | null } | null
  // PR-D F5 ephemeral lifecycle (server returns these; absent on permanent agents).
  ephemeral?: boolean
  expires_at?: string | null
}

export interface CrewsExplorerProps {
  crews: CrewData[]
  agents: AgentData[]
  selectedCrewId: string | null
  selectedAgentId: string | null
  collapsed: boolean
  onToggleCollapse: () => void
  onCrewSelect: (crewId: string) => void
  onAgentSelect: (agentId: string) => void
  /** Server totals (X-Total-Count); null on a server that does not page. */
  crewsTotal?: number | null
  agentsTotal?: number | null
  /** More rows exist past what is loaded. */
  hasMore?: boolean
  loadingMore?: boolean
  onLoadMore?: () => void
  provisioningByCrew?: ReadonlyMap<string, ProvisioningState>
}

/**
 * The left column of /crews: every crew, grouped by what needs a person
 * first (Needs attention → Running → Idle), the idle majority folded after
 * six, with the real totals under the search. The derivation is pure
 * (explorer-groups.ts); this only draws it.
 */
export function CrewsExplorer({
  crews,
  agents,
  selectedCrewId,
  selectedAgentId,
  collapsed,
  onToggleCollapse,
  onCrewSelect,
  onAgentSelect,
  crewsTotal = null,
  agentsTotal = null,
  hasMore = false,
  loadingMore = false,
  onLoadMore,
  provisioningByCrew,
}: CrewsExplorerProps) {
  const [search, setSearch] = useState("")
  const [showAllIdle, setShowAllIdle] = useState(false)

  const grouped = useMemo(
    () => groupExplorerCrews({ crews, agents, search, provisioningByCrew }),
    [crews, agents, search, provisioningByCrew],
  )

  // Open the crews that need a look and the selected one; a hundred idle
  // crews stay closed until asked. A crew that ENTERS attention opens too.
  const [expandedCrews, setExpandedCrews] = useState<Set<string>>(() => new Set())
  useEffect(() => {
    setExpandedCrews((prev) => {
      const next = new Set(prev)
      let changed = false
      for (const g of grouped.groups) {
        if (g.key !== "attention") continue
        for (const r of g.rows) if (!next.has(r.crew.id)) { next.add(r.crew.id); changed = true }
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

  const countLine = explorerCountLine({
    search,
    crewsTotal,
    agentsTotal,
    matchedCrews: grouped.matchedCrews,
    matchedAgents: grouped.matchedAgents,
  })
  const nothingMatches = search.trim() !== "" && grouped.matchedCrews === 0 && grouped.unassigned.length === 0
  const collapseToggle = <SidebarCollapseButton collapsed={collapsed} onToggle={onToggleCollapse} />

  const renderAgent = (agent: AgentData) => {
    const ghost = isGhost(agent)
    const isAgentSelected = selectedAgentId === agent.id
    // A search always shows the role line: it is what may have matched.
    const showRole = isAgentSelected || search.trim() !== ""
    return (
      <SidebarRow
        key={agent.id}
        as="div"
        selected={isAgentSelected}
        aria-label={agent.name}
        onSelect={() => onAgentSelect(agent.id)}
        className={cn(ghost && "opacity-55 grayscale-[0.4] hover:opacity-90 hover:grayscale-0")}
      >
        <AgentAvatar
          seed={agent.avatar_seed || agent.name}
          style={agent.avatar_style || agent.crew?.avatar_style}
          agentId={agent.id}
          avatarUrl={agent.avatar_url}
          className="h-8 w-8 rounded-lg shrink-0"
        />
        <div className="flex-1 min-w-0">
          <span className="type-nav font-medium truncate block">{agent.name}</span>
          {showRole && (
            <span className="type-nav-sub text-muted-foreground truncate block">
              {agent.role_title || agent.agent_role}
            </span>
          )}
        </div>
        <div className="flex items-center gap-1 shrink-0">
          {agent.ephemeral && !ghost && (
            <Clock className="h-2.5 w-2.5 text-notice/80" aria-label="Ephemeral hire" />
          )}
          <StatusPill status={effectiveStatus(agent)} live={agent.status === "RUNNING" && !ghost} />
        </div>
      </SidebarRow>
    )
  }

  const renderCrew = (row: ExplorerCrewRow) => {
    const { crew } = row
    const expanded = expandedCrews.has(crew.id)
    const isSelected = selectedCrewId === crew.id && !selectedAgentId
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
          <span className="type-nav font-semibold truncate flex-1">{crew.name}</span>
          {row.pill && <StatusPill tone={row.pill.tone} label={row.pill.label} live={row.pill.tone === "blue"} />}
          <span className="type-nav-sub text-muted-foreground-soft tabular-nums shrink-0">{row.agentCount}</span>
        </SidebarRow>

        {expanded && row.agents.length > 0 && (
          <div className="relative ml-[1.1rem] border-l border-border/70 pl-1">
            {row.agents.map((a) => renderAgent(a as AgentData))}
          </div>
        )}
      </div>
    )
  }

  return (
    <div className="flex flex-col h-full bg-card border-r border-white/[0.1] overflow-hidden">
      {collapsed ? (
        <div className="flex items-center justify-center px-2 py-2 shrink-0">
          {collapseToggle}
        </div>
      ) : (
        <div className="flex-1 min-h-0 flex flex-col">
          <SidebarToolbar>
            <SidebarSearch value={search} onValueChange={setSearch} placeholder="Search crews, agents…" />
            {collapseToggle}
          </SidebarToolbar>

          {/* The count is the server's total, not the page: "100 crews" on a
              workspace with 103 was the audit's first finding. */}
          <div className="flex items-center justify-between gap-2 px-3 pb-1 type-nav-sub text-muted-foreground" aria-live="polite">
            <span className="truncate" data-testid="explorer-count">{countLine}</span>
            {search.trim() !== "" && (
              <button type="button" className="shrink-0 text-primary-hover hover:underline kit-tap" onClick={() => setSearch("")}>
                Clear
              </button>
            )}
          </div>

          <div className="flex-1 overflow-y-auto px-1">
            {nothingMatches ? (
              <InlineEmpty
                icon={SearchX}
                className="mx-1 my-1"
                text={<>Nothing matches “{search.trim()}”. Search looks at crew and agent names, slugs and roles.</>}
                action={<button type="button" className="text-primary-hover hover:underline" onClick={() => setSearch("")}>Clear</button>}
              />
            ) : (
              grouped.groups.map((group) => {
                const { visible, hidden } = group.key === "idle" ? foldRows(group.rows, showAllIdle) : { visible: group.rows, hidden: 0 }
                return (
                  <SidebarSection
                    key={group.key}
                    label={
                      group.key === "attention" ? (
                        <span className="inline-flex items-center gap-1.5"><AlertTriangle className="h-3 w-3 text-warn" aria-hidden />{group.label}</span>
                      ) : group.label
                    }
                    count={group.rows.length}
                    className="mt-1"
                  >
                    {visible.map(renderCrew)}
                    {hidden > 0 && (
                      <button
                        type="button"
                        onClick={() => setShowAllIdle(true)}
                        className="kit-tap mx-1 my-1 flex w-[calc(100%-0.5rem)] items-center justify-between rounded-md border border-border/60 px-2.5 py-1.5 text-left type-nav-sub hover:bg-foreground/[0.03]"
                      >
                        <span className="text-muted-foreground"><span className="font-medium text-foreground/90">{hidden} more {hidden === 1 ? "crew" : "crews"}</span> · idle</span>
                        <span className="inline-flex items-center gap-1 text-primary-hover">Show all <ChevronRight className="h-3 w-3" /></span>
                      </button>
                    )}
                  </SidebarSection>
                )
              })
            )}

            {grouped.unassigned.length > 0 && (
              <SidebarSection label="Unassigned" count={grouped.unassigned.length} className="mt-2 border-t border-border pt-1">
                {grouped.unassigned.map((a) => renderAgent(a as AgentData))}
              </SidebarSection>
            )}

            {hasMore && onLoadMore && (
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
          </div>
        </div>
      )}
    </div>
  )
}
