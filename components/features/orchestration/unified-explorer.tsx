"use client"

import { useMemo, useState } from "react"
import { motion, AnimatePresence } from "motion/react"
import { StatusIcon } from "@/components/features/issues/status-icon"
import { PriorityIcon, priorityLabel } from "@/components/features/issues/priority-icon"
import type { IssuePriority } from "@/lib/types/mission"
import { cn } from "@/lib/utils"
import { AgentAvatar } from "@/components/ui/agent-avatar"
import { getCrewIconDef, getGradientPalette } from "@/lib/entities"
import type { Mission, MissionTask, Project } from "@/lib/types/mission"
import type { CrewSummary } from "@/lib/types/orchestration"
import {
  SidebarToolbar,
  SidebarSearch,
  SidebarFilterButton,
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
  /** Collapse toggle — rendered in the toolbar next to search. */
  onToggleCollapse?: () => void
}

const dropdownAnim = {
  initial: { opacity: 0, scale: 0.95, y: -4 },
  animate: { opacity: 1, scale: 1, y: 0, transition: { duration: 0.12 } },
  exit: { opacity: 0, scale: 0.95, y: -4, transition: { duration: 0.1 } },
}

export function UnifiedExplorer({
  issues, projects, search, onSearchChange,
  selectedIssue, selectedProjectId, onProjectSelect, onIssueSelect,
  crews,
  filterCrewId, onCrewFilter, filterAgentId, onAgentFilter,
  filterPriority = null, onPriorityFilter,
  onToggleCollapse,
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

  // Active facet count for the Filter button badge. Crew / agent / priority
  // are mutually exclusive in the dropdown (picking one clears the others),
  // so this is 0 or 1 in practice — but counting keeps it correct if that
  // ever changes.
  const activeFilterCount =
    (filterCrewId ? 1 : 0) + (filterAgentId ? 1 : 0) + (filterPriority ? 1 : 0)

  const displayed = useMemo(() => {
    let filtered = issues
    if (selectedProjectId) filtered = filtered.filter((i) => i.project_id === selectedProjectId)
    if (filterCrewId) filtered = filtered.filter((i) => i.crew_id === filterCrewId)
    if (filterAgentId) filtered = filtered.filter((i) => i.assignee_id === filterAgentId)
    if (search) {
      const q = search.toLowerCase()
      filtered = filtered.filter((i) =>
        i.title.toLowerCase().includes(q) ||
        (i.identifier && i.identifier.toLowerCase().includes(q)) ||
        (i.assignee_name && i.assignee_name.toLowerCase().includes(q)) ||
        (i.crew_name && i.crew_name.toLowerCase().includes(q))
      )
    }
    return filtered
  }, [issues, search, selectedProjectId, filterCrewId, filterAgentId])

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
        {/* Filter dropdown — SidebarFilterButton is the trigger; the popover
            body is kept as-is. */}
        <div className="relative shrink-0">
          <SidebarFilterButton
            activeCount={activeFilterCount}
            onClick={() => setFilterDropdownOpen(!filterDropdownOpen)}
          />
          <AnimatePresence>
            {filterDropdownOpen && (
              <>
                <div className="fixed inset-0 z-40" onClick={() => setFilterDropdownOpen(false)} />
                <motion.div
                  {...dropdownAnim}
                  className="absolute right-0 top-9 z-50 bg-card border border-white/[0.1] rounded-lg shadow-xl py-1 min-w-[180px] max-h-[320px] overflow-y-auto"
                >
                  <div className="px-3 py-1 text-[9px] font-semibold text-foreground/40 uppercase tracking-wider">Crews</div>
                  <button
                    onClick={() => { onCrewFilter(null); onAgentFilter(null); setFilterDropdownOpen(false) }}
                    className={cn("w-full text-left px-3 py-1.5 text-xs hover:bg-white/[0.06]", !filterCrewId && !filterAgentId ? "text-primary" : "text-muted-foreground/80")}
                  >All crews</button>
                  {crews.map((c) => {
                    const crewIcon = getCrewIconDef(c.icon || "users")
                    const CrewIconComp = crewIcon.icon
                    return (
                      <button
                        key={c.id}
                        onClick={() => { onCrewFilter(c.id); onAgentFilter(null); setFilterDropdownOpen(false) }}
                        className={cn("w-full text-left px-3 py-1.5 text-xs hover:bg-white/[0.06] flex items-center gap-2", filterCrewId === c.id ? "text-primary" : "text-muted-foreground/80")}
                      >
                        <CrewIconComp className={cn("h-3.5 w-3.5 shrink-0", getGradientPalette(c.color).text)} />
                        {c.name}
                      </button>
                    )
                  })}
                  {agents.length > 0 && (
                    <>
                      <div className="border-t border-white/[0.06] mt-1" />
                      <div className="px-3 py-1 text-[9px] font-semibold text-foreground/40 uppercase tracking-wider">Agents</div>
                      {agents.map((a) => (
                        <button
                          key={a.id}
                          onClick={() => { onAgentFilter(a.id); onCrewFilter(null); onPriorityFilter?.(null); setFilterDropdownOpen(false) }}
                          className={cn("w-full text-left px-3 py-1.5 text-xs hover:bg-white/[0.06] flex items-center gap-2", filterAgentId === a.id ? "text-primary" : "text-muted-foreground/80")}
                        >
                          <AgentAvatar seed={a.id} className="h-4 w-4 rounded-full shrink-0" />
                          {a.name}
                        </button>
                      ))}
                    </>
                  )}
                  {onPriorityFilter && (
                    <>
                      <div className="border-t border-white/[0.06] mt-1" />
                      <div className="px-3 py-1 text-[9px] font-semibold text-foreground/40 uppercase tracking-wider">Priority</div>
                      {(["urgent", "high", "medium", "low", "none"] as IssuePriority[]).map((p) => (
                        <button
                          key={p}
                          onClick={() => { onPriorityFilter(p); onCrewFilter(null); onAgentFilter(null); setFilterDropdownOpen(false) }}
                          className={cn("w-full text-left px-3 py-1.5 text-xs hover:bg-white/[0.06] flex items-center gap-2", filterPriority === p ? "text-primary" : "text-muted-foreground/80")}
                        >
                          <PriorityIcon priority={p} className="h-3.5 w-3.5 shrink-0" />
                          {priorityLabel[p]}
                        </button>
                      ))}
                    </>
                  )}
                </motion.div>
              </>
            )}
          </AnimatePresence>
        </div>
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
                <IconComp className={cn("h-3.5 w-3.5 shrink-0", getGradientPalette(p.color).text)} />
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
        <SidebarSection label="Issues" count={displayed.length} />
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
                  <span className="absolute -top-0.5 -right-0.5 h-1.5 w-1.5 rounded-full bg-green-500 agent-active-dot" />
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
