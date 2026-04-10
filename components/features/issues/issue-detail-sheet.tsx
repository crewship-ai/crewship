"use client"

import { useCallback, useEffect, useState } from "react"
import {
  Calendar,
  Loader2,
  MessageSquare,
  Pencil,
  Save,
  Send,
  Settings2,
  X,
} from "lucide-react"
import {
  Sheet,
  SheetContent,
  SheetDescription,
  SheetHeader,
  SheetTitle,
} from "@/components/ui/sheet"
import { Button } from "@/components/ui/button"
import { Badge } from "@/components/ui/badge"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import { Textarea } from "@/components/ui/textarea"
import { Separator } from "@/components/ui/separator"
import { ScrollArea } from "@/components/ui/scroll-area"
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select"
import { PriorityIcon, priorityLabel } from "./priority-icon"
import { LabelBadge } from "./label-badge"
import { AssigneePicker } from "./assignee-picker"
import type { AssigneeOption } from "./assignee-picker"
import { LabelsDialog } from "./labels-dialog"
import { cn } from "@/lib/utils"
import { toast } from "sonner"
import type {
  IssueComment,
  IssueLabel,
  IssuePriority,
  Mission,
  MissionStatus,
} from "@/lib/types/mission"

interface IssueDetailSheetProps {
  issue: Mission | null
  open: boolean
  onOpenChange: (open: boolean) => void
  labels: IssueLabel[]
  onUpdated: () => void
  workspaceId: string
}

const STATUS_OPTIONS: { value: MissionStatus; label: string; color: string }[] = [
  { value: "BACKLOG", label: "Backlog", color: "text-slate-400" },
  { value: "TODO", label: "Todo", color: "text-slate-500" },
  { value: "IN_PROGRESS", label: "In Progress", color: "text-blue-500" },
  { value: "REVIEW", label: "In Review", color: "text-amber-500" },
  { value: "COMPLETED", label: "Done", color: "text-green-500" },
  { value: "FAILED", label: "Failed", color: "text-red-500" },
  { value: "CANCELLED", label: "Cancelled", color: "text-gray-400" },
]

const STATUS_BADGE_STYLES: Record<string, string> = {
  BACKLOG: "bg-slate-100 text-slate-700 dark:bg-slate-800/40 dark:text-slate-300",
  TODO: "bg-slate-100 text-slate-700 dark:bg-slate-800/40 dark:text-slate-300",
  IN_PROGRESS: "bg-blue-100 text-blue-700 dark:bg-blue-900/40 dark:text-blue-300",
  REVIEW: "bg-amber-100 text-amber-700 dark:bg-amber-900/40 dark:text-amber-300",
  COMPLETED: "bg-green-100 text-green-700 dark:bg-green-900/40 dark:text-green-300",
  FAILED: "bg-red-100 text-red-700 dark:bg-red-900/40 dark:text-red-300",
  CANCELLED: "bg-gray-100 text-gray-700 dark:bg-gray-900/40 dark:text-gray-300",
}

const PRIORITIES: IssuePriority[] = ["urgent", "high", "medium", "low", "none"]

function formatDateTime(dateStr: string): string {
  return new Date(dateStr).toLocaleString(undefined, {
    year: "numeric",
    month: "short",
    day: "numeric",
    hour: "2-digit",
    minute: "2-digit",
  })
}

function formatCommentTime(dateStr: string): string {
  const now = Date.now()
  const date = new Date(dateStr).getTime()
  const diffMin = Math.floor((now - date) / 60000)
  if (diffMin < 1) return "just now"
  if (diffMin < 60) return `${diffMin}m ago`
  const diffHours = Math.floor(diffMin / 60)
  if (diffHours < 24) return `${diffHours}h ago`
  const diffDays = Math.floor(diffHours / 24)
  if (diffDays < 7) return `${diffDays}d ago`
  return new Date(dateStr).toLocaleDateString()
}

