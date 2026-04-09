"use client"

import { useCallback, useEffect, useMemo, useState } from "react"
import { ScrollArea } from "@/components/ui/scroll-area"
import { Search, X, Send, Clock, Plus, ChevronDown, ChevronRight, User, Link2, FolderKanban } from "lucide-react"
import { Popover, PopoverContent, PopoverTrigger } from "@/components/ui/popover"
import { Command, CommandEmpty, CommandGroup, CommandInput, CommandItem, CommandList } from "@/components/ui/command"
import { StatusIcon, statusLabel } from "@/components/features/issues/status-icon"
import { PriorityIcon, priorityLabel } from "@/components/features/issues/priority-icon"
import { LabelBadge } from "@/components/features/issues/label-badge"
import { IssuesBoardView } from "@/components/features/issues/issues-board-view"
import { IssuesListView } from "@/components/features/issues/issues-list-view"
import { cn } from "@/lib/utils"
import { toast } from "sonner"
import { getAgentAvatarUrl } from "@/lib/agent-avatar"
import type { Mission, MissionStatus, IssueLabel, IssueComment, IssuePriority, IssueRelation, RelationType, Project, ProjectStatus, IssueActivity } from "@/lib/types/mission"

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
              {/* Mini progress bar */}
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
                <StatusIcon status={issue.status} className="h-3.5 w-3.5 shrink-0" />
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
}

export function IssuesBoardInline({ issues, onIssueClick }: IssuesBoardInlineProps) {
  return <IssuesBoardView issues={issues} onIssueClick={onIssueClick} />
}

/* -------------------------------------------------------------------------- */
/*  IssuesListInline — center list view wrapper                               */
/* -------------------------------------------------------------------------- */

interface IssuesListInlineProps {
  issues: Mission[]
  onIssueClick: (issue: Mission) => void
}

export function IssuesListInline({ issues, onIssueClick }: IssuesListInlineProps) {
  return <IssuesListView issues={issues} onIssueClick={onIssueClick} />
}

/* -------------------------------------------------------------------------- */
/*  IssueDetailInline — right panel (Linear-style)                            */
/* -------------------------------------------------------------------------- */

const ISSUE_STATUSES: MissionStatus[] = [
  "BACKLOG", "TODO", "IN_PROGRESS", "REVIEW", "DONE", "CANCELLED",
]

const ALL_PRIORITIES: IssuePriority[] = ["urgent", "high", "medium", "low", "none"]

const RELATION_TYPE_LABELS: Record<RelationType, string> = {
  blocks: "Blocks",
  blocked_by: "Blocked by",
  relates_to: "Related",
  duplicate_of: "Duplicate of",
}

const RELATION_TYPE_OPTIONS: { value: RelationType; label: string }[] = [
  { value: "relates_to", label: "Related to" },
  { value: "blocks", label: "Blocks" },
  { value: "blocked_by", label: "Blocked by" },
  { value: "duplicate_of", label: "Duplicate of" },
]

function formatRelativeTime(dateStr: string): string {
  const now = Date.now()
  const date = new Date(dateStr).getTime()
  const diffMs = now - date
  const diffMin = Math.floor(diffMs / 60000)
  if (diffMin < 1) return "just now"
  if (diffMin < 60) return `${diffMin}m ago`
  const diffHours = Math.floor(diffMin / 60)
  if (diffHours < 24) return `${diffHours}h ago`
  const diffDays = Math.floor(diffHours / 24)
  if (diffDays < 30) return `${diffDays}d ago`
  return new Date(dateStr).toLocaleDateString()
}

function actionLabel(action: string): string {
  switch (action) {
    case "task_completed": return "completed a task"
    case "task_failed": return "task failed"
    case "status_changed": return "changed status"
    case "assignee_changed": return "changed assignee"
    case "priority_changed": return "changed priority"
    case "review_approved": return "approved"
    case "review_changes_requested": return "requested changes"
    case "issue_started": return "started the issue"
    case "issue_stopped": return "stopped the issue"
    default: return action.replace(/_/g, " ")
  }
}

function timeAgo(dateStr: string): string {
  return formatRelativeTime(dateStr)
}

/* -- Collapsible section header ------------------------------------------- */

function SectionHeader({
  title,
  open,
  onToggle,
  action,
}: {
  title: string
  open: boolean
  onToggle: () => void
  action?: React.ReactNode
}) {
  return (
    <div className="flex items-center justify-between px-3 py-1.5">
      <button
        onClick={onToggle}
        className="flex items-center gap-1 text-[11px] uppercase tracking-wider text-muted-foreground/60 hover:text-muted-foreground/80 transition-colors"
      >
        {open ? (
          <ChevronDown className="h-3 w-3" />
        ) : (
          <ChevronRight className="h-3 w-3" />
        )}
        {title}
      </button>
      {action}
    </div>
  )
}

/* -- Clickable property row ----------------------------------------------- */

function PropertyRow({
  children,
  className,
}: {
  children: React.ReactNode
  className?: string
}) {
  return (
    <div
      className={cn(
        "flex items-center gap-2 px-3 py-1.5 rounded-sm hover:bg-white/[0.04] transition-colors cursor-pointer",
        className,
      )}
    >
      {children}
    </div>
  )
}

interface IssueDetailInlineProps {
  issue: Mission
  comments: IssueComment[]
  labels: IssueLabel[]
  projects: Project[]
  workspaceId: string
  onClose: () => void
  onUpdated: () => void
}

