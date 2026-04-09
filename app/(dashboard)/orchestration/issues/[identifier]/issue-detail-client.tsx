"use client"

import { useCallback, useEffect, useRef, useState } from "react"
import { useParams, useRouter } from "next/navigation"
import {
  ArrowLeft,
  Calendar,
  Check,
  ChevronRight,
  ChevronsUpDown,
  Copy,
  FolderKanban,
  Hash,
  Link2,
  Loader2,
  MessageSquare,
  Pencil,
  Play,
  Plus,
  Send,
  Square,
  ThumbsDown,
  ThumbsUp,
  X,
} from "lucide-react"
import { useWorkspace } from "@/hooks/use-workspace"
import { useSession } from "@/hooks/use-auth"
import { useRealtimeEvent } from "@/hooks/use-realtime"
import { getAgentAvatarUrl } from "@/lib/agent-avatar"
import { StatusIcon, statusLabel, statusColor } from "@/components/features/issues/status-icon"
import { MarkdownContent } from "@/components/features/issues/markdown-content"
import { TiptapEditor } from "@/components/features/issues/tiptap-editor"
import { PriorityIcon, priorityLabel } from "@/components/features/issues/priority-icon"
import { LabelBadge } from "@/components/features/issues/label-badge"
import { Button } from "@/components/ui/button"
import { Badge } from "@/components/ui/badge"
import { Separator } from "@/components/ui/separator"
import { Input } from "@/components/ui/input"
import { Textarea } from "@/components/ui/textarea"
import { Popover, PopoverContent, PopoverTrigger } from "@/components/ui/popover"
import {
  Command,
  CommandEmpty,
  CommandGroup,
  CommandInput,
  CommandItem,
  CommandList,
} from "@/components/ui/command"
import { Skeleton } from "@/components/ui/skeleton"
import { ScrollArea } from "@/components/ui/scroll-area"
import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip"
import { cn } from "@/lib/utils"
import { toast } from "sonner"
import type {
  IssueActivity,
  IssueComment,
  IssueLabel,
  IssuePriority,
  IssueRelation,
  Mission,
  MissionStatus,
  Project,
} from "@/lib/types/mission"

// ---------------------------------------------------------------------------
// Constants
// ---------------------------------------------------------------------------

const ALL_STATUSES: { value: MissionStatus; label: string }[] = [
  { value: "BACKLOG", label: "Backlog" },
  { value: "TODO", label: "Todo" },
  { value: "PLANNING", label: "Planning" },
  { value: "IN_PROGRESS", label: "In Progress" },
  { value: "REVIEW", label: "In Review" },
  { value: "COMPLETED", label: "Done" },
  { value: "FAILED", label: "Failed" },
  { value: "CANCELLED", label: "Cancelled" },
]

const ALL_PRIORITIES: IssuePriority[] = ["urgent", "high", "medium", "low", "none"]

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

function relativeTime(dateStr: string): string {
  const now = Date.now()
  const date = new Date(dateStr).getTime()
  const diffSec = Math.floor((now - date) / 1000)
  if (diffSec < 60) return "just now"
  const diffMin = Math.floor(diffSec / 60)
  if (diffMin < 60) return `${diffMin}m ago`
  const diffHours = Math.floor(diffMin / 60)
  if (diffHours < 24) return `${diffHours}h ago`
  const diffDays = Math.floor(diffHours / 24)
  if (diffDays < 30) return `${diffDays}d ago`
  return new Date(dateStr).toLocaleDateString(undefined, {
    month: "short",
    day: "numeric",
    year: "numeric",
  })
}

function formatDate(dateStr: string): string {
  return new Date(dateStr).toLocaleDateString(undefined, {
    month: "short",
    day: "numeric",
    year: "numeric",
  })
}


// ---------------------------------------------------------------------------
// Sidebar property row
// ---------------------------------------------------------------------------

function PropertyRow({
  label,
  children,
}: {
  label: string
  children: React.ReactNode
}) {
  return (
    <div className="flex items-center justify-between gap-3 py-1.5 min-h-[32px]">
      <span className="text-xs text-muted-foreground shrink-0 w-[80px]">
        {label}
      </span>
      <div className="flex-1 flex items-center justify-end min-w-0">
        {children}
      </div>
    </div>
  )
}

