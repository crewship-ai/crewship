"use client"

import { useMemo, useState } from "react"
import { motion } from "motion/react"
import { StatusIcon, statusLabel } from "@/components/features/issues/status-icon"
import { STATUS_CHIPS } from "@/components/features/issues/issues-status-chips"
import { PriorityIcon, priorityLabel } from "@/components/features/issues/priority-icon"
import type { IssuePriority, MissionStatus } from "@/lib/types/mission"
import { cn } from "@/lib/utils"
import { useFilteredIssues } from "@/hooks/use-filtered-issues"
import { AgentAvatar } from "@/components/ui/agent-avatar"
import { getCrewIconDef, iconColorProps } from "@/lib/entities"
import type { Mission, MissionTask, Project } from "@/lib/types/mission"
import type { CrewSummary } from "@/lib/types/orchestration"
import {
  SidebarToolbar,
  SidebarSearch,
  SidebarFilterPopover,
  SidebarFacet,
  SidebarFacetOption,
  SidebarSection,
  SidebarRow,
  SidebarCollapseButton,
} from "@/components/layout/sidebar-kit"

interface UnifiedExplorerProps {
  issues: Mission[]
  projects: Project[]
  search: string
  onSearchChange: (value: string) => void
  selectedIssue: Mission | null
  selectedProjectId: string | null
  onProjectSelect: (id: string) => void
  onIssueSelect: (issue: Mission) => void
  crews: CrewSummary[]
  missions: Mission[]
  onTaskSelect: (task: MissionTask, mission: Mission) => void
  onApproveGate?: (taskId: string, missionId: string) => void
  filterCrewId: string | null
  onCrewFilter: (crewId: string | null) => void
  filterAgentId: string | null
  onAgentFilter: (agentId: string | null) => void
  filterPriority?: IssuePriority | null
  onPriorityFilter?: (priority: IssuePriority | null) => void
  /**
   * The status multi-select, shared with `IssuesStatusChips` over the board —
   * one piece of state, two affordances. Empty = every status.
   */
  filterStatuses?: MissionStatus[]
  /** Receives the whole next selection, so toggle and clear are one prop. */
  onStatusFilter?: (statuses: MissionStatus[]) => void
  /** Collapse toggle — rendered in the toolbar next to search. */
  onToggleCollapse?: () => void
  /** The server's total; the section header says "100 of 1 015" when the page is shorter. */
  total?: number | null
}

const EMPTY_STATUSES: MissionStatus[] = []