export function IssueDetailInline({
  issue,
  comments,
  labels: workspaceLabels,
  projects,
  workspaceId,
  onClose,
  onUpdated,
}: IssueDetailInlineProps) {
  const [newComment, setNewComment] = useState("")
  const [submitting, setSubmitting] = useState(false)
  const [editingTitle, setEditingTitle] = useState(false)
  const [titleDraft, setTitleDraft] = useState(issue.title)

  // Activities
  const [activities, setActivities] = useState<IssueActivity[]>([])

  // Review state
  const [reviewChangesOpen, setReviewChangesOpen] = useState(false)
  const [reviewComment, setReviewComment] = useState("")

  // Section collapse state
  const [propertiesOpen, setPropertiesOpen] = useState(true)
  const [labelsOpen, setLabelsOpen] = useState(true)
  const [projectOpen, setProjectOpen] = useState(true)
  const [relationsOpen, setRelationsOpen] = useState(true)

  // Popover open state
  const [statusOpen, setStatusOpen] = useState(false)
  const [priorityOpen, setPriorityOpen] = useState(false)
  const [labelsPopoverOpen, setLabelsPopoverOpen] = useState(false)
  const [projectPopoverOpen, setProjectPopoverOpen] = useState(false)
  const [assigneeOpen, setAssigneeOpen] = useState(false)
  const [crewAgents, setCrewAgents] = useState<{id: string, name: string, slug: string, crew_slug?: string}[]>([])

  // Fetch all agents for assignee picker
  useEffect(() => {
    if (!workspaceId) return
    fetch(`/api/v1/agents?workspace_id=${workspaceId}`)
      .then(r => r.ok ? r.json() : [])
      .then((agents: Array<{id: string, name: string, slug: string, crew?: {slug: string}}>) =>
        setCrewAgents(agents.map(a => ({ id: a.id, name: a.name, slug: a.slug, crew_slug: a.crew?.slug })))
      )
      .catch(() => {})
  }, [workspaceId])

  const matchingProject = projects.find((p) => p.id === issue.project_id)

  // Relations
  const [relations, setRelations] = useState<IssueRelation[]>([])
  const [relationsLoading, setRelationsLoading] = useState(false)
  const [addRelationOpen, setAddRelationOpen] = useState(false)
  const [newRelationTarget, setNewRelationTarget] = useState("")
  const [newRelationType, setNewRelationType] = useState<RelationType>("relates_to")
  const [addingRelation, setAddingRelation] = useState(false)

  // Fetch relations
  const fetchRelations = useCallback(async () => {
    if (!issue.crew_id || !issue.identifier) return
    setRelationsLoading(true)
    try {
      const res = await fetch(
        `/api/v1/crews/${issue.crew_id}/issues/${issue.identifier}/relations?workspace_id=${workspaceId}`,
      )
      if (res.ok) {
        const data = await res.json()
        setRelations(Array.isArray(data) ? data : data.relations ?? [])
      }
    } catch {
      // silent — relations are supplementary
    } finally {
      setRelationsLoading(false)
    }
  }, [issue.crew_id, issue.identifier, workspaceId])

  useEffect(() => {
    fetchRelations()
  }, [fetchRelations])

  // Fetch activities
  useEffect(() => {
    if (!issue.crew_id || !issue.identifier) return
    fetch(`/api/v1/crews/${issue.crew_id}/issues/${issue.identifier}/activity?workspace_id=${workspaceId}`)
      .then(r => r.ok ? r.json() : [])
      .then(setActivities)
      .catch(() => {})
  }, [issue.crew_id, issue.identifier, workspaceId, issue.updated_at])

  useEffect(() => {
    if (!issue.crew_id || !workspaceId) return
    fetch(`/api/v1/agents?workspace_id=${workspaceId}&crew_id=${issue.crew_id}`)
      .then(r => r.ok ? r.json() : [])
      .then(agents => setCrewAgents(agents.map((a: any) => ({ id: a.id, name: a.name, slug: a.slug }))))
      .catch(() => {})
  }, [issue.crew_id, workspaceId])

  const patchIssue = useCallback(
    async (patch: Record<string, unknown>) => {
      if (!issue.crew_id || !issue.identifier) return
      try {
        const res = await fetch(
          `/api/v1/crews/${issue.crew_id}/issues/${issue.identifier}?workspace_id=${workspaceId}`,
          {
            method: "PATCH",
            headers: { "Content-Type": "application/json" },
            body: JSON.stringify(patch),
          },
        )
        if (!res.ok) {
          const b = await res.json().catch(() => null)
          toast.error(b?.detail ?? "Failed to update issue")
          return
        }
        toast.success("Updated")
        onUpdated()
      } catch {
        toast.error("Failed to update issue")
      }
    },
    [issue.crew_id, issue.identifier, workspaceId, onUpdated],
  )

  const handleTitleSave = useCallback(async () => {
    setEditingTitle(false)
    if (titleDraft.trim() && titleDraft !== issue.title) {
      await patchIssue({ title: titleDraft.trim() })
    }
  }, [titleDraft, issue.title, patchIssue])

  const handleCommentSubmit = useCallback(async () => {
    if (!newComment.trim() || !issue.crew_id || !issue.identifier) return
    setSubmitting(true)
    try {
      const res = await fetch(
        `/api/v1/crews/${issue.crew_id}/issues/${issue.identifier}/comments?workspace_id=${workspaceId}`,
        {
          method: "POST",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify({ body: newComment.trim() }),
        },
      )
      if (!res.ok) {
        toast.error("Failed to add comment")
        return
      }
      setNewComment("")
      onUpdated()
    } catch {
      toast.error("Failed to add comment")
    } finally {
      setSubmitting(false)
    }
  }, [newComment, issue.crew_id, issue.identifier, workspaceId, onUpdated])

  const handleAddRelation = useCallback(async () => {
    if (!newRelationTarget.trim() || !issue.crew_id || !issue.identifier) return
    setAddingRelation(true)
    try {
      const res = await fetch(
        `/api/v1/crews/${issue.crew_id}/issues/${issue.identifier}/relations?workspace_id=${workspaceId}`,
        {
          method: "POST",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify({
            target_identifier: newRelationTarget.trim(),
            relation_type: newRelationType,
          }),
        },
      )
      if (!res.ok) {
        const b = await res.json().catch(() => null)
        toast.error(b?.detail ?? "Failed to add relation")
        return
      }
      toast.success("Relation added")
      setNewRelationTarget("")
      setAddRelationOpen(false)
      fetchRelations()
    } catch {
      toast.error("Failed to add relation")
    } finally {
      setAddingRelation(false)
    }
  }, [newRelationTarget, newRelationType, issue.crew_id, issue.identifier, workspaceId, fetchRelations])

  const handleDeleteRelation = useCallback(async (relationId: string) => {
    try {
      const res = await fetch(
        `/api/v1/relations/${relationId}?workspace_id=${workspaceId}`,
        { method: "DELETE" },
      )
      if (!res.ok) {
        toast.error("Failed to remove relation")
        return
      }
      fetchRelations()
    } catch {
      toast.error("Failed to remove relation")
    }
  }, [workspaceId, fetchRelations])

  // Group relations by type
  const relationsByType = relations.reduce<Record<string, IssueRelation[]>>((acc, rel) => {
    const key = rel.relation_type
    if (!acc[key]) acc[key] = []
    acc[key].push(rel)
    return acc
  }, {})

  // Assigned labels on this issue
  const issueLabels = issue.labels ?? []

  return (
    <div className="flex flex-col h-full bg-card">
      {/* ── Header: identifier badge + close ─────────────────────────────── */}
      <div className="flex items-center gap-2 px-3 py-2 border-b border-white/[0.06] shrink-0">
        <span className="text-[11px] font-mono text-muted-foreground/70 bg-white/[0.06] px-1.5 py-0.5 rounded">
          {issue.identifier || "--"}
        </span>
        <div className="flex-1" />
        {issue.identifier && (
          <a
            href={`/orchestration/issues/${issue.identifier}`}
            className="text-muted-foreground/40 hover:text-foreground p-1 rounded hover:bg-white/[0.06] transition-colors"
            title="Open full page"
          >
            <svg className="h-3.5 w-3.5" viewBox="0 0 16 16" fill="none" stroke="currentColor" strokeWidth="1.5" strokeLinecap="round" strokeLinejoin="round">
              <path d="M6 2H3a1 1 0 0 0-1 1v10a1 1 0 0 0 1 1h10a1 1 0 0 0 1-1v-3" />
              <path d="M10 2h4v4" />
              <path d="M14 2L7 9" />
            </svg>
          </a>
        )}
        <button
          onClick={onClose}
          className="text-muted-foreground/50 hover:text-foreground p-1 rounded hover:bg-white/[0.06] transition-colors"
        >
          <X className="h-3.5 w-3.5" />
        </button>
      </div>

      <div className="flex-1 overflow-y-auto min-h-0">
        <div className="py-3">
          {/* ── Title + Description ──────────────────────────────────────── */}
          <div className="px-3">
            {editingTitle ? (
              <input
                autoFocus
                value={titleDraft}
                onChange={(e) => setTitleDraft(e.target.value)}
                onBlur={handleTitleSave}
                onKeyDown={(e) => {
                  if (e.key === "Enter") handleTitleSave()
                  if (e.key === "Escape") {
                    setEditingTitle(false)
                    setTitleDraft(issue.title)
                  }
                }}
                className="w-full text-[15px] font-semibold text-foreground bg-white/[0.04] border border-white/[0.1] rounded px-2 py-1 outline-none focus:border-blue-400/50"
              />
            ) : (
              <h3
                onClick={() => {
                  setEditingTitle(true)
                  setTitleDraft(issue.title)
                }}
                className="text-[15px] font-semibold text-foreground cursor-pointer hover:text-blue-400 transition-colors leading-snug"
              >
                {issue.title}
              </h3>
            )}

            {issue.description && (
              <p className="mt-1.5 text-[12px] text-muted-foreground/60 leading-relaxed">
                {issue.description}
              </p>
            )}
          </div>

          {/* ── Start / Stop actions ──────────────────────────────────────── */}
          {(issue.status === "BACKLOG" || issue.status === "TODO") && issue.assignee_id && (
            <div className="mt-3 px-0">
              <button
                onClick={async () => {
                  const qs = `?workspace_id=${encodeURIComponent(workspaceId)}`
                  const res = await fetch(`/api/v1/crews/${issue.crew_id}/issues/${issue.identifier}/start${qs}`, { method: "POST" })
                  if (res.ok) { toast.success("Issue started — agent dispatched"); onUpdated() }
                  else { const e = await res.json().catch(() => null); toast.error(e?.detail || "Failed to start") }
                }}
                className="w-full flex items-center justify-center gap-2 h-8 rounded-md bg-blue-600 hover:bg-blue-500 text-white text-xs font-medium transition-colors"
              >
                <svg className="h-3 w-3" viewBox="0 0 16 16" fill="currentColor"><path d="M4 2.5v11l9-5.5z"/></svg>
                Start
              </button>
            </div>
          )}
          {issue.status === "IN_PROGRESS" && (
            <div className="mt-3 px-0">
              <button
                onClick={async () => {
                  const qs = `?workspace_id=${encodeURIComponent(workspaceId)}`
                  const res = await fetch(`/api/v1/crews/${issue.crew_id}/issues/${issue.identifier}/stop${qs}`, { method: "POST" })
                  if (res.ok) { toast.success("Issue stopped"); onUpdated() }
                  else { const e = await res.json().catch(() => null); toast.error(e?.detail || "Failed to stop") }
                }}
                className="w-full flex items-center justify-center gap-2 h-8 rounded-md bg-red-500/10 border border-red-500/30 text-red-400 text-xs font-medium hover:bg-red-500/20 transition-colors"
              >
                <svg className="h-3 w-3" viewBox="0 0 16 16" fill="currentColor"><rect x="3" y="3" width="10" height="10" rx="1"/></svg>
                Stop
              </button>
            </div>
          )}
          {issue.status === "REVIEW" && (
            <div className="mt-3 space-y-2">
              <button
                onClick={async () => {
                  const qs = `?workspace_id=${encodeURIComponent(workspaceId)}`
                  const res = await fetch(`/api/v1/crews/${issue.crew_id}/issues/${issue.identifier}/review${qs}`, {
                    method: "POST",
                    headers: { "Content-Type": "application/json" },
                    body: JSON.stringify({ action: "approve" }),
                  })
                  if (res.ok) { toast.success("Issue approved"); onUpdated() }
                  else { const e = await res.json().catch(() => null); toast.error(e?.detail || "Failed") }
                }}
                className="w-full flex items-center justify-center gap-2 h-8 rounded-md bg-green-600 hover:bg-green-500 text-white text-xs font-medium transition-colors"
              >
                <svg className="h-3 w-3" viewBox="0 0 16 16" fill="none" stroke="currentColor" strokeWidth="2"><path d="M3 8.5l3.5 3.5 6.5-8" strokeLinecap="round" strokeLinejoin="round"/></svg>
                Approve
              </button>
              <button
                onClick={() => setReviewChangesOpen(true)}
                className="w-full flex items-center justify-center gap-2 h-8 rounded-md bg-amber-500/10 border border-amber-500/30 text-amber-400 text-xs font-medium hover:bg-amber-500/20 transition-colors"
              >
                Request Changes
              </button>
              {reviewChangesOpen && (
                <div className="border border-white/[0.06] rounded-md p-3 space-y-2">
                  <textarea
                    className="w-full h-16 bg-transparent border border-white/[0.08] rounded px-2 py-1.5 text-xs text-foreground outline-none resize-none"
                    placeholder="What needs to change..."
                    value={reviewComment}
                    onChange={(e) => setReviewComment(e.target.value)}
                  />
                  <div className="flex gap-2">
                    <button
                      onClick={() => { setReviewChangesOpen(false); setReviewComment("") }}
                      className="flex-1 h-7 rounded text-xs text-muted-foreground hover:text-foreground border border-white/[0.06]"
                    >
                      Cancel
                    </button>
                    <button
                      onClick={async () => {
                        const qs = `?workspace_id=${encodeURIComponent(workspaceId)}`
                        const res = await fetch(`/api/v1/crews/${issue.crew_id}/issues/${issue.identifier}/review${qs}`, {
                          method: "POST",
                          headers: { "Content-Type": "application/json" },
                          body: JSON.stringify({ action: "request_changes", comment: reviewComment }),
                        })
                        if (res.ok) { toast.success("Changes requested"); setReviewChangesOpen(false); setReviewComment(""); onUpdated() }
                        else { const e = await res.json().catch(() => null); toast.error(e?.detail || "Failed") }
                      }}
                      className="flex-1 h-7 rounded text-xs bg-amber-600 text-white hover:bg-amber-500"
                    >
                      Send
                    </button>
                  </div>
                </div>
              )}
            </div>
          )}

          {/* ── Properties section ───────────────────────────────────────── */}
          <div className="mt-3 border-t border-white/[0.06]">
            <SectionHeader
              title="Properties"
              open={propertiesOpen}
              onToggle={() => setPropertiesOpen((v) => !v)}
            />
            {propertiesOpen && (
              <div>
                {/* Status */}
                <Popover open={statusOpen} onOpenChange={setStatusOpen}>
                  <PopoverTrigger asChild>
                    <div>
                      <PropertyRow>
                        <StatusIcon status={issue.status} className="h-3.5 w-3.5" />
                        <span className="text-[12px] text-foreground/80">
                          {statusLabel[issue.status] || issue.status}
                        </span>
                      </PropertyRow>
                    </div>
                  </PopoverTrigger>
                  <PopoverContent className="w-[200px] p-0" align="start" sideOffset={4}>
                    <Command>
                      <CommandInput placeholder="Set status..." className="text-xs h-8" />
                      <CommandList>
                        <CommandEmpty>No status found.</CommandEmpty>
                        <CommandGroup>
                          {ISSUE_STATUSES.map((s) => (
                            <CommandItem
                              key={s}
                              value={s}
                              onSelect={() => {
                                patchIssue({ status: s })
                                setStatusOpen(false)
                              }}
                              className="flex items-center gap-2 text-xs"
                            >
                              <StatusIcon status={s} className="h-3.5 w-3.5" />
                              <span>{statusLabel[s] || s}</span>
                              {s === issue.status && (
                                <span className="ml-auto text-blue-400 text-[10px]">current</span>
                              )}
                            </CommandItem>
                          ))}
                        </CommandGroup>
                      </CommandList>
                    </Command>
                  </PopoverContent>
                </Popover>

                {/* Priority */}
                <Popover open={priorityOpen} onOpenChange={setPriorityOpen}>
                  <PopoverTrigger asChild>
                    <div>
                      <PropertyRow>
                        <PriorityIcon
                          priority={(issue.priority || "none") as IssuePriority}
                          className="h-3.5 w-3.5"
                        />
                        <span className="text-[12px] text-foreground/80">
                          {priorityLabel[(issue.priority || "none") as IssuePriority]}
                        </span>
                      </PropertyRow>
                    </div>
                  </PopoverTrigger>
                  <PopoverContent className="w-[200px] p-0" align="start" sideOffset={4}>
                    <Command>
                      <CommandList>
                        <CommandGroup>
                          {ALL_PRIORITIES.map((p) => (
                            <CommandItem
                              key={p}
                              value={p}
                              onSelect={() => {
                                patchIssue({ priority: p })
                                setPriorityOpen(false)
                              }}
                              className="flex items-center gap-2 text-xs"
                            >
                              <PriorityIcon priority={p} className="h-3.5 w-3.5" />
                              <span>{priorityLabel[p]}</span>
                              {p === (issue.priority || "none") && (
                                <span className="ml-auto text-blue-400 text-[10px]">current</span>
                              )}
                            </CommandItem>
                          ))}
                        </CommandGroup>
                      </CommandList>
                    </Command>
                  </PopoverContent>
                </Popover>

                {/* Assignee */}
                <Popover open={assigneeOpen} onOpenChange={setAssigneeOpen}>
                  <PopoverTrigger asChild>
                    <div>
                      <PropertyRow>
                        <User className="h-3.5 w-3.5 text-muted-foreground/50" />
                        <span className="text-[12px] text-foreground/70 hover:text-foreground transition-colors">
                          {issue.assignee_name || "Unassigned"}
                        </span>
                      </PropertyRow>
                    </div>
                  </PopoverTrigger>
                  <PopoverContent className="w-56 p-1" align="start">
                    <Command>
                      <CommandInput placeholder="Search agents..." className="h-7 text-xs" />
                      <CommandList>
                        <CommandEmpty className="text-xs text-center py-2">No agents found</CommandEmpty>
                        <CommandGroup>
                          <CommandItem
                            value="unassigned"
                            className="text-xs"
                            onSelect={() => {
                              patchIssue({ assignee_type: null, assignee_id: null })
                              setAssigneeOpen(false)
                            }}
                          >
                            Unassigned
                          </CommandItem>
                          {crewAgents.map(agent => (
                            <CommandItem
                              key={agent.id}
                              value={`${agent.name} ${agent.slug} ${agent.crew_slug || ""}`}
                              className="text-xs"
                              onSelect={() => {
                                patchIssue({ assignee_type: "agent", assignee_id: agent.id })
                                setAssigneeOpen(false)
                              }}
                            >
                              <span>{agent.name}</span>
                              <span className="text-muted-foreground/40 ml-1">@{agent.slug}</span>
                              {agent.crew_slug && <span className="text-muted-foreground/30 ml-auto text-[10px]">{agent.crew_slug}</span>}
                            </CommandItem>
                          ))}
                        </CommandGroup>
                      </CommandList>
                    </Command>
                  </PopoverContent>
                </Popover>

                {/* Due date */}
                <Popover>
                  <PopoverTrigger asChild>
                    <div>
                      <PropertyRow>
                        <Clock className="h-3.5 w-3.5 text-muted-foreground/50" />
                        <span className="text-[12px] text-foreground/70 hover:text-foreground transition-colors">
                          {issue.due_date ? new Date(issue.due_date).toLocaleDateString() : "No due date"}
                        </span>
                      </PropertyRow>
                    </div>
                  </PopoverTrigger>
                  <PopoverContent className="w-auto p-2" align="start">
                    <input
                      type="date"
                      className="bg-transparent border border-white/[0.1] rounded px-2 py-1 text-xs text-foreground outline-none"
                      defaultValue={issue.due_date || ""}
                      onChange={(e) => {
                        patchIssue(e.target.value ? { due_date: e.target.value } : { due_date: null })
                      }}
                    />
                    {issue.due_date && (
                      <button
                        className="text-[11px] text-red-400 mt-1 hover:underline"
                        onClick={() => patchIssue({ due_date: null })}
                      >
                        Remove date
                      </button>
                    )}
                  </PopoverContent>
                </Popover>
              </div>
            )}
          </div>

          {/* ── Labels section ───────────────────────────────────────────── */}
          <div className="border-t border-white/[0.06]">
            <SectionHeader
              title="Labels"
              open={labelsOpen}
              onToggle={() => setLabelsOpen((v) => !v)}
              action={
                <Popover open={labelsPopoverOpen} onOpenChange={setLabelsPopoverOpen}>
                  <PopoverTrigger asChild>
                    <button className="p-0.5 rounded hover:bg-white/[0.06] text-muted-foreground/40 hover:text-muted-foreground/70 transition-colors">
                      <Plus className="h-3 w-3" />
                    </button>
                  </PopoverTrigger>
                  <PopoverContent className="w-[220px] p-0" align="end" sideOffset={4}>
                    <Command>
                      <CommandInput placeholder="Search labels..." className="text-xs h-8" />
                      <CommandList>
                        <CommandEmpty>No labels found.</CommandEmpty>
                        <CommandGroup>
                          {workspaceLabels.map((label) => {
                            const isAssigned = issueLabels.some((l) => l.id === label.id)
                            return (
                              <CommandItem
                                key={label.id}
                                value={label.name}
                                className="flex items-center gap-2 text-xs"
                                onSelect={() => {
                                  if (isAssigned) {
                                    const remaining = (issue.labels || []).filter(l => l.id !== label.id).map(l => l.id)
                                    patchIssue({ labels: remaining })
                                  } else {
                                    const updated = [...(issue.labels || []).map(l => l.id), label.id]
                                    patchIssue({ labels: updated })
                                  }
                                  setLabelsPopoverOpen(false)
                                }}
                              >
                                <span
                                  className="h-2.5 w-2.5 rounded-full shrink-0"
                                  style={{ backgroundColor: label.color }}
                                />
                                <span className={isAssigned ? "text-muted-foreground/50" : ""}>
                                  {label.name}
                                </span>
                                {isAssigned && (
                                  <span className="ml-auto text-[10px] text-muted-foreground/40">assigned</span>
                                )}
                              </CommandItem>
                            )
                          })}
                        </CommandGroup>
                      </CommandList>
                    </Command>
                  </PopoverContent>
                </Popover>
              }
            />
            {labelsOpen && (
              <div className="px-3 pb-1.5">
                {issueLabels.length > 0 ? (
                  <div className="flex flex-wrap gap-1">
                    {issueLabels.map((label) => (
                      <LabelBadge key={label.id} label={label} />
                    ))}
                  </div>
                ) : (
                  <span className="text-[11px] text-muted-foreground/40 pl-0.5">
                    No labels
                  </span>
                )}
              </div>
            )}
          </div>

          {/* ── Project section ────────────────────────────────────────── */}
          <div className="border-t border-white/[0.06]">
            <SectionHeader
              title="Project"
              open={projectOpen}
              onToggle={() => setProjectOpen((v) => !v)}
            />
            {projectOpen && (
              <div className="px-3 pb-2">
                <Popover open={projectPopoverOpen} onOpenChange={setProjectPopoverOpen}>
                  <PopoverTrigger asChild>
                    {issue.project_id ? (
                      <button className="flex items-center gap-2 py-1 w-full text-left group">
                        <div className="w-2.5 h-2.5 rounded-sm shrink-0" style={{ backgroundColor: matchingProject?.color || '#6B7280' }} />
                        <span className="text-[12px] text-foreground/80 flex-1 hover:text-foreground transition-colors">{matchingProject?.name || "Unknown"}</span>
                      </button>
                    ) : (
                      <button className="text-[12px] text-muted-foreground/50 hover:text-muted-foreground py-1 flex items-center gap-1.5">
                        <Plus className="h-3 w-3" /> Set project
                      </button>
                    )}
                  </PopoverTrigger>
                    <PopoverContent className="w-[220px] p-1" align="start" sideOffset={4}>
                      {projects.length === 0 ? (
                        <p className="text-[11px] text-muted-foreground/40 px-2 py-3 text-center">No projects</p>
                      ) : (
                        <>
                          {issue.project_id && (
                            <button
                              onClick={() => {
                                patchIssue({ project_id: "" })
                                setProjectPopoverOpen(false)
                              }}
                              className="flex items-center gap-2 w-full px-2 py-1.5 rounded-sm text-left text-[12px] text-red-400/70 hover:bg-red-500/10 transition-colors"
                            >
                              <X className="h-3 w-3" />
                              <span>Remove project</span>
                            </button>
                          )}
                          {projects.map((p) => (
                            <button
                              key={p.id}
                              onClick={() => {
                                patchIssue({ project_id: p.id })
                                setProjectPopoverOpen(false)
                              }}
                              className={cn(
                                "flex items-center gap-2 w-full px-2 py-1.5 rounded-sm text-left text-[12px] hover:bg-white/[0.06] transition-colors",
                                p.id === issue.project_id && "bg-white/[0.04]"
                              )}
                            >
                              <div className="w-2 h-2 rounded-sm shrink-0" style={{ backgroundColor: p.color }} />
                              <span className="text-foreground/80 truncate">{p.name}</span>
                              {p.id === issue.project_id && <span className="ml-auto text-[10px] text-muted-foreground/40">current</span>}
                            </button>
                          ))}
                        </>
                      )}
                    </PopoverContent>
                  </Popover>
              </div>
            )}
          </div>

          {/* ── Relations section ────────────────────────────────────────── */}
          <div className="border-t border-white/[0.06]">
            <SectionHeader
              title="Relations"
              open={relationsOpen}
              onToggle={() => setRelationsOpen((v) => !v)}
              action={
                <Popover open={addRelationOpen} onOpenChange={setAddRelationOpen}>
                  <PopoverTrigger asChild>
                    <button className="p-0.5 rounded hover:bg-white/[0.06] text-muted-foreground/40 hover:text-muted-foreground/70 transition-colors">
                      <Plus className="h-3 w-3" />
                    </button>
                  </PopoverTrigger>
                  <PopoverContent className="w-[260px] p-3" align="end" sideOffset={4}>
                    <div className="space-y-2">
                      <p className="text-[11px] font-medium text-foreground/80">Add relation</p>
                      <input
                        value={newRelationTarget}
                        onChange={(e) => setNewRelationTarget(e.target.value)}
                        placeholder="Target identifier (e.g. ENG-5)"
                        className="w-full h-7 px-2 bg-white/[0.04] border border-white/[0.08] rounded text-[11px] text-foreground placeholder:text-muted-foreground/30 outline-none focus:border-blue-400/40"
                        onKeyDown={(e) => {
                          if (e.key === "Enter") handleAddRelation()
                        }}
                      />
                      <div className="flex gap-1 flex-wrap">
                        {RELATION_TYPE_OPTIONS.map((opt) => (
                          <button
                            key={opt.value}
                            onClick={() => setNewRelationType(opt.value)}
                            className={cn(
                              "px-2 py-0.5 rounded text-[10px] border transition-colors",
                              newRelationType === opt.value
                                ? "border-blue-400/50 bg-blue-500/10 text-blue-400"
                                : "border-white/[0.08] text-muted-foreground/60 hover:border-white/[0.15]",
                            )}
                          >
                            {opt.label}
                          </button>
                        ))}
                      </div>
                      <button
                        onClick={handleAddRelation}
                        disabled={!newRelationTarget.trim() || addingRelation}
                        className={cn(
                          "w-full h-7 rounded text-[11px] font-medium transition-colors",
                          newRelationTarget.trim() && !addingRelation
                            ? "bg-blue-600 text-white hover:bg-blue-500"
                            : "bg-white/[0.04] text-muted-foreground/30 cursor-not-allowed",
                        )}
                      >
                        {addingRelation ? "Adding..." : "Add relation"}
                      </button>
                    </div>
                  </PopoverContent>
                </Popover>
              }
            />
            {relationsOpen && (
              <div className="px-3 pb-1.5">
                {relationsLoading ? (
                  <span className="text-[11px] text-muted-foreground/40">Loading...</span>
                ) : relations.length === 0 ? (
                  <span className="text-[11px] text-muted-foreground/40 pl-0.5">
                    No relations
                  </span>
                ) : (
                  <div className="space-y-2">
                    {Object.entries(relationsByType).map(([type, rels]) => (
                      <div key={type}>
                        <span className="text-[10px] uppercase tracking-wider text-muted-foreground/50">
                          {RELATION_TYPE_LABELS[type as RelationType] || type}
                        </span>
                        <div className="mt-0.5 space-y-0.5">
                          {rels.map((rel) => (
                            <div
                              key={rel.id}
                              className="group flex items-center gap-1.5 py-1 px-1 rounded hover:bg-white/[0.04] transition-colors"
                            >
                              {rel.target_status ? (
                                <StatusIcon
                                  status={rel.target_status}
                                  className="h-3 w-3 shrink-0"
                                />
                              ) : (
                                <Link2 className="h-3 w-3 shrink-0 text-muted-foreground/40" />
                              )}
                              <span className="text-[10px] font-mono text-muted-foreground/60 shrink-0">
                                {rel.target_identifier || "--"}
                              </span>
                              <span className="text-[11px] text-foreground/70 truncate flex-1">
                                {rel.target_title || "Untitled"}
                              </span>
                              <button
                                onClick={() => handleDeleteRelation(rel.id)}
                                className="opacity-0 group-hover:opacity-100 p-0.5 rounded hover:bg-white/[0.08] text-muted-foreground/40 hover:text-red-400 transition-all"
                              >
                                <X className="h-2.5 w-2.5" />
                              </button>
                            </div>
                          ))}
                        </div>
                      </div>
                    ))}
                  </div>
                )}
              </div>
            )}
          </div>

          {/* ── Comments section ─────────────────────────────────────────── */}
          <div className="border-t border-white/[0.06] mt-1">
            <div className="px-3 py-1.5">
              <span className="text-[11px] uppercase tracking-wider text-muted-foreground/60 font-medium">
                Comments ({comments.length})
              </span>
            </div>

            <div className="px-3">
              {comments.length > 0 ? (
                <div className="space-y-2.5">
                  {comments.map((comment) => (
                    <div key={comment.id} className="flex gap-2">
                      <div className="w-5 h-5 rounded-full bg-white/[0.08] flex items-center justify-center shrink-0 mt-0.5">
                        <span className="text-[9px] font-semibold text-muted-foreground/60">
                          {(comment.author_name || comment.author_type || "?")[0].toUpperCase()}
                        </span>
                      </div>
                      <div className="flex-1 min-w-0">
                        <div className="flex items-center gap-2">
                          <span className="text-[11px] font-medium text-foreground/80">
                            {comment.author_name || comment.author_type}
                          </span>
                          <span className="text-[10px] text-muted-foreground/40">
                            {formatRelativeTime(comment.created_at)}
                          </span>
                        </div>
                        <p className="text-[11px] text-foreground/70 mt-0.5 leading-relaxed whitespace-pre-wrap">
                          {comment.body}
                        </p>
                      </div>
                    </div>
                  ))}
                </div>
              ) : (
                <p className="text-[11px] text-muted-foreground/40">No comments yet</p>
              )}

              {/* New comment input */}
              <div className="mt-3 flex gap-2">
                <textarea
                  value={newComment}
                  onChange={(e) => setNewComment(e.target.value)}
                  placeholder="Write a comment..."
                  rows={2}
                  className="flex-1 bg-white/[0.03] border border-white/[0.08] rounded-md px-2 py-1.5 text-[11px] text-foreground placeholder:text-muted-foreground/30 outline-none focus:border-blue-400/40 resize-none"
                  onKeyDown={(e) => {
                    if (e.key === "Enter" && (e.metaKey || e.ctrlKey)) {
                      e.preventDefault()
                      handleCommentSubmit()
                    }
                  }}
                />
                <button
                  onClick={handleCommentSubmit}
                  disabled={!newComment.trim() || submitting}
                  className={cn(
                    "self-end p-1.5 rounded-md transition-colors",
                    newComment.trim() && !submitting
                      ? "bg-blue-600 text-white hover:bg-blue-500"
                      : "bg-white/[0.04] text-muted-foreground/30",
                  )}
                >
                  <Send className="h-3 w-3" />
                </button>
              </div>
            </div>
          </div>

          {/* ── Activity timeline ────────────────────────────────────────── */}
          <div className="border-t border-white/[0.06] pt-3 px-4 pb-4">
            <div className="text-[10px] font-semibold text-muted-foreground/60 uppercase tracking-wider mb-2">Activity</div>
            <div className="space-y-2">
              {activities.length === 0 ? (
                <p className="text-[11px] text-muted-foreground/40">No activity yet</p>
              ) : (
                activities.map((a) => (
                  <div key={a.id} className="flex items-start gap-2">
                    <div className={cn(
                      "w-1.5 h-1.5 rounded-full mt-1.5 shrink-0",
                      a.action === "task_completed" ? "bg-green-500" :
                      a.action === "task_failed" ? "bg-red-500" :
                      a.action === "review_approved" ? "bg-indigo-500" :
                      a.action === "review_changes_requested" ? "bg-amber-500" :
                      a.action === "status_changed" ? "bg-blue-500" :
                      "bg-muted-foreground/30"
                    )} />
                    <div className="flex-1 min-w-0">
                      <span className="text-[11px] text-muted-foreground/70">
                        <span className="text-foreground/60">{a.actor_name || a.actor_id}</span>
                        {" "}
                        {actionLabel(a.action)}
                      </span>
                      {a.details && (
                        <p className="text-[10px] text-muted-foreground/40 mt-0.5 line-clamp-2">{a.details}</p>
                      )}
                    </div>
                    <span className="text-[10px] text-muted-foreground/30 shrink-0">{timeAgo(a.created_at)}</span>
                  </div>
                ))
              )}
            </div>
          </div>
        </div>
      </div>
    </div>
  )
}