// ---------------------------------------------------------------------------
// Main page component
// ---------------------------------------------------------------------------

export function IssueDetailClient() {
  const params = useParams()
  const router = useRouter()
  const identifier = params.identifier as string
  const { workspaceId, loading: wsLoading } = useWorkspace()
  const { data: session } = useSession()

  // Core data
  const [issue, setIssue] = useState<Mission | null>(null)
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)

  // Comments
  const [comments, setComments] = useState<IssueComment[]>([])
  const [loadingComments, setLoadingComments] = useState(false)
  const [newComment, setNewComment] = useState("")
  const [submittingComment, setSubmittingComment] = useState(false)

  // Activity
  const [activities, setActivities] = useState<IssueActivity[]>([])
  const [loadingActivity, setLoadingActivity] = useState(false)

  // Description editing
  const [editingDesc, setEditingDesc] = useState(false)
  const [descDraft, setDescDraft] = useState("")

  // Relations
  const [relations, setRelations] = useState<IssueRelation[]>([])

  // Sidebar data
  const [agents, setAgents] = useState<{ id: string; name: string; slug?: string }[]>([])
  const [allLabels, setAllLabels] = useState<IssueLabel[]>([])
  const [projects, setProjects] = useState<Project[]>([])

  // Editing states
  const [editingTitle, setEditingTitle] = useState(false)
  const [titleDraft, setTitleDraft] = useState("")
  const [saving, setSaving] = useState(false)

  // Popover open states
  const [statusOpen, setStatusOpen] = useState(false)
  const [priorityOpen, setPriorityOpen] = useState(false)
  const [assigneeOpen, setAssigneeOpen] = useState(false)
  const [dueDateOpen, setDueDateOpen] = useState(false)
  const [labelsOpen, setLabelsOpen] = useState(false)
  const [projectOpen, setProjectOpen] = useState(false)

  // Action loading
  const [actionLoading, setActionLoading] = useState(false)

  const commentInputRef = useRef<HTMLTextAreaElement>(null)

  const crewId = issue?.crew_id

  // -----------------------------------------------------------------------
  // Fetch issue
  // -----------------------------------------------------------------------

  const fetchIssue = useCallback(async () => {
    if (!workspaceId) return
    try {
      const res = await fetch(
        `/api/v1/issues/${encodeURIComponent(identifier)}?workspace_id=${encodeURIComponent(workspaceId)}`,
      )
      if (!res.ok) {
        setError(res.status === 404 ? "Issue not found" : "Failed to load issue")
        return
      }
      const data: Mission = await res.json()
      setIssue(data)
      setError(null)
    } catch {
      setError("Failed to load issue")
    } finally {
      setLoading(false)
    }
  }, [workspaceId, identifier])

  const fetchComments = useCallback(async () => {
    if (!crewId || !identifier || !workspaceId) return
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

  const fetchActivity = useCallback(async () => {
    if (!crewId || !identifier || !workspaceId) return
    setLoadingActivity(true)
    try {
      const res = await fetch(
        `/api/v1/crews/${crewId}/issues/${encodeURIComponent(identifier)}/activity?workspace_id=${encodeURIComponent(workspaceId)}`,
      )
      if (res.ok) {
        const data = await res.json()
        setActivities(Array.isArray(data) ? data : [])
      }
    } catch {
      // ignore
    } finally {
      setLoadingActivity(false)
    }
  }, [crewId, identifier, workspaceId])

  const fetchRelations = useCallback(async () => {
    if (!crewId || !identifier || !workspaceId) return
    try {
      const res = await fetch(
        `/api/v1/crews/${crewId}/issues/${encodeURIComponent(identifier)}/relations?workspace_id=${encodeURIComponent(workspaceId)}`,
      )
      if (res.ok) {
        const data = await res.json()
        setRelations(Array.isArray(data) ? data : [])
      }
    } catch {
      // ignore
    }
  }, [crewId, identifier, workspaceId])

  const fetchSidebarData = useCallback(async () => {
    if (!workspaceId) return
    try {
      const [agentsRes, labelsRes, projectsRes] = await Promise.all([
        fetch(`/api/v1/agents?workspace_id=${encodeURIComponent(workspaceId)}`),
        fetch(`/api/v1/labels?workspace_id=${encodeURIComponent(workspaceId)}`),
        fetch(`/api/v1/projects?workspace_id=${encodeURIComponent(workspaceId)}`),
      ])
      if (agentsRes.ok) {
        const data = await agentsRes.json()
        const list = Array.isArray(data) ? data : data.agents ?? []
        setAgents(list.map((a: { id: string; name: string; slug?: string }) => ({
          id: a.id,
          name: a.name,
          slug: a.slug,
        })))
      }
      if (labelsRes.ok) {
        const data = await labelsRes.json()
        setAllLabels(Array.isArray(data) ? data : [])
      }
      if (projectsRes.ok) {
        const data = await projectsRes.json()
        setProjects(Array.isArray(data) ? data : [])
      }
    } catch {
      // ignore
    }
  }, [workspaceId])

  // Initial data load
  useEffect(() => {
    fetchIssue()
  }, [fetchIssue])

  useEffect(() => {
    if (issue) {
      fetchComments()
      fetchActivity()
      fetchRelations()
    }
  // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [issue?.id])

  useEffect(() => {
    fetchSidebarData()
  }, [fetchSidebarData])

  // Realtime updates
  useRealtimeEvent(
    "mission.updated",
    useCallback(() => {
      fetchIssue()
      fetchActivity()
    }, [fetchIssue, fetchActivity]),
  )

  // -----------------------------------------------------------------------
  // Mutations
  // -----------------------------------------------------------------------

  async function patchIssue(body: Record<string, unknown>) {
    if (!crewId || !identifier || !workspaceId) return false
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
      await fetchIssue()
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

  async function handleSubmitComment() {
    if (!crewId || !identifier || !workspaceId || !newComment.trim()) return
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

  async function handleAction(action: "start" | "stop" | "review", reviewAction?: "approve" | "request_changes") {
    if (!crewId || !identifier || !workspaceId) return
    setActionLoading(true)
    try {
      const url = action === "review"
        ? `/api/v1/crews/${crewId}/issues/${encodeURIComponent(identifier)}/review?workspace_id=${encodeURIComponent(workspaceId)}`
        : `/api/v1/crews/${crewId}/issues/${encodeURIComponent(identifier)}/${action}?workspace_id=${encodeURIComponent(workspaceId)}`
      const res = await fetch(url, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: action === "review" ? JSON.stringify({ action: reviewAction }) : undefined,
      })
      if (!res.ok) {
        const b = await res.json().catch(() => null)
        toast.error(b?.detail ?? `Failed to ${action} issue`)
        return
      }
      toast.success(
        action === "start" ? "Issue started" :
        action === "stop" ? "Issue stopped" :
        reviewAction === "approve" ? "Issue approved" : "Changes requested"
      )
      await fetchIssue()
      fetchActivity()
    } catch {
      toast.error(`Failed to ${action} issue`)
    } finally {
      setActionLoading(false)
    }
  }

  async function handleToggleLabel(label: IssueLabel) {
    if (!issue) return
    const currentIds = (issue.labels ?? []).map((l) => l.id)
    const isActive = currentIds.includes(label.id)
    const newIds = isActive
      ? currentIds.filter((id) => id !== label.id)
      : [...currentIds, label.id]
    await patchIssue({ label_ids: newIds })
  }

  function handleCopyUrl() {
    const url = window.location.href
    navigator.clipboard.writeText(url).then(() => {
      toast.success("Link copied to clipboard")
    }).catch(() => {
      toast.error("Failed to copy link")
    })
  }

  // -----------------------------------------------------------------------
  // Loading / Error states
  // -----------------------------------------------------------------------

  if (wsLoading || loading) {
    return (
      <div className="h-full flex flex-col">
        <div className="flex items-center gap-3 px-6 py-4 border-b border-border">
          <Skeleton className="h-6 w-6 rounded" />
          <Skeleton className="h-4 w-48" />
        </div>
        <div className="flex flex-1 overflow-hidden">
          <div className="flex-1 p-8 space-y-6">
            <Skeleton className="h-8 w-3/4" />
            <Skeleton className="h-4 w-full" />
            <Skeleton className="h-4 w-2/3" />
            <Skeleton className="h-32 w-full" />
          </div>
          <div className="w-[320px] border-l border-border p-6 space-y-4">
            {Array.from({ length: 6 }).map((_, i) => (
              <Skeleton key={i} className="h-8 w-full" />
            ))}
          </div>
        </div>
      </div>
    )
  }

  if (error || !issue) {
    return (
      <div className="h-full flex flex-col items-center justify-center gap-4">
        <p className="text-muted-foreground">{error ?? "Issue not found"}</p>
        <Button variant="outline" onClick={() => router.push("/orchestration")}>
          <ArrowLeft className="h-4 w-4 mr-2" />
          Back to Orchestration
        </Button>
      </div>
    )
  }

  // -----------------------------------------------------------------------
  // Derived
  // -----------------------------------------------------------------------

  const canStart = (issue.status === "BACKLOG" || issue.status === "TODO") && issue.assignee_id
  const canStop = issue.status === "IN_PROGRESS"
  const canReview = issue.status === "REVIEW"
  const issueLabelsIds = new Set((issue.labels ?? []).map((l) => l.id))
  const assigneeName = issue.assignee_name ?? "Unassigned"
  const assigneeAgent = agents.find((a) => a.id === issue.assignee_id)
  const currentProject = projects.find((p) => p.id === issue.project_id)

  // -----------------------------------------------------------------------
  // Render
  // -----------------------------------------------------------------------

  return (
    <div className="h-full flex flex-col bg-background">
      {/* ---- Header / Breadcrumb ---- */}
      <div className="flex items-center gap-2 px-4 py-3 border-b border-border shrink-0">
        <Button
          variant="ghost"
          size="icon"
          className="h-7 w-7 text-muted-foreground hover:text-foreground"
          onClick={() => router.push("/orchestration")}
        >
          <ArrowLeft className="h-4 w-4" />
        </Button>

        <nav className="flex items-center gap-1 text-sm text-muted-foreground min-w-0">
          <button
            className="hover:text-foreground transition-colors"
            onClick={() => router.push("/orchestration")}
          >
            Orchestration
          </button>
          <ChevronRight className="h-3 w-3 shrink-0" />
          <span className="text-foreground font-medium truncate">
            {issue.identifier ?? issue.title}
          </span>
        </nav>

        {issue.crew_name && (
          <Badge
            variant="outline"
            className="ml-2 text-[10px] px-1.5 py-0 border-border text-muted-foreground"
          >
            {issue.crew_name}
          </Badge>
        )}

        <div className="ml-auto flex items-center gap-1">
          <Tooltip>
            <TooltipTrigger asChild>
              <Button
                variant="ghost"
                size="icon"
                className="h-7 w-7 text-muted-foreground hover:text-foreground"
                onClick={handleCopyUrl}
              >
                <Link2 className="h-3.5 w-3.5" />
              </Button>
            </TooltipTrigger>
            <TooltipContent>Copy link</TooltipContent>
          </Tooltip>
        </div>
      </div>

      {/* ---- Main 2-column layout ---- */}
      <div className="flex flex-1 overflow-hidden">
        {/* ---- Left: main content ---- */}
        <ScrollArea className="flex-1">
          <div className="max-w-3xl mx-auto px-8 py-8 space-y-8">
            {/* Title */}
            <div>
              {editingTitle ? (
                <div className="flex items-center gap-2">
                  <Input
                    value={titleDraft}
                    onChange={(e) => setTitleDraft(e.target.value)}
                    className="text-xl font-semibold bg-accent border-border h-10"
                    onKeyDown={(e) => {
                      if (e.key === "Enter") handleSaveTitle()
                      if (e.key === "Escape") setEditingTitle(false)
                    }}
                    autoFocus
                  />
                  <Button
                    size="icon"
                    variant="ghost"
                    className="h-8 w-8 shrink-0"
                    onClick={handleSaveTitle}
                    disabled={saving}
                  >
                    {saving ? (
                      <Loader2 className="h-4 w-4 animate-spin" />
                    ) : (
                      <Check className="h-4 w-4" />
                    )}
                  </Button>
                  <Button
                    size="icon"
                    variant="ghost"
                    className="h-8 w-8 shrink-0"
                    onClick={() => setEditingTitle(false)}
                  >
                    <X className="h-4 w-4" />
                  </Button>
                </div>
              ) : (
                <h1
                  className="text-xl font-semibold text-foreground leading-tight cursor-pointer hover:text-foreground/80 transition-colors group flex items-center gap-2"
                  onClick={() => {
                    setTitleDraft(issue.title)
                    setEditingTitle(true)
                  }}
                >
                  {issue.title}
                  <Pencil className="h-3.5 w-3.5 text-muted-foreground/0 group-hover:text-muted-foreground/60 transition-colors" />
                </h1>
              )}
              {issue.identifier && (
                <p className="text-xs text-muted-foreground font-mono mt-1">
                  {issue.identifier}
                </p>
              )}
            </div>

            {/* Description (WYSIWYG Tiptap editor) */}
            <div>
              <TiptapEditor
                content={issue.description || ""}
                onChange={(md) => setDescDraft(md)}
                onBlur={() => {
                  if (descDraft !== (issue.description || "")) {
                    patchIssue({ description: descDraft })
                  }
                }}
                placeholder="Click to add description..."
                editable={true}
              />
            </div>

            <Separator />

            {/* ---- Comments section ---- */}
            <div className="space-y-4">
              <div className="flex items-center gap-2">
                <MessageSquare className="h-4 w-4 text-muted-foreground/60" />
                <h2 className="text-xs font-medium text-muted-foreground uppercase tracking-wider">
                  Comments
                </h2>
                {comments.length > 0 && (
                  <span className="text-xs text-muted-foreground/50">
                    ({comments.length})
                  </span>
                )}
              </div>

              {loadingComments ? (
                <div className="flex items-center justify-center py-6">
                  <Loader2 className="h-4 w-4 animate-spin text-muted-foreground" />
                </div>
              ) : comments.length === 0 ? (
                <p className="text-xs text-muted-foreground/50 py-3">
                  No comments yet. Be the first to comment.
                </p>
              ) : (
                <div className="space-y-4">
                  {comments.map((comment) => (
                    <div key={comment.id} className="flex gap-3">
                      {/* Avatar */}
                      <div className="shrink-0 mt-0.5">
                        {comment.author_type === "agent" ? (
                          <img
                            src={getAgentAvatarUrl(comment.author_name ?? comment.author_id)}
                            alt=""
                            className="h-7 w-7 rounded-full"
                          />
                        ) : (
                          <div className="h-7 w-7 rounded-full bg-primary/10 flex items-center justify-center text-xs font-medium text-primary">
                            {(comment.author_name ?? "U").charAt(0).toUpperCase()}
                          </div>
                        )}
                      </div>

                      {/* Content */}
                      <div className="flex-1 min-w-0">
                        <div className="flex items-baseline gap-2 mb-1">
                          <span className="text-[13px] font-medium text-foreground">
                            {comment.author_name ?? "Unknown"}
                          </span>
                          <span className="text-[11px] text-muted-foreground/50">
                            {relativeTime(comment.created_at)}
                          </span>
                        </div>
                        <div className="mt-1">
                          <MarkdownContent>{comment.body}</MarkdownContent>
                        </div>
                      </div>
                    </div>
                  ))}
                </div>
              )}

              {/* New comment input */}
              <div className="flex gap-3 pt-2">
                <div className="shrink-0 mt-1">
                  <div className="h-7 w-7 rounded-full bg-primary/10 flex items-center justify-center text-xs font-medium text-primary">
                    {(session?.user?.name ?? session?.user?.email ?? "U").charAt(0).toUpperCase()}
                  </div>
                </div>
                <div className="flex-1 space-y-2">
                  <Textarea
                    ref={commentInputRef}
                    value={newComment}
                    onChange={(e) => setNewComment(e.target.value)}
                    placeholder="Write a comment..."
                    className="min-h-[72px] text-[13px] bg-card border-border resize-none"
                    onKeyDown={(e) => {
                      if (e.key === "Enter" && (e.metaKey || e.ctrlKey)) {
                        handleSubmitComment()
                      }
                    }}
                  />
                  <div className="flex items-center justify-between">
                    <span className="text-[11px] text-muted-foreground/40">
                      {(typeof navigator !== "undefined" ? ((navigator as Navigator & { userAgentData?: { platform?: string } }).userAgentData?.platform ?? navigator.platform ?? "") : "").includes("Mac") ? "Cmd" : "Ctrl"}+Enter to send
                    </span>
                    <Button
                      size="sm"
                      className="h-7 text-xs gap-1.5"
                      onClick={handleSubmitComment}
                      disabled={submittingComment || !newComment.trim()}
                    >
                      {submittingComment ? (
                        <Loader2 className="h-3 w-3 animate-spin" />
                      ) : (
                        <Send className="h-3 w-3" />
                      )}
                      Send
                    </Button>
                  </div>
                </div>
              </div>
            </div>

            <Separator />

            {/* ---- Activity section ---- */}
            <div className="space-y-3">
              <h2 className="text-xs font-medium text-muted-foreground uppercase tracking-wider">
                Activity
              </h2>

              {loadingActivity ? (
                <div className="flex items-center justify-center py-4">
                  <Loader2 className="h-4 w-4 animate-spin text-muted-foreground" />
                </div>
              ) : activities.length === 0 ? (
                <p className="text-xs text-muted-foreground/50 py-2">
                  No activity recorded.
                </p>
              ) : (
                <div className="space-y-0">
                  {activities.map((activity, idx) => (
                    <div key={activity.id} className="flex gap-3 relative">
                      {/* Timeline line */}
                      {idx < activities.length - 1 && (
                        <div className="absolute left-[7px] top-[18px] bottom-0 w-px bg-border" />
                      )}

                      {/* Dot */}
                      <div
                        className={cn(
                          "h-[15px] w-[15px] rounded-full border-2 shrink-0 mt-0.5 z-10",
                          activity.action.includes("completed") || activity.action.includes("done")
                            ? "border-green-500 bg-green-500/20"
                            : activity.action.includes("failed")
                              ? "border-red-500 bg-red-500/20"
                              : activity.action.includes("started") || activity.action.includes("progress")
                                ? "border-yellow-500 bg-yellow-500/20"
                                : "border-border bg-muted",
                        )}
                      />

                      {/* Content */}
                      <div className="flex-1 min-w-0 pb-4">
                        <p className="text-xs text-foreground/70 leading-relaxed">
                          <span className="font-medium text-foreground/90">
                            {activity.actor_name ?? "System"}
                          </span>{" "}
                          {activity.action}
                          {activity.details && (
                            <span className="text-muted-foreground/60">
                              {" "}{activity.details}
                            </span>
                          )}
                        </p>
                        <p className="text-[11px] text-muted-foreground/40 mt-0.5">
                          {relativeTime(activity.created_at)}
                        </p>
                      </div>
                    </div>
                  ))}
                </div>
              )}
            </div>

            {/* Footer metadata */}
            <div className="text-[11px] text-muted-foreground/40 font-mono space-y-0.5 pt-4">
              <div>Created {formatDate(issue.created_at)}</div>
              <div>Updated {formatDate(issue.updated_at)}</div>
            </div>
          </div>
        </ScrollArea>

        {/* ---- Right: Sidebar ---- */}
        <div className="w-[320px] border-l border-border bg-card shrink-0 hidden lg:block">
          <div className="sticky top-0 p-5 space-y-5 overflow-y-auto max-h-[calc(100vh-53px)]">
            {/* PROPERTIES header */}
            <h3 className="text-[10px] font-semibold text-muted-foreground/60 uppercase tracking-widest">
              Properties
            </h3>

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
                        {ALL_STATUSES.map((s) => (
                          <CommandItem
                            key={s.value}
                            onSelect={() => {
                              patchIssue({ status: s.value })
                              setStatusOpen(false)
                            }}
                          >
                            <StatusIcon status={s.value} className="mr-2 h-3.5 w-3.5" />
                            <span className="text-xs">{s.label}</span>
                            {issue.status === s.value && (
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
                      <img
                        src={getAgentAvatarUrl(assigneeAgent.slug ?? assigneeAgent.name)}
                        alt=""
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
                            <img
                              src={getAgentAvatarUrl(agent.slug ?? agent.name)}
                              alt=""
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

            <Separator className="bg-border/60" />

            {/* Labels */}
            <div className="space-y-2">
              <div className="flex items-center justify-between">
                <span className="text-[10px] font-semibold text-muted-foreground/60 uppercase tracking-widest">
                  Labels
                </span>
                <Popover open={labelsOpen} onOpenChange={setLabelsOpen}>
                  <PopoverTrigger asChild>
                    <Button
                      variant="ghost"
                      size="icon"
                      className="h-5 w-5 text-muted-foreground/50 hover:text-foreground"
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
              {(issue.labels ?? []).length > 0 ? (
                <div className="flex flex-wrap gap-1">
                  {(issue.labels ?? []).map((label) => (
                    <LabelBadge key={label.id} label={label} />
                  ))}
                </div>
              ) : (
                <p className="text-xs text-muted-foreground/40">None</p>
              )}
            </div>

            <Separator className="bg-border/60" />

            {/* Project */}
            <div className="space-y-2 relative">
              <span className="text-[10px] font-semibold text-muted-foreground/60 uppercase tracking-widest">
                Project
              </span>
              <div className="flex items-center gap-1 group">
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
                              style={{ backgroundColor: project.color }}
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
                    href={`/orchestration/projects/${currentProject.id}`}
                    className="opacity-0 group-hover:opacity-100 p-1 rounded hover:bg-accent text-muted-foreground/30 hover:text-blue-400 transition-all shrink-0"
                    title="Open project"
                  >
                    <svg className="h-3 w-3" viewBox="0 0 16 16" fill="none" stroke="currentColor" strokeWidth="1.5" strokeLinecap="round" strokeLinejoin="round"><path d="M6 2H3a1 1 0 0 0-1 1v10a1 1 0 0 0 1 1h10a1 1 0 0 0 1-1v-3"/><path d="M10 2h4v4"/><path d="M14 2L7 9"/></svg>
                  </a>
                )}
              </div>
            </div>

            <Separator className="bg-border/60" />

            {/* Relations */}
            <div className="space-y-2">
              <span className="text-[10px] font-semibold text-muted-foreground/60 uppercase tracking-widest">
                Relations
              </span>
              {relations.length > 0 ? (
                <div className="space-y-1.5">
                  {relations.map((rel) => (
                    <button
                      key={rel.id}
                      className="flex items-center gap-2 w-full text-left hover:bg-accent rounded px-1.5 py-1 transition-colors"
                      onClick={() => {
                        if (rel.target_identifier) {
                          router.push(`/orchestration/issues/${encodeURIComponent(rel.target_identifier)}`)
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
                <p className="text-xs text-muted-foreground/40">No relations</p>
              )}
            </div>

            <Separator className="bg-border/60" />

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
        </div>
      </div>

      {/* ---- Mobile sidebar (below content on small screens) ---- */}
      <div className="lg:hidden border-t border-border bg-card p-4 shrink-0">
        <div className="flex flex-wrap items-center gap-3">
          <div className="flex items-center gap-1.5">
            <StatusIcon status={issue.status} className="h-3.5 w-3.5" />
            <span className="text-xs">{statusLabel[issue.status] ?? issue.status}</span>
          </div>
          <div className="flex items-center gap-1.5">
            <PriorityIcon priority={issue.priority ?? "none"} className="h-3.5 w-3.5" />
            <span className="text-xs">{priorityLabel[issue.priority ?? "none"]}</span>
          </div>
          <span className="text-xs text-muted-foreground">{assigneeName}</span>
          {(issue.labels ?? []).map((label) => (
            <LabelBadge key={label.id} label={label} />
          ))}
          {canStart && (
            <Button
              size="sm"
              className="h-7 text-xs gap-1"
              onClick={() => handleAction("start")}
              disabled={actionLoading}
            >
              <Play className="h-3 w-3" />
              Start
            </Button>
          )}
          {canStop && (
            <Button
              size="sm"
              variant="outline"
              className="h-7 text-xs gap-1"
              onClick={() => handleAction("stop")}
              disabled={actionLoading}
            >
              <Square className="h-3 w-3" />
              Stop
            </Button>
          )}
          {canReview && (
            <>
              <Button
                size="sm"
                className="h-7 text-xs gap-1"
                onClick={() => handleAction("review", "approve")}
                disabled={actionLoading}
              >
                <ThumbsUp className="h-3 w-3" />
                Approve
              </Button>
              <Button
                size="sm"
                variant="outline"
                className="h-7 text-xs gap-1"
                onClick={() => handleAction("review", "request_changes")}
                disabled={actionLoading}
              >
                <ThumbsDown className="h-3 w-3" />
                Changes
              </Button>
            </>
          )}
        </div>
      </div>
    </div>
  )
}
