"use client"

import { useState } from "react"
import { useRouter } from "next/navigation"
import {
  Calendar,
  Check,
  ChevronsUpDown,
  FolderKanban,
  Hash,
  Loader2,
  Play,
  Plus,
  Square,
  ThumbsDown,
  ThumbsUp,
} from "lucide-react"
import { AgentAvatar } from "@/components/ui/agent-avatar"
import { StatusIcon, statusLabel } from "@/components/features/issues/status-icon"
import { PriorityIcon, priorityLabel } from "@/components/features/issues/priority-icon"
import { LabelBadge } from "@/components/features/issues/label-badge"
import { PropertyRow } from "@/components/features/issues/property-row"
import { ISSUE_STATUSES, ALL_PRIORITIES } from "@/components/features/issues/issue-constants"
import { Button } from "@/components/ui/button"
import { Badge } from "@/components/ui/badge"
import { Input } from "@/components/ui/input"
import { Popover, PopoverContent, PopoverTrigger } from "@/components/ui/popover"
import {
  Command,
  CommandEmpty,
  CommandGroup,
  CommandInput,
  CommandItem,
  CommandList,
} from "@/components/ui/command"
import { formatDate } from "@/lib/time"
import { cn } from "@/lib/utils"
import { getCrewDotColor } from "@/lib/entities"
import type { IssueLabel, IssueRelation, Mission, Project } from "@/lib/types/mission"

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// ---------------------------------------------------------------------------
// Types
// ---------------------------------------------------------------------------

export interface IssueSidebarProps {
  issue: Mission
  agents: { id: string; name: string; slug?: string }[]
  allLabels: IssueLabel[]
  projects: Project[]
  relations: IssueRelation[]
  patchIssue: (body: Record<string, unknown>) => Promise<boolean>
  handleToggleLabel: (label: IssueLabel) => Promise<void>
  handleAction: (action: "start" | "stop" | "review", reviewAction?: "approve" | "request_changes") => Promise<void>
  actionLoading: boolean
}

// ---------------------------------------------------------------------------
// Component
// ---------------------------------------------------------------------------