export function UnifiedExplorer({
  issues, projects, search, onSearchChange,
  selectedIssue, selectedProjectId, onProjectSelect, onIssueSelect,
  crews,
  filterCrewId, onCrewFilter, filterAgentId, onAgentFilter,
  filterPriority = null, onPriorityFilter,
  filterStatuses = EMPTY_STATUSES, onStatusFilter,
  onToggleCollapse,
  total = null,
}: UnifiedExplorerProps) {
  const [projectsOpen, setProjectsOpen] = useState(true)
  const [filterDropdownOpen, setFilterDropdownOpen] = useState(false)

  const agents = useMemo(() => {
    const map = new Map<string, { id: string; name: string }>()
    for (const i of issues) {
      if (i.assignee_id && i.assignee_name) {
        map.set(i.assignee_id, { id: i.assignee_id, name: i.assignee_name })
      }
    }
    return Array.from(map.values()).sort((a, b) => a.name.localeCompare(b.name))
  }, [issues])

  // Active facet count for the Filter button badge. The facets AND together,
  // so this really can be 4+ — it used to be 0 or 1 because every pick wiped
  // the last one, which is a switch wearing a filter's clothes.
  const activeFilterCount =
    (filterCrewId ? 1 : 0) +
    (filterAgentId ? 1 : 0) +
    (filterPriority ? 1 : 0) +
    filterStatuses.length

  // The same hook the board and the list run on. The explorer used to
  // re-implement a subset of it — crew, agent, search, project, and neither
  // priority nor status — so a priority picked in this very dropdown changed
  // the board while the list beside it went on showing the rows it excluded.
  const { visible: displayed } = useFilteredIssues({
    issues,
    search,
    selectedProjectId,
    filterProjectId: null,
    filterCrewId,
    filterAgentId,
    filterStatuses,
    filterPriority,
  })

  // Every facet toggles on its own value and touches nothing else. Clicking
  // the active value again clears that one facet — the cheapest "undo the
  // last thing I did" there is, and the reason each section also carries an
  // explicit reset row.
  const toggleCrew = (id: string) => onCrewFilter(filterCrewId === id ? null : id)
  const toggleAgent = (id: string) => onAgentFilter(filterAgentId === id ? null : id)
  const togglePriority = (p: IssuePriority) => onPriorityFilter?.(filterPriority === p ? null : p)
  const toggleStatus = (s: MissionStatus) =>
    onStatusFilter?.(
      filterStatuses.includes(s) ? filterStatuses.filter((x) => x !== s) : [...filterStatuses, s],
    )
  const clearAll = () => {
    onCrewFilter(null)
    onAgentFilter(null)
    onPriorityFilter?.(null)
    onStatusFilter?.([])
  }

  return (
    <div className="flex flex-col h-full">
      {/* ── Search + Filter ── */}
      <SidebarToolbar>
        {/* data-issues-search wrapper keeps the `/` focus shortcut working
            (orchestration-layout targets `[data-issues-search] input`). */}
        <div data-issues-search className="flex-1 min-w-0">
          <SidebarSearch
            value={search}
            onValueChange={onSearchChange}
            placeholder="Search issues, agents…"
          />
        </div>
        {/* Filter panel — the shared one. The facets AND together and the panel
            stays open after a pick; both behaviours now live in the kit, so
            every other sidebar inherits them instead of re-deriving them. */}
        <SidebarFilterPopover
          label="Filter issues"
          activeCount={activeFilterCount}
          onClear={clearAll}
          open={filterDropdownOpen}
          onOpenChange={setFilterDropdownOpen}
        >
          {/* Status first: it is the facet a reader looks for here before any
              other, and it used to be the one facet this panel did not have.
              The selection is the same state the chip row over the board owns —
              picking Backlog here lights the Backlog chip, and the other way
              round. */}
          {onStatusFilter && (
            <SidebarFacet
              label="Status"
              resetLabel="Any status"
              resetActive={filterStatuses.length === 0}
              onReset={() => onStatusFilter([])}
              first
            >
              {STATUS_CHIPS.map((s) => (
                <SidebarFacetOption
                  key={s}
                  active={filterStatuses.includes(s)}
                  onToggle={() => toggleStatus(s)}
                >
                  <StatusIcon status={s} className="h-3.5 w-3.5 shrink-0" />
                  {statusLabel[s] ?? s}
                </SidebarFacetOption>
              ))}
            </SidebarFacet>
          )}

          <SidebarFacet
            label="Crews"
            resetLabel="All crews"
            resetActive={!filterCrewId}
            onReset={() => onCrewFilter(null)}
            first={!onStatusFilter}
          >
            {crews.map((c) => {
              const CrewIconComp = getCrewIconDef(c.icon || "users").icon
              return (
                <SidebarFacetOption
                  key={c.id}
                  active={filterCrewId === c.id}
                  onToggle={() => toggleCrew(c.id)}
                >
                  <CrewIconComp className={cn("h-3.5 w-3.5 shrink-0", iconColorProps(c.color).className)} style={iconColorProps(c.color).style} />
                  {c.name}
                </SidebarFacetOption>
              )
            })}
          </SidebarFacet>

          {agents.length > 0 && (
            <SidebarFacet
              label="Agents"
              resetLabel="All agents"
              resetActive={!filterAgentId}
              onReset={() => onAgentFilter(null)}
            >
              {agents.map((a) => (
                <SidebarFacetOption
                  key={a.id}
                  active={filterAgentId === a.id}
                  onToggle={() => toggleAgent(a.id)}
                >
                  <AgentAvatar seed={a.id} className="h-4 w-4 rounded-full shrink-0" />
                  {a.name}
                </SidebarFacetOption>
              ))}
            </SidebarFacet>
          )}

          {onPriorityFilter && (
            <SidebarFacet
              label="Priority"
              resetLabel="Any priority"
              resetActive={!filterPriority}
              onReset={() => onPriorityFilter(null)}
            >
              {(["urgent", "high", "medium", "low", "none"] as IssuePriority[]).map((p) => (
                <SidebarFacetOption
                  key={p}
                  active={filterPriority === p}
                  onToggle={() => togglePriority(p)}
                >
                  <PriorityIcon priority={p} className="h-3.5 w-3.5 shrink-0" />
                  {priorityLabel[p]}
                </SidebarFacetOption>
              ))}
            </SidebarFacet>
          )}
        </SidebarFilterPopover>
        {onToggleCollapse && <SidebarCollapseButton collapsed={false} onToggle={onToggleCollapse} />}
      </SidebarToolbar>

      {/* ── Projects ── */}
      {projects.length > 0 && (
        <SidebarSection
          label="Projects"
          count={projects.length}
          collapsible
          collapsed={!projectsOpen}
          onToggle={() => setProjectsOpen(!projectsOpen)}
          className="border-b border-white/[0.06]"
        >
          {projects.map((p) => {
            const iconDef = getCrewIconDef(p.icon || "folder")
            const IconComp = iconDef.icon
            const progress = Math.max(0, Math.min(100, p.progress || 0))
            return (
              <SidebarRow
                key={p.id}
                as="div"
                selected={selectedProjectId === p.id}
                onSelect={() => onProjectSelect(p.id)}
              >
                <IconComp className={cn("h-3.5 w-3.5 shrink-0", iconColorProps(p.color).className)} style={iconColorProps(p.color).style} />
                <span
                  className="text-foreground/80 truncate flex-1"
                  title={p.issue_count > 0 ? `${p.name} — ${progress}% complete` : p.name}
                >
                  {p.name}
                </span>
                <span className="text-[10px] text-foreground/40 tabular-nums">{p.issue_count}</span>
              </SidebarRow>
            )
          })}
        </SidebarSection>
      )}

      {/* ── Issues ── */}
      <div className="flex-1 min-h-0 flex flex-col border-b border-white/[0.06]">
        <SidebarSection
          label="Issues"
          count={
            total != null && total > issues.length ? (
              <span className="font-mono normal-case tracking-normal" title="Loaded of the workspace's total">
                {displayed.length} of {total.toLocaleString()}
              </span>
            ) : (
              displayed.length
            )
          }
        />
        <div className="flex-1 min-h-0 overflow-y-auto px-1 pb-1">
          {displayed.map((issue) => (
            <SidebarRow
              key={issue.id}
              as="div"
              selected={selectedIssue?.id === issue.id}
              onSelect={() => onIssueSelect(issue)}
            >
              <div className="relative shrink-0">
                <StatusIcon status={issue.status} className="h-3.5 w-3.5" />
                {issue.status === "IN_PROGRESS" && (
                  <span className="absolute -top-0.5 -right-0.5 h-1.5 w-1.5 rounded-full bg-success agent-active-dot" />
                )}
              </div>
              <span className="text-[10px] font-mono text-foreground/50 shrink-0 w-[44px] truncate">{issue.identifier || "--"}</span>
              <span className="text-foreground/80 truncate flex-1">{issue.title}</span>
              {issue.assignee_id && (
                <AgentAvatar seed={issue.assignee_id} alt={issue.assignee_name || ""} className="h-4 w-4 rounded-full shrink-0" />
              )}
              <PriorityIcon priority={issue.priority || "none"} className="h-3 w-3 shrink-0" />
            </SidebarRow>
          ))}
          {displayed.length === 0 && (
            <motion.div
              initial={{ opacity: 0 }} animate={{ opacity: 1 }} transition={{ delay: 0.1 }}
              className="flex items-center justify-center py-6 text-xs text-foreground/40"
            >
              No issues found
            </motion.div>
          )}
        </div>
      </div>
    </div>
  )
}
