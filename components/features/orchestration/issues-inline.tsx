"use client"

import { useCallback, useEffect, useState } from "react"
import { ScrollArea } from "@/components/ui/scroll-area"
import { Search, X, Send, Clock, Plus, ChevronDown, ChevronRight, User, LayoutGrid, Link2 } from "lucide-react"
import { Popover, PopoverContent, PopoverTrigger } from "@/components/ui/popover"
import { Command, CommandEmpty, CommandGroup, CommandInput, CommandItem, CommandList } from "@/components/ui/command"
import { StatusIcon, statusLabel } from "@/components/features/issues/status-icon"
import { PriorityIcon, priorityLabel } from "@/components/features/issues/priority-icon"
import { LabelBadge } from "@/components/features/issues/label-badge"
import { IssuesBoardView } from "@/components/features/issues/issues-board-view"
import { IssuesListView } from "@/components/features/issues/issues-list-view"
import { cn } from "@/lib/utils"
import { toast } from "sonner"
import type { Mission, MissionStatus, IssueLabel, IssueComment, IssuePriority, IssueRelation, RelationType } from "@/lib/types/mission"

/* -------------------------------------------------------------------------- */
/*  IssuesExplorerPanel — left panel                                          */
/* -------------------------------------------------------------------------- */

interface IssuesExplorerPanelProps {
  issues: Mission[]
  search: string
  onSearchChange: (value: string) => void
  selectedIssue: Mission | null
  onIssueSelect: (issue: Mission) => void
}

export function IssuesExplorerPanel({
  issues,
  search,
  onSearchChange,
  selectedIssue,
  onIssueSelect,
}: IssuesExplorerPanelProps) {
  const filtered = search
    ? issues.filter(
        (i) =>
          i.title.toLowerCase().includes(search.toLowerCase()) ||
          (i.identifier && i.identifier.toLowerCase().includes(search.toLowerCase())),
      )
    : issues

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

      {/* Issue count */}
      <div className="px-3 pb-1 shrink-0">
        <span className="text-[10px] text-muted-foreground/50">{filtered.length} issues</span>
      </div>

      {/* Issue list */}
      <ScrollArea className="flex-1">
        <div className="px-1">
          {filtered.map((issue) => {
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
                <PriorityIcon priority={issue.priority || "none"} className="h-3 w-3 shrink-0" />
              </button>
            )
          })}
          {filtered.length === 0 && (
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

const ALL_STATUSES: MissionStatus[] = [
  "BACKLOG", "TODO", "PLANNING", "IN_PROGRESS", "REVIEW", "COMPLETED", "FAILED", "CANCELLED", "DUPLICATE",
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
  workspaceId: string
  onClose: () => void
  onUpdated: () => void
}

export function IssueDetailInline({
  issue,
  comments,
  labels: workspaceLabels,
  workspaceId,
  onClose,
  onUpdated,
}: IssueDetailInlineProps) {
  const [newComment, setNewComment] = useState("")
  const [submitting, setSubmitting] = useState(false)
  const [editingTitle, setEditingTitle] = useState(false)
  const [titleDraft, setTitleDraft] = useState(issue.title)

  // Section collapse state
  const [propertiesOpen, setPropertiesOpen] = useState(true)
  const [labelsOpen, setLabelsOpen] = useState(true)
  const [projectOpen, setProjectOpen] = useState(true)
  const [relationsOpen, setRelationsOpen] = useState(true)

  // Popover open state
  const [statusOpen, setStatusOpen] = useState(false)
  const [priorityOpen, setPriorityOpen] = useState(false)
  const [labelsPopoverOpen, setLabelsPopoverOpen] = useState(false)

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
        <button
          onClick={onClose}
          className="text-muted-foreground/50 hover:text-foreground p-1 rounded hover:bg-white/[0.06] transition-colors"
        >
          <X className="h-3.5 w-3.5" />
        </button>
      </div>

      <ScrollArea className="flex-1">
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
                          {ALL_STATUSES.map((s) => (
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
                <PropertyRow>
                  <User className="h-3.5 w-3.5 text-muted-foreground/50" />
                  <span className="text-[12px] text-foreground/70">
                    {issue.assignee_name || "Unassigned"}
                  </span>
                </PropertyRow>

                {/* Due date */}
                <PropertyRow>
                  <Clock className="h-3.5 w-3.5 text-muted-foreground/50" />
                  <span className="text-[12px] text-foreground/70">
                    {issue.due_date
                      ? new Date(issue.due_date).toLocaleDateString()
                      : "No due date"}
                  </span>
                </PropertyRow>
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
                                disabled={isAssigned}
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

          {/* ── Project section (placeholder) ────────────────────────────── */}
          <div className="border-t border-white/[0.06]">
            <SectionHeader
              title="Project"
              open={projectOpen}
              onToggle={() => setProjectOpen((v) => !v)}
            />
            {projectOpen && (
              <PropertyRow>
                <LayoutGrid className="h-3.5 w-3.5 text-muted-foreground/40" />
                <span className="text-[12px] text-muted-foreground/40">No project</span>
              </PropertyRow>
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

          {/* ── Activity section ─────────────────────────────────────────── */}
          <div className="border-t border-white/[0.06] mt-3">
            <div className="px-3 py-1.5">
              <span className="text-[11px] uppercase tracking-wider text-muted-foreground/60 font-medium">
                Activity
              </span>
            </div>
            <div className="px-3 pb-3 space-y-1">
              <div className="flex items-center gap-2 text-[10px] text-muted-foreground/50">
                <div className="h-1.5 w-1.5 rounded-full border border-muted-foreground/30 shrink-0" />
                <span>Created {formatRelativeTime(issue.created_at)}</span>
              </div>
              <div className="flex items-center gap-2 text-[10px] text-muted-foreground/50">
                <div className="h-1.5 w-1.5 rounded-full border border-muted-foreground/30 shrink-0" />
                <span>Updated {formatRelativeTime(issue.updated_at)}</span>
              </div>
              {issue.completed_at && (
                <div className="flex items-center gap-2 text-[10px] text-muted-foreground/50">
                  <div className="h-1.5 w-1.5 rounded-full border border-muted-foreground/30 shrink-0" />
                  <span>Completed {formatRelativeTime(issue.completed_at)}</span>
                </div>
              )}
            </div>
          </div>
        </div>
      </ScrollArea>
    </div>
  )
}