function IssueSidebarBody({
  issue,
  agents,
  allLabels,
  projects,
  relations,
  patchIssue,
  handleToggleLabel,
  handleAction,
  actionLoading,
}: IssueSidebarProps) {
  const router = useRouter()

  // Popover open states
  const [statusOpen, setStatusOpen] = useState(false)
  const [priorityOpen, setPriorityOpen] = useState(false)
  const [assigneeOpen, setAssigneeOpen] = useState(false)
  const [dueDateOpen, setDueDateOpen] = useState(false)
  const [labelsOpen, setLabelsOpen] = useState(false)
  const [projectOpen, setProjectOpen] = useState(false)

  // Derived
  const canStart = (issue.status === "BACKLOG" || issue.status === "TODO") && issue.assignee_id
  const canStop = issue.status === "IN_PROGRESS"
  const canReview = issue.status === "REVIEW"
  const issueLabelsIds = new Set((issue.labels ?? []).map((l) => l.id))
  const assigneeName = issue.assignee_name ?? "Unassigned"
  const assigneeAgent = agents.find((a) => a.id === issue.assignee_id)
  const currentProject = projects.find((p) => p.id === issue.project_id)

  return (
    <div className="space-y-1">
        {/* PROPERTIES */}
        <div className="rounded-lg border border-white/[0.04] bg-background">
          <div className="flex items-center px-3 py-2">
            <span className="text-[11px] font-medium text-muted-foreground/70">Properties</span>
          </div>
          <div className="px-1 pb-1">

        {/* Status */}
        <PropertyRow label="Status">
          <Popover open={statusOpen} onOpenChange={setStatusOpen}>
            <PopoverTrigger asChild>
              <button className="flex items-center gap-1.5 text-sm hover:bg-accent rounded px-1.5 py-0.5 transition-colors -mr-1.5">
                <StatusIcon status={issue.status} className="h-3.5 w-3.5" />
                <span className="text-xs">{statusLabel[issue.status] ?? issue.status}</span>
              </button>
            </PopoverTrigger>
            <PopoverContent className="w-[200px] p-0" align="end">
              <Command>
                <CommandInput placeholder="Change status..." className="h-8 text-xs" />
                <CommandList>
                  <CommandEmpty>No status found.</CommandEmpty>
                  <CommandGroup>
                    {ISSUE_STATUSES.map((s) => (
                      <CommandItem
                        key={s}
                        onSelect={() => {
                          patchIssue({ status: s })
                          setStatusOpen(false)
                        }}
                      >
                        <StatusIcon status={s} className="mr-2 h-3.5 w-3.5" />
                        <span className="text-xs">{statusLabel[s] ?? s}</span>
                        {issue.status === s && (
                          <Check className="ml-auto h-3.5 w-3.5" />
                        )}
                      </CommandItem>
                    ))}
                  </CommandGroup>
                </CommandList>
              </Command>
            </PopoverContent>
          </Popover>
        </PropertyRow>

        {/* Priority */}
        <PropertyRow label="Priority">
          <Popover open={priorityOpen} onOpenChange={setPriorityOpen}>
            <PopoverTrigger asChild>
              <button className="flex items-center gap-1.5 text-sm hover:bg-accent rounded px-1.5 py-0.5 transition-colors -mr-1.5">
                <PriorityIcon priority={issue.priority ?? "none"} className="h-3.5 w-3.5" />
                <span className="text-xs">{priorityLabel[issue.priority ?? "none"]}</span>
              </button>
            </PopoverTrigger>
            <PopoverContent className="w-[200px] p-0" align="end">
              <Command>
                <CommandList>
                  <CommandGroup>
                    {ALL_PRIORITIES.map((p) => (
                      <CommandItem
                        key={p}
                        onSelect={() => {
                          patchIssue({ priority: p })
                          setPriorityOpen(false)
                        }}
                      >
                        <PriorityIcon priority={p} className="mr-2 h-3.5 w-3.5" />
                        <span className="text-xs">{priorityLabel[p]}</span>
                        {(issue.priority ?? "none") === p && (
                          <Check className="ml-auto h-3.5 w-3.5" />
                        )}
                      </CommandItem>
                    ))}
                  </CommandGroup>
                </CommandList>
              </Command>
            </PopoverContent>
          </Popover>
        </PropertyRow>

        {/* Assignee */}
        <PropertyRow label="Assignee">
          <Popover open={assigneeOpen} onOpenChange={setAssigneeOpen}>
            <PopoverTrigger asChild>
              <button className="flex items-center gap-1.5 text-sm hover:bg-accent rounded px-1.5 py-0.5 transition-colors -mr-1.5">
                {issue.assignee_id && assigneeAgent ? (
                  <AgentAvatar
                    seed={assigneeAgent.slug ?? assigneeAgent.name}
                    className="h-4 w-4 rounded-full"
                  />
                ) : issue.assignee_id ? (
                  <div className="h-4 w-4 rounded-full bg-primary/10 flex items-center justify-center text-[8px] font-medium text-primary">
                    {assigneeName.charAt(0).toUpperCase()}
                  </div>
                ) : null}
                <span className="text-xs">{assigneeName}</span>
              </button>
            </PopoverTrigger>
            <PopoverContent className="w-[220px] p-0" align="end">
              <Command>
                <CommandInput placeholder="Search assignee..." className="h-8 text-xs" />
                <CommandList>
                  <CommandEmpty>No results.</CommandEmpty>
                  <CommandGroup>
                    <CommandItem
                      onSelect={() => {
                        patchIssue({ assignee_type: null, assignee_id: null })
                        setAssigneeOpen(false)
                      }}
                    >
                      <span className="text-xs text-muted-foreground">Unassigned</span>
                      {!issue.assignee_id && (
                        <Check className="ml-auto h-3.5 w-3.5" />
                      )}
                    </CommandItem>
                    {agents.map((agent) => (
                      <CommandItem
                        key={agent.id}
                        onSelect={() => {
                          patchIssue({ assignee_type: "agent", assignee_id: agent.id })
                          setAssigneeOpen(false)
                        }}
                      >
                        <AgentAvatar
                          seed={agent.slug ?? agent.name}
                          className="mr-2 h-4 w-4 rounded-full"
                        />
                        <span className="text-xs">{agent.name}</span>
                        {issue.assignee_id === agent.id && (
                          <Check className="ml-auto h-3.5 w-3.5" />
                        )}
                      </CommandItem>
                    ))}
                  </CommandGroup>
                </CommandList>
              </Command>
            </PopoverContent>
          </Popover>
        </PropertyRow>

        {/* Due date */}
        <PropertyRow label="Due date">
          <Popover open={dueDateOpen} onOpenChange={setDueDateOpen}>
            <PopoverTrigger asChild>
              <button className="flex items-center gap-1.5 text-sm hover:bg-accent rounded px-1.5 py-0.5 transition-colors -mr-1.5">
                <Calendar className="h-3.5 w-3.5 text-muted-foreground/60" />
                <span className="text-xs">
                  {issue.due_date
                    ? formatDate(issue.due_date)
                    : "No due date"}
                </span>
              </button>
            </PopoverTrigger>
            <PopoverContent className="w-auto p-3" align="end">
              <div className="space-y-2">
                <Input
                  type="date"
                  defaultValue={issue.due_date?.split("T")[0] ?? ""}
                  className="h-8 text-sm"
                  onChange={(e) => {
                    patchIssue({ due_date: e.target.value || null })
                  }}
                />
                {issue.due_date && (
                  <Button
                    variant="ghost"
                    size="sm"
                    className="w-full h-7 text-xs text-muted-foreground"
                    onClick={() => {
                      patchIssue({ due_date: null })
                      setDueDateOpen(false)
                    }}
                  >
                    Clear due date
                  </Button>
                )}
              </div>
            </PopoverContent>
          </Popover>
        </PropertyRow>

        {/* Estimate */}
        <PropertyRow label="Estimate">
          <Popover>
            <PopoverTrigger asChild>
              <button className="flex items-center gap-1.5 text-sm hover:bg-accent rounded px-1.5 py-0.5 transition-colors -mr-1.5">
                <Hash className="h-3.5 w-3.5 text-muted-foreground/60" />
                <span className="text-xs">
                  {issue.estimate ? `${issue.estimate} points` : "No estimate"}
                </span>
              </button>
            </PopoverTrigger>
            <PopoverContent className="w-48 p-1" align="end">
              {[1, 2, 3, 5, 8, 13, 21].map((pts) => (
                <button
                  key={pts}
                  onClick={() => patchIssue({ estimate: pts })}
                  className={cn(
                    "w-full px-2 py-1.5 text-xs text-left rounded hover:bg-white/[0.06]",
                    issue.estimate === pts && "bg-blue-500/10 text-blue-400",
                  )}
                >
                  {pts} points
                </button>
              ))}
              <button
                onClick={() => patchIssue({ estimate: null })}
                className="w-full px-2 py-1.5 text-xs text-left rounded hover:bg-white/[0.06] text-muted-foreground/50"
              >
                Clear estimate
              </button>
            </PopoverContent>
          </Popover>
        </PropertyRow>

          </div>
        </div>

        {/* Labels */}
        <div className="rounded-lg border border-white/[0.04] bg-background">
          <div className="flex items-center justify-between px-3 py-2">
            <span className="text-[11px] font-medium text-muted-foreground/70">Labels</span>
            <Popover open={labelsOpen} onOpenChange={setLabelsOpen}>
              <PopoverTrigger asChild>
                <Button
                  variant="ghost"
                  size="icon"
                  className="h-5 w-5 text-muted-foreground/50 hover:text-foreground"
                  aria-label="Add label"
                >
                  <Plus className="h-3 w-3" />
                </Button>
              </PopoverTrigger>
              <PopoverContent className="w-[220px] p-0" align="end">
                <Command>
                  <CommandInput placeholder="Search labels..." className="h-8 text-xs" />
                  <CommandList>
                    <CommandEmpty>No labels found.</CommandEmpty>
                    <CommandGroup>
                      {allLabels.map((label) => (
                        <CommandItem
                          key={label.id}
                          onSelect={() => handleToggleLabel(label)}
                        >
                          <span
                            className="mr-2 h-2.5 w-2.5 rounded-full shrink-0"
                            style={{ backgroundColor: label.color }}
                          />
                          <span className="text-xs">{label.name}</span>
                          {issueLabelsIds.has(label.id) && (
                            <Check className="ml-auto h-3.5 w-3.5" />
                          )}
                        </CommandItem>
                      ))}
                    </CommandGroup>
                  </CommandList>
                </Command>
              </PopoverContent>
            </Popover>
          </div>
          <div className="px-3 pb-2">
            {(issue.labels ?? []).length > 0 ? (
              <div className="flex flex-wrap gap-1">
                {(issue.labels ?? []).map((label) => (
                  <LabelBadge key={label.id} label={label} />
                ))}
              </div>
            ) : (
              <span className="text-[11px] text-muted-foreground/40 pl-0.5">No labels</span>
            )}
          </div>
        </div>

        {/* Project */}
        <div className="rounded-lg border border-white/[0.04] bg-background">
          <div className="flex items-center px-3 py-2">
            <span className="text-[11px] font-medium text-muted-foreground/70">Project</span>
          </div>
          <div className="flex items-center gap-1 group px-3 pb-2">
            <Popover open={projectOpen} onOpenChange={setProjectOpen}>
              <PopoverTrigger asChild>
                <button className="flex items-center gap-1.5 text-sm hover:bg-accent rounded px-1.5 py-0.5 transition-colors flex-1">
                  <FolderKanban className="h-3.5 w-3.5 text-muted-foreground/60" />
                  <span className="text-xs truncate">
                    {currentProject ? currentProject.name : "No project"}
                  </span>
                  <ChevronsUpDown className="ml-auto h-3 w-3 text-muted-foreground/40" />
                </button>
              </PopoverTrigger>
              <PopoverContent className="w-[220px] p-0" align="end">
                <Command>
                  <CommandInput placeholder="Search projects..." className="h-8 text-xs" />
                  <CommandList>
                    <CommandEmpty>No projects found.</CommandEmpty>
                    <CommandGroup>
                      <CommandItem
                        onSelect={() => {
                          patchIssue({ project_id: null })
                          setProjectOpen(false)
                        }}
                      >
                        <span className="text-xs text-muted-foreground">No project</span>
                        {!issue.project_id && (
                          <Check className="ml-auto h-3.5 w-3.5" />
                        )}
                      </CommandItem>
                      {projects.map((project) => (
                        <CommandItem
                          key={project.id}
                          onSelect={() => {
                            patchIssue({ project_id: project.id })
                            setProjectOpen(false)
                          }}
                        >
                          <span
                            className="mr-2 h-2.5 w-2.5 rounded shrink-0"
                            style={{ backgroundColor: getCrewDotColor(project.color) }}
                          />
                          <span className="text-xs">{project.name}</span>
                          {issue.project_id === project.id && (
                            <Check className="ml-auto h-3.5 w-3.5" />
                          )}
                        </CommandItem>
                      ))}
                    </CommandGroup>
                  </CommandList>
                </Command>
              </PopoverContent>
            </Popover>
            {currentProject && (
              <a
                href={`/issues?project=${currentProject.id}`}
                className="opacity-0 group-hover:opacity-100 p-1 rounded hover:bg-accent text-muted-foreground/30 hover:text-blue-400 transition-all shrink-0"
                title="Open project"
              >
                <svg className="h-3 w-3" viewBox="0 0 16 16" fill="none" stroke="currentColor" strokeWidth="1.5" strokeLinecap="round" strokeLinejoin="round"><path d="M6 2H3a1 1 0 0 0-1 1v10a1 1 0 0 0 1 1h10a1 1 0 0 0 1-1v-3"/><path d="M10 2h4v4"/><path d="M14 2L7 9"/></svg>
              </a>
            )}
          </div>
        </div>

        {/* Relations */}
        <div className="rounded-lg border border-white/[0.04] bg-background">
          <div className="flex items-center px-3 py-2">
            <span className="text-[11px] font-medium text-muted-foreground/70">Relations</span>
          </div>
          {relations.length > 0 ? (
            <div className="space-y-1.5 px-3 pb-2">
              {relations.map((rel) => (
                <button
                  key={rel.id}
                  className="flex items-center gap-2 w-full text-left hover:bg-accent rounded px-1.5 py-1 transition-colors"
                  onClick={() => {
                    if (rel.target_identifier) {
                      router.push(`/issues/${encodeURIComponent(rel.target_identifier)}`)
                    }
                  }}
                >
                  {rel.target_status && (
                    <StatusIcon status={rel.target_status} className="h-3 w-3 shrink-0" />
                  )}
                  <span className="text-xs font-mono text-muted-foreground shrink-0">
                    {rel.target_identifier}
                  </span>
                  <span className="text-xs text-foreground/70 truncate">
                    {rel.target_title}
                  </span>
                  <Badge
                    variant="outline"
                    className="ml-auto text-[9px] px-1 py-0 border-border text-muted-foreground/60 shrink-0"
                  >
                    {rel.relation_type.replace("_", " ")}
                  </Badge>
                </button>
              ))}
            </div>
          ) : (
            <p className="text-xs text-muted-foreground/40 px-3 pb-2">No relations</p>
          )}
        </div>

        {/* Action buttons */}
        <div className="space-y-2">
          {canStart && (
            <Button
              className="w-full h-8 text-xs gap-1.5"
              onClick={() => handleAction("start")}
              disabled={actionLoading}
            >
              {actionLoading ? (
                <Loader2 className="h-3.5 w-3.5 animate-spin" />
              ) : (
                <Play className="h-3.5 w-3.5" />
              )}
              Start Issue
            </Button>
          )}
          {canStop && (
            <Button
              variant="outline"
              className="w-full h-8 text-xs gap-1.5"
              onClick={() => handleAction("stop")}
              disabled={actionLoading}
            >
              {actionLoading ? (
                <Loader2 className="h-3.5 w-3.5 animate-spin" />
              ) : (
                <Square className="h-3.5 w-3.5" />
              )}
              Stop
            </Button>
          )}
          {canReview && (
            <div className="flex gap-2">
              <Button
                className="flex-1 h-8 text-xs gap-1.5"
                onClick={() => handleAction("review", "approve")}
                disabled={actionLoading}
              >
                {actionLoading ? (
                  <Loader2 className="h-3.5 w-3.5 animate-spin" />
                ) : (
                  <ThumbsUp className="h-3.5 w-3.5" />
                )}
                Approve
              </Button>
              <Button
                variant="outline"
                className="flex-1 h-8 text-xs gap-1.5"
                onClick={() => handleAction("review", "request_changes")}
                disabled={actionLoading}
              >
                {actionLoading ? (
                  <Loader2 className="h-3.5 w-3.5 animate-spin" />
                ) : (
                  <ThumbsDown className="h-3.5 w-3.5" />
                )}
                Changes
              </Button>
            </div>
          )}
        </div>
    </div>
  )
}

// ---------------------------------------------------------------------------
// Desktop sidebar wrapper
// ---------------------------------------------------------------------------

export function IssueSidebar(props: IssueSidebarProps) {
  return (
    <div className="w-[320px] border-l border-border bg-card shrink-0 hidden lg:block">
      <div className="sticky top-0 p-3 overflow-y-auto max-h-[calc(100vh-53px)]">
        <IssueSidebarBody {...props} />
      </div>
    </div>
  )
}

// ---------------------------------------------------------------------------
// Mobile sidebar — full editor parity with the desktop sidebar, rendered in a
// mobile-friendly scrollable container below the main content on small
// screens. Reuses the exact same popover-based editor sub-tree via
// IssueSidebarBody so mobile users can edit status, priority, assignee, due
// date, estimate, labels, and project identically to desktop.
// ---------------------------------------------------------------------------

export function IssueSidebarMobile(props: IssueSidebarProps) {
  return (
    <div className="lg:hidden border-t border-border bg-card p-3 shrink-0">
      <IssueSidebarBody {...props} />
    </div>
  )
}