export function IssueDetailSheet({
  issue,
  open,
  onOpenChange,
  labels: _labels,
  onUpdated,
  workspaceId,
}: IssueDetailSheetProps) {
  const [editingTitle, setEditingTitle] = useState(false)
  const [titleDraft, setTitleDraft] = useState("")
  const [descDraft, setDescDraft] = useState("")
  const [editingDesc, setEditingDesc] = useState(false)
  const [saving, setSaving] = useState(false)

  // Comments
  const [comments, setComments] = useState<IssueComment[]>([])
  const [loadingComments, setLoadingComments] = useState(false)
  const [newComment, setNewComment] = useState("")
  const [submittingComment, setSubmittingComment] = useState(false)

  // Agents for assignee picker
  const [agents, setAgents] = useState<AssigneeOption[]>([])
  const [labelsDialogOpen, setLabelsDialogOpen] = useState(false)

  const crewId = issue?.crew_id
  const identifier = issue?.identifier

  // Fetch comments when issue changes
  const fetchComments = useCallback(async () => {
    if (!crewId || !identifier) return
    setLoadingComments(true)
    try {
      const res = await fetch(
        `/api/v1/crews/${crewId}/issues/${encodeURIComponent(identifier)}/comments?workspace_id=${encodeURIComponent(workspaceId)}`,
      )
      if (res.ok) {
        const data = await res.json()
        setComments(Array.isArray(data) ? data : [])
      }
    } catch {
      // ignore
    } finally {
      setLoadingComments(false)
    }
  }, [crewId, identifier, workspaceId])

  useEffect(() => {
    if (open && issue) {
      fetchComments()
    }
  // eslint-disable-next-line react-hooks/exhaustive-deps -- re-fetch only when issue ID or open state changes
  }, [open, issue?.id, fetchComments])

  // Fetch agents when issue/crew changes
  useEffect(() => {
    if (!open || !crewId) {
      setAgents([])
      return
    }
    let cancelled = false
    async function fetchAgents() {
      try {
        const res = await fetch(
          `/api/v1/agents?workspace_id=${encodeURIComponent(workspaceId)}&crew_id=${encodeURIComponent(crewId!)}`,
        )
        if (!res.ok || cancelled) return
        const data = await res.json()
        const list = Array.isArray(data) ? data : data.agents ?? []
        if (!cancelled) {
          setAgents(
            list.map((a: { id: string; name: string; slug?: string }) => ({
              id: a.id,
              name: a.name,
              type: "agent" as const,
              slug: a.slug,
            })),
          )
        }
      } catch {
        // ignore
      }
    }
    fetchAgents()
    return () => {
      cancelled = true
    }
  }, [open, crewId, workspaceId])

  // Reset editing state when issue changes
  useEffect(() => {
    setEditingTitle(false)
    setEditingDesc(false)
    setNewComment("")
  }, [issue?.id])

  async function patchIssue(body: Record<string, unknown>) {
    if (!crewId || !identifier) return false
    setSaving(true)
    try {
      const res = await fetch(
        `/api/v1/crews/${crewId}/issues/${encodeURIComponent(identifier)}?workspace_id=${encodeURIComponent(workspaceId)}`,
        {
          method: "PATCH",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify(body),
        },
      )
      if (!res.ok) {
        const b = await res.json().catch(() => null)
        toast.error(b?.detail ?? "Failed to update issue")
        return false
      }
      onUpdated()
      return true
    } catch {
      toast.error("Failed to update issue")
      return false
    } finally {
      setSaving(false)
    }
  }

  async function handleSaveTitle() {
    if (!titleDraft.trim()) return
    const ok = await patchIssue({ title: titleDraft.trim() })
    if (ok) setEditingTitle(false)
  }

  async function handleSaveDescription() {
    const ok = await patchIssue({ description: descDraft.trim() || null })
    if (ok) setEditingDesc(false)
  }

  async function handleStatusChange(status: MissionStatus) {
    await patchIssue({ status })
  }

  async function handlePriorityChange(priority: IssuePriority) {
    await patchIssue({ priority })
  }

  async function handleDueDateChange(date: string) {
    await patchIssue({ due_date: date || null })
  }

  async function handleAssigneeChange(
    type: "user" | "agent" | null,
    id: string | null,
  ) {
    await patchIssue({ assignee_type: type, assignee_id: id })
  }

  async function handleSubmitComment() {
    if (!crewId || !identifier || !newComment.trim()) return
    setSubmittingComment(true)
    try {
      const res = await fetch(
        `/api/v1/crews/${crewId}/issues/${encodeURIComponent(identifier)}/comments?workspace_id=${encodeURIComponent(workspaceId)}`,
        {
          method: "POST",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify({ body: newComment.trim() }),
        },
      )
      if (!res.ok) {
        const b = await res.json().catch(() => null)
        toast.error(b?.detail ?? "Failed to add comment")
        return
      }
      setNewComment("")
      fetchComments()
    } catch {
      toast.error("Failed to add comment")
    } finally {
      setSubmittingComment(false)
    }
  }

  const statusBadge = issue
    ? STATUS_BADGE_STYLES[issue.status] || STATUS_BADGE_STYLES.BACKLOG
    : ""

  return (
    <Sheet open={open} onOpenChange={onOpenChange}>
      <SheetContent className="w-[480px] sm:w-[540px] p-0 bg-card border-border">
        {issue && (
          <>
            <SheetHeader className="px-6 pt-6 pb-4">
              <div className="flex items-start gap-3">
                <div className="min-w-0 flex-1">
                  {editingTitle ? (
                    <div className="flex items-center gap-2">
                      <Input
                        value={titleDraft}
                        onChange={(e) => setTitleDraft(e.target.value)}
                        className="text-base font-semibold bg-accent border-border h-9"
                        onKeyDown={(e) => {
                          if (e.key === "Enter") handleSaveTitle()
                          if (e.key === "Escape") setEditingTitle(false)
                        }}
                        autoFocus
                      />
                      <Button
                        size="icon"
                        variant="ghost"
                        className="h-7 w-7 shrink-0"
                        onClick={handleSaveTitle}
                        disabled={saving}
                      >
                        {saving ? (
                          <Loader2 className="h-3.5 w-3.5 animate-spin" />
                        ) : (
                          <Save className="h-3.5 w-3.5" />
                        )}
                      </Button>
                      <Button
                        size="icon"
                        variant="ghost"
                        className="h-7 w-7 shrink-0"
                        onClick={() => setEditingTitle(false)}
                      >
                        <X className="h-3.5 w-3.5" />
                      </Button>
                    </div>
                  ) : (
                    <SheetTitle
                      className="text-base font-semibold text-foreground leading-tight cursor-pointer hover:text-foreground/80"
                      onClick={() => {
                        setTitleDraft(issue.title)
                        setEditingTitle(true)
                      }}
                    >
                      {issue.title}
                    </SheetTitle>
                  )}
                  <SheetDescription className="mt-1 flex items-center gap-2">
                    {issue.identifier && (
                      <span className="font-mono text-xs">{issue.identifier}</span>
                    )}
                    {issue.crew_name && (
                      <>
                        <span className="text-muted-foreground/30">in</span>
                        <span className="text-xs">{issue.crew_name}</span>
                      </>
                    )}
                  </SheetDescription>
                </div>
                {!editingTitle && (
                  <Button
                    variant="ghost"
                    size="icon"
                    className="h-7 w-7 shrink-0 text-muted-foreground/70 hover:text-foreground/70"
                    onClick={() => {
                      setTitleDraft(issue.title)
                      setEditingTitle(true)
                    }}
                  >
                    <Pencil className="h-3.5 w-3.5" />
                  </Button>
                )}
              </div>
            </SheetHeader>

            <ScrollArea className="h-[calc(100vh-100px)]">
              <div className="px-6 pb-6 space-y-5">
                {/* Status + Priority row */}
                <div className="grid grid-cols-2 gap-4">
                  <div className="space-y-1.5">
                    <Label className="text-xs text-muted-foreground uppercase tracking-wider">
                      Status
                    </Label>
                    <Select
                      value={issue.status}
                      onValueChange={(v) =>
                        handleStatusChange(v as MissionStatus)
                      }
                      disabled={saving}
                    >
                      <SelectTrigger className="h-9">
                        <SelectValue>
                          <Badge
                            variant="outline"
                            className={cn("border-0 text-xs", statusBadge)}
                          >
                            {STATUS_OPTIONS.find((s) => s.value === issue.status)
                              ?.label || issue.status}
                          </Badge>
                        </SelectValue>
                      </SelectTrigger>
                      <SelectContent>
                        {STATUS_OPTIONS.map((opt) => (
                          <SelectItem key={opt.value} value={opt.value}>
                            <span className={opt.color}>{opt.label}</span>
                          </SelectItem>
                        ))}
                      </SelectContent>
                    </Select>
                  </div>

                  <div className="space-y-1.5">
                    <Label className="text-xs text-muted-foreground uppercase tracking-wider">
                      Priority
                    </Label>
                    <Select
                      value={issue.priority || "none"}
                      onValueChange={(v) =>
                        handlePriorityChange(v as IssuePriority)
                      }
                      disabled={saving}
                    >
                      <SelectTrigger className="h-9">
                        <SelectValue>
                          <div className="flex items-center gap-2">
                            <PriorityIcon
                              priority={issue.priority || "none"}
                              className="h-3.5 w-3.5"
                            />
                            <span className="text-sm">
                              {priorityLabel[issue.priority || "none"]}
                            </span>
                          </div>
                        </SelectValue>
                      </SelectTrigger>
                      <SelectContent>
                        {PRIORITIES.map((p) => (
                          <SelectItem key={p} value={p}>
                            <div className="flex items-center gap-2">
                              <PriorityIcon priority={p} className="h-3.5 w-3.5" />
                              <span>{priorityLabel[p]}</span>
                            </div>
                          </SelectItem>
                        ))}
                      </SelectContent>
                    </Select>
                  </div>
                </div>

                {/* Assignee */}
                <div className="space-y-1.5">
                  <Label className="text-xs text-muted-foreground uppercase tracking-wider">
                    Assignee
                  </Label>
                  <AssigneePicker
                    value={{
                      type: issue.assignee_type ?? null,
                      id: issue.assignee_id ?? null,
                    }}
                    onChange={handleAssigneeChange}
                    agents={agents}
                    users={[]}
                    className="w-full"
                  />
                </div>

                {/* Due date */}
                <div className="space-y-1.5">
                  <Label className="text-xs text-muted-foreground uppercase tracking-wider">
                    Due Date
                  </Label>
                  <div className="flex items-center gap-2">
                    <Calendar className="h-4 w-4 text-muted-foreground/60" />
                    <Input
                      type="date"
                      value={issue.due_date?.split("T")[0] || ""}
                      onChange={(e) => handleDueDateChange(e.target.value)}
                      className="h-8 w-[180px] text-sm"
                      disabled={saving}
                    />
                  </div>
                </div>

                {/* Labels */}
                <div className="space-y-1.5">
                  <div className="flex items-center justify-between">
                    <Label className="text-xs text-muted-foreground uppercase tracking-wider">
                      Labels
                    </Label>
                    <Button
                      variant="ghost"
                      size="sm"
                      className="h-6 text-xs text-muted-foreground gap-1"
                      onClick={() => setLabelsDialogOpen(true)}
                    >
                      <Settings2 className="h-3 w-3" />
                      Manage
                    </Button>
                  </div>
                  {issue.labels && issue.labels.length > 0 ? (
                    <div className="flex flex-wrap gap-1.5">
                      {issue.labels.map((label) => (
                        <LabelBadge key={label.id} label={label} />
                      ))}
                    </div>
                  ) : (
                    <p className="text-xs text-muted-foreground/60">No labels</p>
                  )}
                </div>

                <Separator className="bg-border" />

                {/* Description */}
                <div className="space-y-1.5">
                  <div className="flex items-center justify-between">
                    <Label className="text-xs text-muted-foreground uppercase tracking-wider">
                      Description
                    </Label>
                    {!editingDesc && (
                      <Button
                        variant="ghost"
                        size="sm"
                        className="h-6 text-xs text-muted-foreground"
                        onClick={() => {
                          setDescDraft(issue.description || "")
                          setEditingDesc(true)
                        }}
                      >
                        <Pencil className="h-3 w-3 mr-1" />
                        Edit
                      </Button>
                    )}
                  </div>
                  {editingDesc ? (
                    <div className="space-y-2">
                      <Textarea
                        value={descDraft}
                        onChange={(e) => setDescDraft(e.target.value)}
                        placeholder="Add a description..."
                        className="min-h-[100px] bg-accent border-border text-sm"
                        autoFocus
                      />
                      <div className="flex items-center gap-2">
                        <Button
                          size="sm"
                          onClick={handleSaveDescription}
                          disabled={saving}
                          className="h-7 text-xs"
                        >
                          {saving && (
                            <Loader2 className="h-3 w-3 animate-spin mr-1" />
                          )}
                          Save
                        </Button>
                        <Button
                          size="sm"
                          variant="outline"
                          onClick={() => setEditingDesc(false)}
                          className="h-7 text-xs"
                        >
                          Cancel
                        </Button>
                      </div>
                    </div>
                  ) : (
                    <p className="text-sm text-muted-foreground leading-relaxed whitespace-pre-wrap">
                      {issue.description || "No description provided."}
                    </p>
                  )}
                </div>

                <Separator className="bg-border" />

                {/* Comments */}
                <div className="space-y-3">
                  <div className="flex items-center gap-2">
                    <MessageSquare className="h-4 w-4 text-muted-foreground/60" />
                    <Label className="text-xs text-muted-foreground uppercase tracking-wider">
                      Comments
                    </Label>
                    {issue.comment_count != null && issue.comment_count > 0 && (
                      <span className="text-xs text-muted-foreground/60">
                        ({issue.comment_count})
                      </span>
                    )}
                  </div>

                  {/* Comments list */}
                  {loadingComments ? (
                    <div className="flex items-center justify-center py-4">
                      <Loader2 className="h-4 w-4 animate-spin text-muted-foreground" />
                    </div>
                  ) : comments.length === 0 ? (
                    <p className="text-xs text-muted-foreground/60 py-2">
                      No comments yet.
                    </p>
                  ) : (
                    <div className="space-y-3">
                      {comments.map((comment) => (
                        <div
                          key={comment.id}
                          className="rounded-lg border border-border/60 bg-accent/30 p-3"
                        >
                          <div className="flex items-center gap-2 mb-1.5">
                            <span className="text-xs font-medium text-foreground">
                              {comment.author_name || "Unknown"}
                            </span>
                            <Badge
                              variant="outline"
                              className="text-[10px] px-1 py-0 border-border"
                            >
                              {comment.author_type}
                            </Badge>
                            <span className="text-[11px] text-muted-foreground/60 ml-auto">
                              {formatCommentTime(comment.created_at)}
                            </span>
                          </div>
                          <p className="text-sm text-foreground/80 whitespace-pre-wrap leading-relaxed">
                            {comment.body}
                          </p>
                        </div>
                      ))}
                    </div>
                  )}

                  {/* New comment */}
                  <div className="flex items-start gap-2">
                    <Textarea
                      value={newComment}
                      onChange={(e) => setNewComment(e.target.value)}
                      placeholder="Write a comment..."
                      className="min-h-[60px] text-sm bg-accent/30 border-border"
                      onKeyDown={(e) => {
                        if (e.key === "Enter" && (e.metaKey || e.ctrlKey)) {
                          handleSubmitComment()
                        }
                      }}
                    />
                    <Button
                      size="icon"
                      className="h-9 w-9 shrink-0"
                      onClick={handleSubmitComment}
                      disabled={submittingComment || !newComment.trim()}
                    >
                      {submittingComment ? (
                        <Loader2 className="h-3.5 w-3.5 animate-spin" />
                      ) : (
                        <Send className="h-3.5 w-3.5" />
                      )}
                    </Button>
                  </div>
                </div>

                <Separator className="bg-border" />

                {/* Metadata footer */}
                <div className="text-[11px] text-muted-foreground/50 space-y-1 font-mono">
                  {issue.identifier && <div>Identifier: {issue.identifier}</div>}
                  <div>Created: {formatDateTime(issue.created_at)}</div>
                  <div>Updated: {formatDateTime(issue.updated_at)}</div>
                  <div>ID: {issue.id}</div>
                </div>
              </div>
            </ScrollArea>
          </>
        )}
      </SheetContent>

      <LabelsDialog
        open={labelsDialogOpen}
        onOpenChange={setLabelsDialogOpen}
        labels={_labels}
        workspaceId={workspaceId}
        onLabelsChanged={onUpdated}
      />
    </Sheet>
  )
}
