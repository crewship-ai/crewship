"use client"

import { useCallback, useState } from "react"
import { ScrollArea } from "@/components/ui/scroll-area"
import { Separator } from "@/components/ui/separator"
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select"
import { Search, X, Send, Clock } from "lucide-react"
import { StatusIcon, statusLabel } from "@/components/features/issues/status-icon"
import { PriorityIcon, priorityLabel } from "@/components/features/issues/priority-icon"
import { LabelBadge } from "@/components/features/issues/label-badge"
import { IssuesBoardView } from "@/components/features/issues/issues-board-view"
import { IssuesListView } from "@/components/features/issues/issues-list-view"
import { cn } from "@/lib/utils"
import { toast } from "sonner"
import type { Mission, MissionStatus, IssueLabel, IssueComment, IssuePriority } from "@/lib/types/mission"

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
/*  IssueDetailInline — right panel                                           */
/* -------------------------------------------------------------------------- */

const ALL_STATUSES: MissionStatus[] = [
  "BACKLOG", "TODO", "PLANNING", "IN_PROGRESS", "REVIEW", "COMPLETED", "FAILED", "CANCELLED", "DUPLICATE",
]

const ALL_PRIORITIES: IssuePriority[] = ["urgent", "high", "medium", "low", "none"]

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
  labels: _labels,
  workspaceId,
  onClose,
  onUpdated,
}: IssueDetailInlineProps) {
  const [newComment, setNewComment] = useState("")
  const [submitting, setSubmitting] = useState(false)
  const [editingTitle, setEditingTitle] = useState(false)
  const [titleDraft, setTitleDraft] = useState(issue.title)

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

  return (
    <div className="flex flex-col h-full bg-card">
      {/* Header */}
      <div className="flex items-center gap-2 px-3 py-2 border-b border-white/[0.08] shrink-0">
        <span className="text-[11px] font-mono text-muted-foreground/60 bg-white/[0.04] px-1.5 py-0.5 rounded">
          {issue.identifier || "--"}
        </span>
        <div className="flex-1" />
        <button onClick={onClose} className="text-muted-foreground/50 hover:text-foreground p-1 rounded hover:bg-white/[0.06]">
          <X className="h-3.5 w-3.5" />
        </button>
      </div>

      <ScrollArea className="flex-1">
        <div className="px-3 py-3">
          {/* Title */}
          {editingTitle ? (
            <input
              autoFocus
              value={titleDraft}
              onChange={(e) => setTitleDraft(e.target.value)}
              onBlur={handleTitleSave}
              onKeyDown={(e) => { if (e.key === "Enter") handleTitleSave(); if (e.key === "Escape") { setEditingTitle(false); setTitleDraft(issue.title) } }}
              className="w-full text-sm font-semibold text-foreground bg-white/[0.04] border border-white/[0.1] rounded px-2 py-1 outline-none focus:border-blue-400/50"
            />
          ) : (
            <h3
              onClick={() => { setEditingTitle(true); setTitleDraft(issue.title) }}
              className="text-sm font-semibold text-foreground cursor-pointer hover:text-blue-400 transition-colors"
            >
              {issue.title}
            </h3>
          )}

          {issue.description && (
            <p className="mt-1.5 text-[11px] text-muted-foreground/70 leading-relaxed">
              {issue.description}
            </p>
          )}

          <Separator className="my-3 bg-white/[0.06]" />

          {/* Properties */}
          <div className="space-y-2">
            {/* Status */}
            <div className="flex items-center gap-2">
              <span className="text-[11px] text-muted-foreground/60 w-[80px] shrink-0">Status</span>
              <Select
                value={issue.status}
                onValueChange={(value) => patchIssue({ status: value })}
              >
                <SelectTrigger className="h-7 flex-1 text-[11px] bg-white/[0.03] border-white/[0.08]">
                  <div className="flex items-center gap-1.5">
                    <StatusIcon status={issue.status} className="h-3 w-3" />
                    <SelectValue />
                  </div>
                </SelectTrigger>
                <SelectContent>
                  {ALL_STATUSES.map((s) => (
                    <SelectItem key={s} value={s}>
                      <div className="flex items-center gap-1.5">
                        <StatusIcon status={s} className="h-3.5 w-3.5" />
                        <span>{statusLabel[s] || s}</span>
                      </div>
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </div>

            {/* Priority */}
            <div className="flex items-center gap-2">
              <span className="text-[11px] text-muted-foreground/60 w-[80px] shrink-0">Priority</span>
              <Select
                value={issue.priority || "none"}
                onValueChange={(value) => patchIssue({ priority: value })}
              >
                <SelectTrigger className="h-7 flex-1 text-[11px] bg-white/[0.03] border-white/[0.08]">
                  <div className="flex items-center gap-1.5">
                    <PriorityIcon priority={(issue.priority || "none") as IssuePriority} className="h-3 w-3" />
                    <SelectValue />
                  </div>
                </SelectTrigger>
                <SelectContent>
                  {ALL_PRIORITIES.map((p) => (
                    <SelectItem key={p} value={p}>
                      <div className="flex items-center gap-1.5">
                        <PriorityIcon priority={p} className="h-3.5 w-3.5" />
                        <span>{priorityLabel[p]}</span>
                      </div>
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </div>

            {/* Assignee */}
            <div className="flex items-center gap-2">
              <span className="text-[11px] text-muted-foreground/60 w-[80px] shrink-0">Assignee</span>
              <span className="text-[11px] text-foreground/70">
                {issue.assignee_name || "Unassigned"}
              </span>
            </div>

            {/* Crew */}
            <div className="flex items-center gap-2">
              <span className="text-[11px] text-muted-foreground/60 w-[80px] shrink-0">Crew</span>
              <span className="text-[11px] text-foreground/70">
                {issue.crew_name || issue.crew_slug || "--"}
              </span>
            </div>

            {/* Labels */}
            <div className="flex items-start gap-2">
              <span className="text-[11px] text-muted-foreground/60 w-[80px] shrink-0 pt-0.5">Labels</span>
              <div className="flex flex-wrap gap-1">
                {issue.labels && issue.labels.length > 0 ? (
                  issue.labels.map((label) => (
                    <LabelBadge key={label.id} label={label} />
                  ))
                ) : (
                  <span className="text-[11px] text-muted-foreground/40">None</span>
                )}
              </div>
            </div>

            {/* Due date */}
            <div className="flex items-center gap-2">
              <span className="text-[11px] text-muted-foreground/60 w-[80px] shrink-0">Due date</span>
              <span className="text-[11px] text-foreground/70">
                {issue.due_date ? new Date(issue.due_date).toLocaleDateString() : "Not set"}
              </span>
            </div>
          </div>

          <Separator className="my-3 bg-white/[0.06]" />

          {/* Comments */}
          <div>
            <span className="text-[11px] font-semibold text-muted-foreground/70 uppercase tracking-wider">
              Comments
            </span>
            {comments.length > 0 ? (
              <div className="mt-2 space-y-2.5">
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
                        <span className="text-[10px] text-muted-foreground/40 flex items-center gap-0.5">
                          <Clock className="h-2.5 w-2.5" />
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
              <p className="mt-2 text-[11px] text-muted-foreground/40">No comments yet</p>
            )}

            {/* New comment */}
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

          <Separator className="my-3 bg-white/[0.06]" />

          {/* Activity / timestamps */}
          <div>
            <span className="text-[11px] font-semibold text-muted-foreground/70 uppercase tracking-wider">
              Activity
            </span>
            <div className="mt-2 space-y-1.5">
              <div className="flex items-center gap-2 text-[10px] text-muted-foreground/50">
                <Clock className="h-3 w-3" />
                <span>Created {formatRelativeTime(issue.created_at)}</span>
              </div>
              <div className="flex items-center gap-2 text-[10px] text-muted-foreground/50">
                <Clock className="h-3 w-3" />
                <span>Updated {formatRelativeTime(issue.updated_at)}</span>
              </div>
              {issue.completed_at && (
                <div className="flex items-center gap-2 text-[10px] text-muted-foreground/50">
                  <Clock className="h-3 w-3" />
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