/* -------------------------------------------------------------------------- */
/*  ProjectsListView — Linear-style projects table for center panel           */
/* -------------------------------------------------------------------------- */

function ProjectStatusIcon({ status, className }: { status: ProjectStatus; className?: string }) {
  switch (status) {
    case "backlog":
      return (
        <svg className={className} viewBox="0 0 16 16" fill="none">
          <circle cx="8" cy="8" r="6" stroke="currentColor" strokeWidth="1.5" strokeDasharray="3 3" opacity="0.5" />
        </svg>
      )
    case "planned":
      return (
        <svg className={className} viewBox="0 0 16 16" fill="none">
          <circle cx="8" cy="8" r="6" stroke="currentColor" strokeWidth="1.5" opacity="0.6" />
        </svg>
      )
    case "in_progress":
      return (
        <svg className={className} viewBox="0 0 16 16" fill="none">
          <circle cx="8" cy="8" r="6" stroke="currentColor" strokeWidth="1.5" opacity="0.3" />
          <path d="M8 2a6 6 0 0 1 6 6" stroke="currentColor" strokeWidth="1.5" strokeLinecap="round" />
        </svg>
      )
    case "paused":
      return (
        <svg className={className} viewBox="0 0 16 16" fill="none">
          <circle cx="8" cy="8" r="6" stroke="currentColor" strokeWidth="1.5" opacity="0.4" />
          <rect x="6" y="5" width="1.5" height="6" rx="0.5" fill="currentColor" opacity="0.6" />
          <rect x="8.5" y="5" width="1.5" height="6" rx="0.5" fill="currentColor" opacity="0.6" />
        </svg>
      )
    case "completed":
      return (
        <svg className={className} viewBox="0 0 16 16" fill="none">
          <circle cx="8" cy="8" r="6" fill="currentColor" opacity="0.15" stroke="currentColor" strokeWidth="1.5" />
          <path d="M5.5 8l2 2 3.5-3.5" stroke="currentColor" strokeWidth="1.5" strokeLinecap="round" strokeLinejoin="round" />
        </svg>
      )
    case "cancelled":
      return (
        <svg className={className} viewBox="0 0 16 16" fill="none">
          <circle cx="8" cy="8" r="6" stroke="currentColor" strokeWidth="1.5" opacity="0.3" />
          <path d="M5.5 5.5l5 5M10.5 5.5l-5 5" stroke="currentColor" strokeWidth="1.5" strokeLinecap="round" />
        </svg>
      )
    default:
      return null
  }
}

