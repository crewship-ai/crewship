"use client"

import { useMemo, useState } from "react"
import { ScrollArea } from "@/components/ui/scroll-area"
import { Search, X, ChevronDown, ChevronRight, Network } from "lucide-react"
import { StatusIcon } from "@/components/features/issues/status-icon"
import { PriorityIcon } from "@/components/features/issues/priority-icon"
import { HierarchyTree } from "@/components/features/orchestration/hierarchy-tree"
import { UnifiedInbox } from "@/components/features/orchestration/unified-inbox"
import { ConnectionMap } from "@/components/features/orchestration/connection-map"
import { cn } from "@/lib/utils"
import { getAgentAvatarUrl } from "@/lib/agent-avatar"
import type { Mission, MissionTask, Project } from "@/lib/types/mission"
import type { CrewSummary, AgentSummary, CrewConnection } from "@/lib/types/orchestration"

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
  agents: AgentSummary[]
  selectedCrewId: string | null
  selectedAgentSlug: string | null
  onCrewSelect: (crewId: string) => void
  onAgentSelect: (agentSlug: string) => void
  missions: Mission[]
  onTaskSelect: (task: MissionTask, mission: Mission) => void
  onApproveGate?: (taskId: string, missionId: string) => void
  connections: CrewConnection[]
  filterCrewId: string | null
  onCrewFilter: (crewId: string | null) => void
}

