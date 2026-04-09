"use client"

import { useMemo, useState } from "react"
import { ScrollArea } from "@/components/ui/scroll-area"
import { Search, X, ChevronDown, ChevronRight, Filter } from "lucide-react"
import { StatusIcon } from "@/components/features/issues/status-icon"
import { PriorityIcon } from "@/components/features/issues/priority-icon"
import { UnifiedInbox } from "@/components/features/orchestration/unified-inbox"
import { cn } from "@/lib/utils"
import { getAgentAvatarUrl } from "@/lib/agent-avatar"
import { getCrewIconDef } from "@/lib/crew-icon"
import type { Mission, MissionTask, Project } from "@/lib/types/mission"
import type { CrewSummary } from "@/lib/types/orchestration"

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
}

export function UnifiedExplorer({
  issues, projects, search, onSearchChange,
  selectedIssue, selectedProjectId, onProjectSelect, onIssueSelect,
  crews, missions, onTaskSelect, onApproveGate,
  filterCrewId, onCrewFilter,
}: UnifiedExplorerProps) {
  const [projectsOpen, setProjectsOpen] = useState(true)
  const [inboxOpen, setInboxOpen] = useState(true)
  const [filterDropdownOpen, setFilterDropdownOpen] = useState(false)
  const [filterAgentId, setFilterAgentId] = useState<string | null>(null)

  const inboxCount = useMemo(() => {
    let count = 0
    for (const m of missions) {
      if (!m.tasks) continue
      for (const t of m.tasks) {
        if (t.status === "FAILED" || t.status === "BLOCKED" || t.status === "AWAITING_APPROVAL") count++
      }
    }
    return count
  }, [missions])

  // Unique agents from issues for filter dropdown
  const agents = useMemo(() => {
    const map = new Map<string, { id: string; name: string }>()
    for (const i of issues) {
      if (i.assignee_id && i.assignee_name) {
        map.set(i.assignee_id, { id: i.assignee_id, name: i.assignee_name })
      }
    }
    return Array.from(map.values()).sort((a, b) => a.name.localeCompare(b.name))
  }, [issues])

  // Active filter label
  const filterLabel = useMemo(() => {
    if (filterAgentId) {
      const a = agents.find(a => a.id === filterAgentId)
      return a?.name || "Agent"
    }
    if (filterCrewId) {
      const c = crews.find(c => c.id === filterCrewId)
      return c?.name || "Crew"
    }
    return null
  }, [filterCrewId, filterAgentId, crews, agents])

  // Filter issues
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

  const clearFilters = () => {
    onCrewFilter(null)
    setFilterAgentId(null)
  }

  return (
    <ScrollArea className="h-full">
      <div className="flex flex-col">
        {/* ── Search + Filter button ── */}
        <div className="px-2 py-1.5 shrink-0 flex items-center gap-1.5">
          <div className="flex items-center gap-1.5 h-7 px-2 bg-white/[0.04] border border-white/[0.08] rounded-md flex-1 min-w-0">
            <Search className="h-3 w-3 text-muted-foreground/50 shrink-0" />
            <input
              type="text" value={search} onChange={(e) => onSearchChange(e.target.value)}
              placeholder="Search issues, agents..."
              className="flex-1 bg-transparent text-[11px] text-foreground placeholder:text-muted-foreground/40 outline-none min-w-0"
            />
            {search && (
              <button onClick={() => onSearchChange("")} className="text-muted-foreground/50 hover:text-foreground">
                <X className="h-3 w-3" />
              </button>
            )}
          </div>
          {/* Filter dropdown */}
          <div className="relative shrink-0">
            <button
              onClick={() => setFilterDropdownOpen(!filterDropdownOpen)}
              className={cn(
                "flex items-center gap-1 h-7 px-2 rounded-md border text-[10px] whitespace-nowrap transition-colors",
                filterLabel
                  ? "bg-blue-500/10 border-blue-500/30 text-blue-400"
                  : "bg-white/[0.04] border-white/[0.08] text-muted-foreground/60 hover:text-muted-foreground",
              )}
            >
              <Filter className="h-2.5 w-2.5" />
              {filterLabel || "Filter"}
              {filterLabel && (
                <button onClick={(e) => { e.stopPropagation(); clearFilters() }} className="ml-0.5 hover:text-white">
                  <X className="h-2.5 w-2.5" />
                </button>
              )}
            </button>
            {filterDropdownOpen && (
              <>
                <div className="fixed inset-0 z-40" onClick={() => setFilterDropdownOpen(false)} />
                <div className="absolute right-0 top-8 z-50 bg-card border border-white/[0.1] rounded-lg shadow-xl py-1 min-w-[180px] max-h-[320px] overflow-y-auto">
                  {/* Crews */}
                  <div className="px-3 py-1 text-[9px] font-semibold text-muted-foreground/40 uppercase tracking-wider">Crews</div>
                  <button
                    onClick={() => { onCrewFilter(null); setFilterAgentId(null); setFilterDropdownOpen(false) }}
                    className={cn("w-full text-left px-3 py-1.5 text-[11px] hover:bg-white/[0.06]", !filterCrewId && !filterAgentId ? "text-blue-400" : "text-muted-foreground/80")}
                  >All crews</button>
                  {crews.map((c) => (
                    <button
                      key={c.id}
                      onClick={() => { onCrewFilter(c.id); setFilterAgentId(null); setFilterDropdownOpen(false) }}
                      className={cn("w-full text-left px-3 py-1.5 text-[11px] hover:bg-white/[0.06] flex items-center gap-2", filterCrewId === c.id ? "text-blue-400" : "text-muted-foreground/80")}
                    >
                      <div className="w-2 h-2 rounded-sm shrink-0" style={{ backgroundColor: c.color || "#666" }} />
                      {c.name}
                    </button>
                  ))}
                  {/* Agents */}
                  {agents.length > 0 && (
                    <>
                      <div className="border-t border-white/[0.06] mt-1" />
                      <div className="px-3 py-1 text-[9px] font-semibold text-muted-foreground/40 uppercase tracking-wider">Agents</div>
                      {agents.map((a) => (
                        <button
                          key={a.id}
                          onClick={() => { setFilterAgentId(a.id); onCrewFilter(null); setFilterDropdownOpen(false) }}
                          className={cn("w-full text-left px-3 py-1.5 text-[11px] hover:bg-white/[0.06] flex items-center gap-2", filterAgentId === a.id ? "text-blue-400" : "text-muted-foreground/80")}
                        >
                          <img src={getAgentAvatarUrl(a.id)} alt="" className="h-4 w-4 rounded-full shrink-0" />
                          {a.name}
                        </button>
                      ))}
                    </>
                  )}
                </div>
              </>
            )}
          </div>
        </div>

        {/* ── Projects (compact) ── */}
        {projects.length > 0 && (
          <div className="border-b border-white/[0.06]">
            <button onClick={() => setProjectsOpen(!projectsOpen)} className="flex items-center gap-1 w-full px-3 py-1 hover:bg-white/[0.02]">
              {projectsOpen ? <ChevronDown className="h-2.5 w-2.5 text-muted-foreground/40" /> : <ChevronRight className="h-2.5 w-2.5 text-muted-foreground/40" />}
              <span className="text-[10px] font-semibold text-muted-foreground/60 uppercase tracking-wider flex-1 text-left">Projects</span>
              <span className="text-[9px] text-muted-foreground/30">{projects.length}</span>
            </button>
            {projectsOpen && projects.map((p) => {
              const iconDef = getCrewIconDef(p.icon || "folder")
              const IconComp = iconDef.icon
              return (
                <button
                  key={p.id} onClick={() => onProjectSelect(p.id)}
                  className={cn(
                    "flex items-center gap-2 w-full px-3 py-1 text-left hover:bg-white/[0.04] transition-colors",
                    selectedProjectId === p.id ? "bg-blue-500/10 border-l-2 border-blue-500" : "border-l-2 border-transparent",
                  )}
                >
                  <IconComp className="h-3 w-3 shrink-0" style={{ color: p.color }} />
                  <span className="text-[11px] text-foreground/80 truncate flex-1">{p.name}</span>
                  <span className="text-[9px] text-muted-foreground/40 tabular-nums">{p.issue_count}</span>
                </button>
              )
            })}
          </div>
        )}

        {/* ── Issues ── */}
        <div className="px-3 py-1 shrink-0 flex items-center justify-between">
          <span className="text-[10px] font-semibold text-muted-foreground/60 uppercase tracking-wider">Issues</span>
          <span className="text-[9px] text-muted-foreground/30">{displayed.length}</span>
        </div>
        <div className="px-1 border-b border-white/[0.06] pb-1">
          {displayed.map((issue) => {
            const isSelected = selectedIssue?.id === issue.id
            return (
              <button
                key={issue.id} onClick={() => onIssueSelect(issue)}
                className={cn(
                  "w-full flex items-center gap-1.5 px-2 py-1 rounded-md text-left transition-colors hover:bg-white/[0.04]",
                  isSelected ? "bg-blue-500/10 border-l-2 border-l-blue-400" : "border-l-2 border-l-transparent",
                )}
              >
                <StatusIcon status={issue.status} className="h-3 w-3 shrink-0" />
                <span className="text-[9px] font-mono text-muted-foreground/50 shrink-0 w-[40px] truncate">{issue.identifier || "--"}</span>
                <span className="text-[10px] text-foreground/80 truncate flex-1">{issue.title}</span>
                {issue.assignee_id && (
                  <img src={getAgentAvatarUrl(issue.assignee_id)} alt={issue.assignee_name || ""} title={issue.assignee_name || ""} className="h-3.5 w-3.5 rounded-full shrink-0" />
                )}
                <PriorityIcon priority={issue.priority || "none"} className="h-2.5 w-2.5 shrink-0" />
              </button>
            )
          })}
          {displayed.length === 0 && (
            <div className="flex items-center justify-center py-4 text-[10px] text-muted-foreground/40">No issues found</div>
          )}
        </div>

        {/* ── Inbox ── */}
        <div>
          <button onClick={() => setInboxOpen(!inboxOpen)} className="flex items-center gap-1 w-full px-3 py-1 hover:bg-white/[0.02]">
            {inboxOpen ? <ChevronDown className="h-2.5 w-2.5 text-muted-foreground/40" /> : <ChevronRight className="h-2.5 w-2.5 text-muted-foreground/40" />}
            <span className="text-[10px] font-semibold text-muted-foreground/60 uppercase tracking-wider flex-1 text-left">Inbox</span>
            {inboxCount > 0 && (
              <span className="text-[9px] bg-red-500 text-white rounded-full px-1.5 min-w-[16px] text-center leading-[16px]">{inboxCount}</span>
            )}
          </button>
          {inboxOpen && (
            <UnifiedInbox
              missions={missions}
              onTaskSelect={(task, mission) => {
                const relatedIssue = issues.find(i => i.id === mission.id)
                if (relatedIssue) onIssueSelect(relatedIssue)
                onTaskSelect(task, mission)
              }}
              onApproveGate={onApproveGate}
            />
          )}
        </div>
      </div>
    </ScrollArea>
  )
}
