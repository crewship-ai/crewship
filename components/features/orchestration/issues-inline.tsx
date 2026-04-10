"use client"

import { useMemo } from "react"
import { ScrollArea } from "@/components/ui/scroll-area"
import { motion, AnimatePresence } from "motion/react"
import { Search, X, Send, Clock, Plus, ChevronDown, User, Link2, FolderKanban, Hash, Flag } from "lucide-react"
import { Popover, PopoverContent, PopoverTrigger } from "@/components/ui/popover"
import { Command, CommandEmpty, CommandGroup, CommandInput, CommandItem, CommandList } from "@/components/ui/command"
import { StatusIcon, statusLabel } from "@/components/features/issues/status-icon"
import { PriorityIcon, priorityLabel } from "@/components/features/issues/priority-icon"
import { LabelBadge } from "@/components/features/issues/label-badge"
import { MarkdownContent } from "@/components/features/issues/markdown-content"
import { CrewIconPopover } from "@/components/crew-icon-popover"
import { IssuesBoardView } from "@/components/features/issues/issues-board-view"
import { IssuesListView } from "@/components/features/issues/issues-list-view"
import { cn } from "@/lib/utils"
import { getAgentAvatarUrl } from "@/lib/agent-avatar"
import type { Mission, Project } from "@/lib/types/mission"

// Re-exports for backwards compatibility
export { IssueDetailInline } from "@/components/features/orchestration/issue-detail-inline"
export { ProjectDetailInline, ProjectsListView } from "@/components/features/orchestration/project-detail-inline"

/* -------------------------------------------------------------------------- */
/*  IssuesExplorerPanel — left panel                                          */
/* -------------------------------------------------------------------------- */

interface IssuesExplorerPanelProps {
  issues: Mission[]
  projects: Project[]
  search: string
  onSearchChange: (value: string) => void
  selectedIssue: Mission | null
  selectedProjectId: string | null
  onProjectSelect: (id: string) => void
  onIssueSelect: (issue: Mission) => void
}

export function IssuesExplorerPanel({
  issues,
  projects,
  search,
  onSearchChange,
  selectedIssue,
  selectedProjectId,
  onProjectSelect,
  onIssueSelect,
}: IssuesExplorerPanelProps) {
  const displayed = useMemo(() => {
    let filtered = issues
    if (selectedProjectId) {
      filtered = filtered.filter((i) => i.project_id === selectedProjectId)
    }
    if (search) {
      const q = search.toLowerCase()
      filtered = filtered.filter(
        (i) =>
          i.title.toLowerCase().includes(q) ||
          (i.identifier && i.identifier.toLowerCase().includes(q)),
      )
    }
    return filtered
  }, [issues, search, selectedProjectId])

  return (
    <div className="flex flex-col h-full">
      {/* Search */}
      <div className="px-2 py-1.5 shrink-0">
        <div className="flex items-center gap-1.5 h-7 px-2 bg-white/[0.04] border border-white/[0.08] rounded-md">
          <Search className="h-3 w-3 text-muted-foreground/50 shrink-0" />
          <input
            type="text"
            value={search}
            onChange={(e) => onSearchChange(e.target.value)}
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

      {/* Projects */}
      {projects.length > 0 && (
        <div className="border-b border-white/[0.06] pb-1">
          <div className="flex items-center justify-between px-3 py-1.5">
            <span className="text-[10px] font-semibold text-muted-foreground/60 uppercase tracking-wider">Projects</span>
          </div>
          {projects.map((p) => (
            <button
              key={p.id}
              onClick={() => onProjectSelect(p.id)}
              className={cn(
                "flex items-center gap-2 w-full px-3 py-1.5 text-left hover:bg-white/[0.04] transition-colors",
                selectedProjectId === p.id && "bg-blue-500/10 border-l-2 border-blue-500",
                selectedProjectId !== p.id && "border-l-2 border-transparent",
              )}
            >
              <div className="w-2 h-2 rounded-sm shrink-0" style={{ backgroundColor: p.color }} />
              <span className="text-[12px] text-foreground/80 truncate flex-1">{p.name}</span>
              <span className="text-[10px] text-muted-foreground/50 tabular-nums">{p.issue_count}</span>
              <div className="w-8 h-1 bg-white/[0.06] rounded-full overflow-hidden">
                <div className="h-full bg-blue-500/60 rounded-full" style={{ width: `${p.progress}%` }} />
              </div>
            </button>
          ))}
        </div>
      )}

      {/* Issue count */}
      <div className="px-3 pb-1 pt-1 shrink-0">
        <span className="text-[10px] text-muted-foreground/50">{displayed.length} issues</span>
      </div>

      {/* Issue list */}
      <ScrollArea className="flex-1">
        <div className="px-1">
          {displayed.map((issue) => {
            const isSelected = selectedIssue?.id === issue.id
            return (
              <button
                key={issue.id}
                onClick={() => onIssueSelect(issue)}
                className={cn(
                  "w-full flex items-center gap-2 px-2 py-1.5 rounded-md text-left transition-colors",
                  "hover:bg-white/[0.04]",
                  isSelected && "bg-blue-500/10 border-l-2 border-l-blue-400",
                  !isSelected && "border-l-2 border-l-transparent",
                )}
              >
                <div className="relative shrink-0">
                  <StatusIcon status={issue.status} className="h-3.5 w-3.5" />
                  {issue.status === "IN_PROGRESS" && (
                    <span className="absolute -top-0.5 -right-0.5 h-1.5 w-1.5 rounded-full bg-green-500 agent-active-dot" />
                  )}
                </div>
                <span className="text-[10px] font-mono text-muted-foreground/60 shrink-0 w-[48px] truncate">
                  {issue.identifier || "--"}
                </span>
                <span className="text-[11px] text-foreground/80 truncate flex-1">{issue.title}</span>
                {issue.assignee_id && (
                  <img
                    src={getAgentAvatarUrl(issue.assignee_id)}
                    alt=""
                    className="h-4 w-4 rounded-full shrink-0"
                  />
                )}
                <PriorityIcon priority={issue.priority || "none"} className="h-3 w-3 shrink-0" />
              </button>
            )
          })}
          {displayed.length === 0 && (
            <div className="flex items-center justify-center py-8 text-[11px] text-muted-foreground/40">
              No issues found
            </div>
          )}
        </div>
      </ScrollArea>
    </div>
  )
}

/* -------------------------------------------------------------------------- */
/*  IssuesBoardInline — center board view wrapper                             */
/* -------------------------------------------------------------------------- */

interface IssuesBoardInlineProps {
  issues: Mission[]
  onIssueClick: (issue: Mission) => void
  selectedIssueId?: string | null
}

export function IssuesBoardInline({ issues, onIssueClick, selectedIssueId }: IssuesBoardInlineProps) {
  return <IssuesBoardView issues={issues} onIssueClick={onIssueClick} selectedIssueId={selectedIssueId} />
}

/* -------------------------------------------------------------------------- */
/*  IssuesListInline — center list view wrapper                               */
/* -------------------------------------------------------------------------- */

interface IssuesListInlineProps {
  issues: Mission[]
  onIssueClick: (issue: Mission) => void
  selectedIssueId?: string | null
}

export function IssuesListInline({ issues, onIssueClick, selectedIssueId }: IssuesListInlineProps) {
  return <IssuesListView issues={issues} onIssueClick={onIssueClick} selectedIssueId={selectedIssueId} />
}