export function UnifiedExplorer({
  issues, projects, search, onSearchChange,
  selectedIssue, selectedProjectId, onProjectSelect, onIssueSelect,
  crews, agents, selectedCrewId, selectedAgentSlug, onCrewSelect, onAgentSelect,
  missions, onTaskSelect, onApproveGate,
  connections,
  filterCrewId, onCrewFilter,
}: UnifiedExplorerProps) {
  const [projectsOpen, setProjectsOpen] = useState(true)
  const [crewsOpen, setCrewsOpen] = useState(false)
  const [inboxOpen, setInboxOpen] = useState(true)
  const [connectionsOpen, setConnectionsOpen] = useState(false)

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

  const displayed = useMemo(() => {
    let filtered = issues
    if (selectedProjectId) filtered = filtered.filter((i) => i.project_id === selectedProjectId)
    if (filterCrewId) filtered = filtered.filter((i) => i.crew_id === filterCrewId)
    if (search) {
      const q = search.toLowerCase()
      filtered = filtered.filter((i) => i.title.toLowerCase().includes(q) || (i.identifier && i.identifier.toLowerCase().includes(q)))
    }
    return filtered
  }, [issues, search, selectedProjectId, filterCrewId])

  return (
    <ScrollArea className="h-full">
      <div className="flex flex-col">
        {/* ── Search ── */}
        <div className="px-2 py-1.5 shrink-0">
          <div className="flex items-center gap-1.5 h-7 px-2 bg-white/[0.04] border border-white/[0.08] rounded-md">
            <Search className="h-3 w-3 text-muted-foreground/50 shrink-0" />
            <input
              type="text" value={search} onChange={(e) => onSearchChange(e.target.value)}
              placeholder="Search issues..."
              className="flex-1 bg-transparent text-[11px] text-foreground placeholder:text-muted-foreground/40 outline-none"
            />
            {search && (
              <button onClick={() => onSearchChange("")} className="text-muted-foreground/50 hover:text-foreground">
                <X className="h-3 w-3" />
              </button>
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
            {projectsOpen && projects.map((p) => (
              <button
                key={p.id} onClick={() => onProjectSelect(p.id)}
                className={cn(
                  "flex items-center gap-2 w-full px-3 py-1 text-left hover:bg-white/[0.04] transition-colors",
                  selectedProjectId === p.id ? "bg-blue-500/10 border-l-2 border-blue-500" : "border-l-2 border-transparent",
                )}
              >
                <div className="w-2 h-2 rounded-sm shrink-0" style={{ backgroundColor: p.color }} />
                <span className="text-[11px] text-foreground/80 truncate flex-1">{p.name}</span>
                <span className="text-[9px] text-muted-foreground/40 tabular-nums">{p.issue_count}</span>
              </button>
            ))}
          </div>
        )}

        {/* ── Crew filter chips ── */}
        {crews.length > 0 && (
          <div className="flex flex-wrap gap-1 px-3 py-1.5 border-b border-white/[0.06]">
            <button
              onClick={() => onCrewFilter(null)}
              className={cn(
                "text-[9px] px-2 py-0.5 rounded border transition-colors",
                !filterCrewId ? "border-blue-500/30 bg-blue-500/10 text-blue-400" : "border-white/[0.06] text-muted-foreground/40 hover:text-muted-foreground/60",
              )}
            >All</button>
            {crews.map((c) => (
              <button
                key={c.id} onClick={() => onCrewFilter(filterCrewId === c.id ? null : c.id)}
                className={cn(
                  "text-[9px] px-2 py-0.5 rounded border transition-colors",
                  filterCrewId === c.id ? "border-blue-500/30 bg-blue-500/10 text-blue-400" : "border-white/[0.06] text-muted-foreground/40 hover:text-muted-foreground/60",
                )}
              >{c.name}</button>
            ))}
          </div>
        )}

        {/* ── Issues (main list) ── */}
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
                  "w-full flex items-center gap-2 px-2 py-1.5 rounded-md text-left transition-colors hover:bg-white/[0.04]",
                  isSelected ? "bg-blue-500/10 border-l-2 border-l-blue-400" : "border-l-2 border-l-transparent",
                )}
              >
                <StatusIcon status={issue.status} className="h-3.5 w-3.5 shrink-0" />
                <span className="text-[10px] font-mono text-muted-foreground/60 shrink-0 w-[48px] truncate">{issue.identifier || "--"}</span>
                <span className="text-[11px] text-foreground/80 truncate flex-1">{issue.title}</span>
                {issue.assignee_id && (
                  <img src={getAgentAvatarUrl(issue.assignee_id)} alt="" className="h-4 w-4 rounded-full shrink-0" />
                )}
                <PriorityIcon priority={issue.priority || "none"} className="h-3 w-3 shrink-0" />
              </button>
            )
          })}
          {displayed.length === 0 && (
            <div className="flex items-center justify-center py-6 text-[11px] text-muted-foreground/40">No issues found</div>
          )}
        </div>

        {/* ── Crews (tree, collapsed by default) ── */}
        <div className="border-b border-white/[0.06]">
          <button onClick={() => setCrewsOpen(!crewsOpen)} className="flex items-center gap-1 w-full px-3 py-1 hover:bg-white/[0.02]">
            {crewsOpen ? <ChevronDown className="h-2.5 w-2.5 text-muted-foreground/40" /> : <ChevronRight className="h-2.5 w-2.5 text-muted-foreground/40" />}
            <span className="text-[10px] font-semibold text-muted-foreground/60 uppercase tracking-wider flex-1 text-left">Crews</span>
            <span className="text-[9px] text-muted-foreground/30">{crews.length}</span>
          </button>
          {crewsOpen && (
            <HierarchyTree
              crews={crews} agents={agents}
              selectedCrewId={selectedCrewId} selectedAgentSlug={selectedAgentSlug}
              onCrewSelect={(crewId) => { onCrewSelect(crewId); onCrewFilter(filterCrewId === crewId ? null : crewId) }}
              onAgentSelect={onAgentSelect}
            />
          )}
        </div>

        {/* ── Inbox (bottom) ── */}
        <div className="border-b border-white/[0.06]">
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

        {/* ── Connections (collapsed) ── */}
        <div>
          <button onClick={() => setConnectionsOpen(!connectionsOpen)} className="flex items-center gap-1 w-full px-3 py-1 hover:bg-white/[0.02]">
            {connectionsOpen ? <ChevronDown className="h-2.5 w-2.5 text-muted-foreground/40" /> : <ChevronRight className="h-2.5 w-2.5 text-muted-foreground/40" />}
            <Network className="h-2.5 w-2.5 text-muted-foreground/40" />
            <span className="text-[10px] font-semibold text-muted-foreground/60 uppercase tracking-wider flex-1 text-left">Connections</span>
          </button>
          {connectionsOpen && (
            <div className="px-2 pb-2">
              <ConnectionMap crews={crews} connections={connections} />
            </div>
          )}
        </div>
      </div>
    </ScrollArea>
  )
}