function HealthBadge({ health }: { health: string }) {
  switch (health) {
    case "at_risk":
      return <span className="text-[11px] text-amber-400">At risk</span>
    case "off_track":
      return <span className="text-[11px] text-red-400">Off track</span>
    default:
      return <span className="text-[11px] text-muted-foreground/40">No updates</span>
  }
}

interface ProjectsListViewProps {
  projects: Project[]
  onRefresh: () => void
  workspaceId: string
  onProjectClick?: (projectId: string) => void
}

export function ProjectsListView({ projects, onRefresh: _onRefresh, workspaceId: _workspaceId, onProjectClick }: ProjectsListViewProps) {
  const sorted = useMemo(
    () => [...projects].sort((a, b) => a.name.localeCompare(b.name)),
    [projects],
  )

  if (projects.length === 0) {
    return (
      <div className="flex flex-col items-center justify-center h-full text-muted-foreground/50">
        <FolderKanban className="h-6 w-6 mb-2" />
        <p className="text-[12px]">No projects yet</p>
        <p className="text-[10px] text-muted-foreground/30 mt-1">Projects will appear here once created</p>
      </div>
    )
  }

  return (
    <div>
      <table className="w-full text-[12px]">
        <thead>
          <tr className="text-left text-muted-foreground/60 border-b border-white/[0.06]">
            <th className="py-2 px-3 font-medium">Name</th>
            <th className="py-2 px-3 font-medium w-24">Health</th>
            <th className="py-2 px-3 font-medium w-20">Priority</th>
            <th className="py-2 px-3 font-medium w-28">Lead</th>
            <th className="py-2 px-3 font-medium w-28">Target date</th>
            <th className="py-2 px-3 font-medium w-32">Status</th>
          </tr>
        </thead>
        <tbody>
          {sorted.map((p) => (
            <tr key={p.id} className="border-b border-white/[0.04] hover:bg-white/[0.02] transition-colors cursor-pointer" onClick={() => onProjectClick?.(p.id)}>
              {/* Name */}
              <td className="py-2 px-3">
                <div className="flex items-center gap-2">
                  <div className="w-3 h-3 rounded-sm shrink-0 flex items-center justify-center" style={{ backgroundColor: p.color }}>
                    {p.icon ? (
                      <span className="text-[8px] text-white font-bold">{p.icon.charAt(0).toUpperCase()}</span>
                    ) : null}
                  </div>
                  <span className="text-foreground/90 font-medium truncate">{p.name}</span>
                </div>
              </td>
              {/* Health */}
              <td className="py-2 px-3">
                <HealthBadge health={p.health} />
              </td>
              {/* Priority */}
              <td className="py-2 px-3">
                <div className="flex items-center gap-1.5">
                  <PriorityIcon priority={p.priority || "none"} className="h-3.5 w-3.5" />
                  <span className="text-foreground/60 capitalize">{p.priority || "None"}</span>
                </div>
              </td>
              {/* Lead */}
              <td className="py-2 px-3">
                {p.lead_name ? (
                  <div className="flex items-center gap-1.5">
                    <div className="w-4 h-4 rounded-full bg-white/[0.08] flex items-center justify-center shrink-0">
                      <span className="text-[8px] font-semibold text-muted-foreground/60">
                        {p.lead_name.charAt(0).toUpperCase()}
                      </span>
                    </div>
                    <span className="text-foreground/60 truncate">{p.lead_name}</span>
                  </div>
                ) : (
                  <span className="text-muted-foreground/30">&mdash;</span>
                )}
              </td>
              {/* Target date */}
              <td className="py-2 px-3">
                {p.target_date ? (
                  <span className="text-foreground/60">{new Date(p.target_date).toLocaleDateString("en-US", { month: "short", day: "numeric", year: "numeric" })}</span>
                ) : (
                  <span className="text-muted-foreground/30">&mdash;</span>
                )}
              </td>
              {/* Status / Progress */}
              <td className="py-2 px-3">
                <div className="flex items-center gap-2">
                  <ProjectStatusIcon status={p.status} className="h-3.5 w-3.5 text-muted-foreground/70 shrink-0" />
                  <span className="text-foreground/60 tabular-nums w-8 text-right">{p.progress}%</span>
                  <div className="w-12 h-1.5 bg-white/[0.06] rounded-full overflow-hidden">
                    <div
                      className={cn(
                        "h-full rounded-full transition-all",
                        p.progress >= 100 ? "bg-green-500/70" : "bg-blue-500/60",
                      )}
                      style={{ width: `${Math.min(p.progress, 100)}%` }}
                    />
                  </div>
                </div>
              </td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  )
}


/*  ProjectDetailInline — Right panel for editing project properties          */
/* ═══════════════════════════════════════════════════════════════════════════ */

interface ProjectDetailInlineProps {
  project: Project
  workspaceId: string
  onClose: () => void
  onUpdated: () => void
}

const PROJECT_STATUSES: { value: ProjectStatus; label: string }[] = [
  { value: "backlog", label: "Backlog" },
  { value: "planned", label: "Planned" },
  { value: "in_progress", label: "In Progress" },
  { value: "paused", label: "Paused" },
  { value: "completed", label: "Completed" },
  { value: "cancelled", label: "Cancelled" },
]

const HEALTH_OPTIONS: { value: string; label: string; color: string }[] = [
  { value: "on_track", label: "On Track", color: "text-green-400" },
  { value: "at_risk", label: "At Risk", color: "text-yellow-400" },
  { value: "off_track", label: "Off Track", color: "text-red-400" },
]

const PRIORITY_OPTIONS: { value: string; label: string }[] = [
  { value: "urgent", label: "Urgent" },
  { value: "high", label: "High" },
  { value: "medium", label: "Medium" },
  { value: "low", label: "Low" },
  { value: "none", label: "No priority" },
]

interface ProjectStats {
  total_issues: number
  completed_issues: number
  by_status: Record<string, number>
  by_assignee: { agent_id: string; agent_name: string; total: number; completed: number }[]
  by_label: { label_name: string; color: string; count: number }[]
  crews: string[]
}

const STATUS_COLORS: Record<string, string> = {
  BACKLOG: "#6b7280",
  TODO: "#a3a3a3",
  IN_PROGRESS: "#3b82f6",
  REVIEW: "#a855f7",
  DONE: "#22c55e",
  CANCELLED: "#ef4444",
}

export function ProjectDetailInline({ project, workspaceId, onClose, onUpdated }: ProjectDetailInlineProps) {
  const [editingTitle, setEditingTitle] = useState(false)
  const [titleDraft, setTitleDraft] = useState(project.name)
  const [stats, setStats] = useState<ProjectStats | null>(null)
  const [allAgents, setAllAgents] = useState<{ id: string; name: string; slug: string }[]>([])

  // Section collapse state
  const [propertiesOpen, setPropertiesOpen] = useState(true)
  const [milestonesOpen, setMilestonesOpen] = useState(false)
  const [progressOpen, setProgressOpen] = useState(true)

  // Popover state
  const [leadOpen, setLeadOpen] = useState(false)

  // Progress breakdown tab
  const [progressTab, setProgressTab] = useState<"assignees" | "labels">("assignees")

  // Fetch stats + agents
  useEffect(() => {
    fetch(`/api/v1/projects/${project.id}/stats?workspace_id=${workspaceId}`)
      .then((r) => (r.ok ? r.json() : null))
      .then(setStats)
      .catch(() => {})
    fetch(`/api/v1/agents?workspace_id=${workspaceId}`)
      .then((r) => (r.ok ? r.json() : []))
      .then((agents: { id: string; name: string; slug: string }[]) =>
        setAllAgents(agents.map((a) => ({ id: a.id, name: a.name, slug: a.slug }))),
      )
      .catch(() => {})
  }, [project.id, workspaceId])

  const patchProject = useCallback(
    async (fields: Record<string, unknown>) => {
      const qs = `?workspace_id=${encodeURIComponent(workspaceId)}`
      const res = await fetch(`/api/v1/projects/${project.id}${qs}`, {
        method: "PATCH",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify(fields),
      })
      if (res.ok) {
        toast.success("Project updated")
        onUpdated()
      } else {
        const err = await res.json().catch(() => null)
        toast.error(err?.detail || "Failed to update project")
      }
    },
    [project.id, workspaceId, onUpdated],
  )

  // Donut chart for status breakdown
  const donutSegments = useMemo(() => {
    if (!stats?.by_status) return []
    const entries = Object.entries(stats.by_status).filter(([, v]) => v > 0)
    const total = entries.reduce((sum, [, v]) => sum + v, 0)
    if (total === 0) return []
    const segments: { status: string; value: number; pct: number; color: string }[] = []
    entries.forEach(([status, value]) => {
      segments.push({
        status,
        value,
        pct: (value / total) * 100,
        color: STATUS_COLORS[status] || "#6b7280",
      })
    })
    return segments
  }, [stats?.by_status])

  const donutPaths = useMemo(() => {
    if (donutSegments.length === 0) return []
    const radius = 16
    const circumference = 2 * Math.PI * radius
    let offset = 0
    return donutSegments.map((seg) => {
      const dashLen = (seg.pct / 100) * circumference
      const path = {
        ...seg,
        dasharray: `${dashLen} ${circumference - dashLen}`,
        dashoffset: -offset,
      }
      offset += dashLen
      return path
    })
  }, [donutSegments])

  return (
    <div className="h-full flex flex-col border-l border-white/[0.06] bg-card">
      {/* Header */}
      <div className="flex items-center justify-between px-4 py-3 border-b border-white/[0.06] shrink-0">
        <div className="flex items-center gap-2">
          <div className="w-3 h-3 rounded-sm" style={{ backgroundColor: project.color }} />
          <span className="text-[11px] font-mono text-muted-foreground/60">Project</span>
        </div>
        <button
          onClick={onClose}
          className="p-1 rounded hover:bg-white/[0.06] text-muted-foreground/50 hover:text-muted-foreground transition-colors"
        >
          <X className="h-3.5 w-3.5" />
        </button>
      </div>

      <ScrollArea className="flex-1">
        <div className="p-4 space-y-4">
          {/* Title */}
          {editingTitle ? (
            <input
              className="text-[15px] font-semibold text-foreground bg-transparent border-b border-blue-500 outline-none w-full pb-1"
              value={titleDraft}
              onChange={(e) => setTitleDraft(e.target.value)}
              onBlur={() => {
                if (titleDraft.trim() && titleDraft !== project.name) patchProject({ name: titleDraft.trim() })
                setEditingTitle(false)
              }}
              onKeyDown={(e) => {
                if (e.key === "Enter") (e.target as HTMLInputElement).blur()
                if (e.key === "Escape") {
                  setTitleDraft(project.name)
                  setEditingTitle(false)
                }
              }}
              autoFocus
            />
          ) : (
            <h2
              className="text-[15px] font-semibold text-foreground cursor-pointer hover:text-blue-400 transition-colors"
              onClick={() => {
                setTitleDraft(project.name)
                setEditingTitle(true)
              }}
            >
              {project.name}
            </h2>
          )}

          {project.description && (
            <p className="text-[12px] text-muted-foreground/70 leading-relaxed">{project.description}</p>
          )}

          {/* ── Properties ─────────────────────────────────────────── */}
          <SectionHeader title="Properties" open={propertiesOpen} onToggle={() => setPropertiesOpen((v) => !v)} />
          {propertiesOpen && (
            <div className="space-y-0.5">
              {/* Status */}
              <Popover>
                <PopoverTrigger asChild>
                  <div>
                    <PropertyRow>
                      <ProjectStatusIcon status={project.status} />
                      <span className="text-[12px] text-foreground/80">
                        {PROJECT_STATUSES.find((s) => s.value === project.status)?.label || project.status}
                      </span>
                    </PropertyRow>
                  </div>
                </PopoverTrigger>
                <PopoverContent className="w-48 p-1" align="start">
                  {PROJECT_STATUSES.map((s) => (
                    <button
                      key={s.value}
                      onClick={() => patchProject({ status: s.value })}
                      className={cn(
                        "flex items-center gap-2 w-full px-2 py-1.5 rounded text-xs hover:bg-white/[0.06]",
                        s.value === project.status && "bg-white/[0.04]",
                      )}
                    >
                      <ProjectStatusIcon status={s.value as ProjectStatus} />
                      {s.label}
                    </button>
                  ))}
                </PopoverContent>
              </Popover>

              {/* Priority */}
              <Popover>
                <PopoverTrigger asChild>
                  <div>
                    <PropertyRow>
                      <PriorityIcon priority={project.priority || "none"} className="h-3.5 w-3.5" />
                      <span className="text-[12px] text-foreground/80">{priorityLabel[project.priority || "none"]}</span>
                    </PropertyRow>
                  </div>
                </PopoverTrigger>
                <PopoverContent className="w-48 p-1" align="start">
                  {PRIORITY_OPTIONS.map((p) => (
                    <button
                      key={p.value}
                      onClick={() => patchProject({ priority: p.value })}
                      className={cn(
                        "flex items-center gap-2 w-full px-2 py-1.5 rounded text-xs hover:bg-white/[0.06]",
                        p.value === project.priority && "bg-white/[0.04]",
                      )}
                    >
                      <PriorityIcon priority={p.value as IssuePriority} className="h-3.5 w-3.5" />
                      {p.label}
                    </button>
                  ))}
                </PopoverContent>
              </Popover>

              {/* Health */}
              <Popover>
                <PopoverTrigger asChild>
                  <div>
                    <PropertyRow>
                      <span
                        className={cn(
                          "text-[12px] font-medium",
                          HEALTH_OPTIONS.find((h) => h.value === project.health)?.color || "text-muted-foreground",
                        )}
                      >
                        {HEALTH_OPTIONS.find((h) => h.value === project.health)?.label || project.health}
                      </span>
                    </PropertyRow>
                  </div>
                </PopoverTrigger>
                <PopoverContent className="w-48 p-1" align="start">
                  {HEALTH_OPTIONS.map((h) => (
                    <button
                      key={h.value}
                      onClick={() => patchProject({ health: h.value })}
                      className={cn(
                        "flex items-center gap-2 w-full px-2 py-1.5 rounded text-xs hover:bg-white/[0.06]",
                        h.value === project.health && "bg-white/[0.04]",
                      )}
                    >
                      <span className={h.color}>{h.label}</span>
                    </button>
                  ))}
                </PopoverContent>
              </Popover>

              {/* Lead */}
              <Popover open={leadOpen} onOpenChange={setLeadOpen}>
                <PopoverTrigger asChild>
                  <div>
                    <PropertyRow>
                      <User className="h-3.5 w-3.5 text-muted-foreground/50" />
                      <span className="text-[12px] text-foreground/70">{project.lead_name || "Add lead"}</span>
                      {project.lead_id && (
                        <img
                          src={getAgentAvatarUrl(project.lead_id)}
                          alt=""
                          className="h-4 w-4 rounded-full ml-auto"
                        />
                      )}
                    </PropertyRow>
                  </div>
                </PopoverTrigger>
                <PopoverContent className="w-52 p-0" align="start">
                  <Command>
                    <CommandInput placeholder="Search agents..." />
                    <CommandList>
                      <CommandEmpty>No agents found</CommandEmpty>
                      <CommandGroup>
                        <CommandItem
                          onSelect={() => {
                            patchProject({ lead_type: null, lead_id: null })
                            setLeadOpen(false)
                          }}
                        >
                          <User className="h-3.5 w-3.5 text-muted-foreground/50 mr-2" />
                          No lead
                        </CommandItem>
                        {allAgents.map((a) => (
                          <CommandItem
                            key={a.id}
                            onSelect={() => {
                              patchProject({ lead_type: "agent", lead_id: a.id })
                              setLeadOpen(false)
                            }}
                          >
                            <img src={getAgentAvatarUrl(a.id)} alt="" className="h-4 w-4 rounded-full mr-2" />
                            {a.name}
                          </CommandItem>
                        ))}
                      </CommandGroup>
                    </CommandList>
                  </Command>
                </PopoverContent>
              </Popover>

              {/* Members */}
              {stats?.by_assignee && stats.by_assignee.length > 0 && (
                <PropertyRow className="cursor-default">
                  <FolderKanban className="h-3.5 w-3.5 text-muted-foreground/50" />
                  <div className="flex items-center gap-1 flex-1 min-w-0">
                    <span className="text-[12px] text-foreground/70 shrink-0">Members</span>
                    <div className="flex -space-x-1 ml-auto">
                      {stats.by_assignee.slice(0, 5).map((a) => (
                        <img
                          key={a.agent_id}
                          src={getAgentAvatarUrl(a.agent_id || a.agent_name)}
                          alt={a.agent_name}
                          title={a.agent_name}
                          className="h-4 w-4 rounded-full ring-1 ring-card"
                        />
                      ))}
                      {stats.by_assignee.length > 5 && (
                        <span className="text-[10px] text-muted-foreground/50 pl-1">
                          +{stats.by_assignee.length - 5}
                        </span>
                      )}
                    </div>
                  </div>
                </PropertyRow>
              )}

              {/* Dates */}
              <Popover>
                <PopoverTrigger asChild>
                  <div>
                    <PropertyRow>
                      <Clock className="h-3.5 w-3.5 text-muted-foreground/50" />
                      <span className="text-[12px] text-foreground/70">
                        {project.start_date || project.target_date
                          ? `${project.start_date || "?"} → ${project.target_date || "?"}`
                          : "No dates set"}
                      </span>
                    </PropertyRow>
                  </div>
                </PopoverTrigger>
                <PopoverContent className="w-auto p-3 space-y-2" align="start">
                  <div>
                    <label className="text-[10px] text-muted-foreground/60 block mb-1">Start date</label>
                    <input
                      type="date"
                      className="bg-transparent border border-white/[0.1] rounded px-2 py-1 text-xs text-foreground outline-none w-full"
                      defaultValue={project.start_date || ""}
                      onChange={(e) => patchProject({ start_date: e.target.value || null })}
                    />
                  </div>
                  <div>
                    <label className="text-[10px] text-muted-foreground/60 block mb-1">Target date</label>
                    <input
                      type="date"
                      className="bg-transparent border border-white/[0.1] rounded px-2 py-1 text-xs text-foreground outline-none w-full"
                      defaultValue={project.target_date || ""}
                      onChange={(e) => patchProject({ target_date: e.target.value || null })}
                    />
                  </div>
                </PopoverContent>
              </Popover>

              {/* Teams */}
              {stats?.crews && stats.crews.length > 0 && (
                <PropertyRow className="cursor-default">
                  <FolderKanban className="h-3.5 w-3.5 text-muted-foreground/50" />
                  <span className="text-[12px] text-foreground/70 shrink-0">Teams</span>
                  <div className="flex items-center gap-1 ml-auto flex-wrap justify-end">
                    {stats.crews.map((crew) => (
                      <span
                        key={crew}
                        className="text-[10px] px-1.5 py-0.5 rounded bg-white/[0.06] text-muted-foreground/70"
                      >
                        {crew}
                      </span>
                    ))}
                  </div>
                </PropertyRow>
              )}

              {/* Labels */}
              {stats?.by_label && stats.by_label.length > 0 && (
                <PropertyRow className="cursor-default">
                  <span className="text-[12px] text-foreground/70 shrink-0">Labels</span>
                  <div className="flex items-center gap-1 ml-auto flex-wrap justify-end">
                    {stats.by_label.map((l) => (
                      <span
                        key={l.label_name}
                        className="text-[10px] px-1.5 py-0.5 rounded flex items-center gap-1"
                        style={{ backgroundColor: `${l.color}20`, color: l.color }}
                      >
                        <span className="w-1.5 h-1.5 rounded-full" style={{ backgroundColor: l.color }} />
                        {l.label_name}
                      </span>
                    ))}
                  </div>
                </PropertyRow>
              )}
            </div>
          )}

          {/* ── Milestones ─────────────────────────────────────────── */}
          <SectionHeader title="Milestones" open={milestonesOpen} onToggle={() => setMilestonesOpen((v) => !v)} />
          {milestonesOpen && (
            <div className="px-3 py-2">
              <p className="text-[12px] text-muted-foreground/50">No milestones yet</p>
              <p className="text-[11px] text-muted-foreground/30 mt-1">
                Add milestones to organize work within your project
              </p>
            </div>
          )}

          {/* ── Progress ─────────────────────────────────────────── */}
          <SectionHeader title="Progress" open={progressOpen} onToggle={() => setProgressOpen((v) => !v)} />
          {progressOpen && (
            <div className="space-y-3 px-3">
              {/* Stat boxes */}
              <div className="grid grid-cols-2 gap-2">
                <div className="bg-white/[0.03] border border-white/[0.06] rounded-md px-3 py-2">
                  <div className="text-[10px] text-muted-foreground/50 uppercase tracking-wider">Scope</div>
                  <div className="text-[18px] font-semibold text-foreground tabular-nums">
                    {stats?.total_issues ?? project.issue_count}
                  </div>
                </div>
                <div className="bg-white/[0.03] border border-white/[0.06] rounded-md px-3 py-2">
                  <div className="text-[10px] text-muted-foreground/50 uppercase tracking-wider">Completed</div>
                  <div className="text-[18px] font-semibold text-green-400 tabular-nums">
                    {stats?.completed_issues ?? project.done_count}
                  </div>
                </div>
              </div>

              {/* Donut chart */}
              {donutPaths.length > 0 && (
                <div className="flex items-center gap-4">
                  <svg viewBox="0 0 40 40" className="w-16 h-16 shrink-0">
                    {donutPaths.map((seg) => (
                      <circle
                        key={seg.status}
                        cx="20"
                        cy="20"
                        r="16"
                        fill="none"
                        stroke={seg.color}
                        strokeWidth="5"
                        strokeDasharray={seg.dasharray}
                        strokeDashoffset={seg.dashoffset}
                        transform="rotate(-90 20 20)"
                        className="transition-all duration-300"
                      />
                    ))}
                  </svg>
                  <div className="space-y-0.5 flex-1 min-w-0">
                    {donutSegments.map((seg) => (
                      <div key={seg.status} className="flex items-center gap-1.5">
                        <span className="w-2 h-2 rounded-sm shrink-0" style={{ backgroundColor: seg.color }} />
                        <span className="text-[10px] text-muted-foreground/70 truncate flex-1">
                          {seg.status.replace(/_/g, " ")}
                        </span>
                        <span className="text-[10px] text-muted-foreground/50 tabular-nums">{seg.value}</span>
                      </div>
                    ))}
                  </div>
                </div>
              )}

              {/* Tabs */}
              <div className="flex items-center gap-0 border-b border-white/[0.06]">
                <button
                  onClick={() => setProgressTab("assignees")}
                  className={cn(
                    "text-[11px] px-2 py-1.5 border-b-2 transition-colors",
                    progressTab === "assignees"
                      ? "border-blue-500 text-foreground"
                      : "border-transparent text-muted-foreground/50 hover:text-muted-foreground/70",
                  )}
                >
                  Assignees
                </button>
                <button
                  onClick={() => setProgressTab("labels")}
                  className={cn(
                    "text-[11px] px-2 py-1.5 border-b-2 transition-colors",
                    progressTab === "labels"
                      ? "border-blue-500 text-foreground"
                      : "border-transparent text-muted-foreground/50 hover:text-muted-foreground/70",
                  )}
                >
                  Labels
                </button>
              </div>

              {/* Assignees tab */}
              {progressTab === "assignees" && (
                <div className="space-y-2">
                  {stats?.by_assignee && stats.by_assignee.length > 0 ? (
                    stats.by_assignee.map((a) => (
                      <div key={a.agent_id || a.agent_name} className="flex items-center gap-2">
                        <img
                          src={getAgentAvatarUrl(a.agent_id || a.agent_name)}
                          alt=""
                          className="h-5 w-5 rounded-full"
                        />
                        <span className="text-[12px] text-foreground/80 flex-1 truncate">{a.agent_name}</span>
                        <span className="text-[11px] text-muted-foreground/50 tabular-nums">
                          {a.completed} of {a.total}
                        </span>
                        <div className="w-8 h-1.5 bg-white/[0.06] rounded-full overflow-hidden">
                          <div
                            className="h-full bg-blue-500/70 rounded-full"
                            style={{ width: `${a.total > 0 ? (a.completed / a.total) * 100 : 0}%` }}
                          />
                        </div>
                      </div>
                    ))
                  ) : (
                    <p className="text-[11px] text-muted-foreground/40">No assignees yet</p>
                  )}
                </div>
              )}

              {/* Labels tab */}
              {progressTab === "labels" && (
                <div className="space-y-2">
                  {stats?.by_label && stats.by_label.length > 0 ? (
                    stats.by_label.map((l) => (
                      <div key={l.label_name} className="flex items-center gap-2">
                        <span className="w-2.5 h-2.5 rounded-full shrink-0" style={{ backgroundColor: l.color }} />
                        <span className="text-[12px] text-foreground/80 flex-1 truncate">{l.label_name}</span>
                        <span className="text-[11px] text-muted-foreground/50 tabular-nums">{l.count}</span>
                      </div>
                    ))
                  ) : (
                    <p className="text-[11px] text-muted-foreground/40">No labels yet</p>
                  )}
                </div>
              )}
            </div>
          )}

          {/* ── Activity ─────────────────────────────────────────── */}
          <div className="pt-2 border-t border-white/[0.06] space-y-1 px-3">
            <div className="text-[10px] text-muted-foreground/40 font-mono">
              Created: {new Date(project.created_at).toLocaleDateString()}
            </div>
            <div className="text-[10px] text-muted-foreground/40 font-mono">ID: {project.id}</div>
          </div>
        </div>
      </ScrollArea>
    </div>
  )
}
