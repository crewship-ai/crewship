"use client"

import { useCallback, useEffect, useRef, useState } from "react"
import { useParams, useRouter } from "next/navigation"
import {
  ArrowLeft,
  Check,
  ChevronRight,
  Link2,
  Loader2,
  MessageSquare,
  Pencil,
  Send,
  X,
} from "lucide-react"
import { useWorkspace } from "@/hooks/use-workspace"
import { useSession } from "@/hooks/use-auth"
import { useRealtimeEvent } from "@/hooks/use-realtime"
import { getAgentAvatarUrl } from "@/lib/agent-avatar"
import { MarkdownContent } from "@/components/features/issues/markdown-content"
import { TiptapEditor } from "@/components/features/issues/tiptap-editor"
import { ActivityFeed } from "@/components/features/issues/activity-feed"
import { IssueSidebar, IssueSidebarMobile } from "@/components/features/orchestration/issue-sidebar"
import { timeAgo, formatDate } from "@/lib/time"
import { Button } from "@/components/ui/button"
import { Badge } from "@/components/ui/badge"
import { Separator } from "@/components/ui/separator"
import { Input } from "@/components/ui/input"
import { Textarea } from "@/components/ui/textarea"
import { Skeleton } from "@/components/ui/skeleton"
import { ScrollArea } from "@/components/ui/scroll-area"
import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip"
import { toast } from "sonner"
import type {
  IssueActivity,
  IssueComment,
  IssueLabel,
  IssueRelation,
  Mission,
  Project,
} from "@/lib/types/mission"

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

function isMac(): boolean {
  if (typeof navigator === "undefined") return false
  const nav = navigator as Navigator & { userAgentData?: { platform?: string } }
  const platform = nav.userAgentData?.platform ?? navigator.platform ?? ""
  return platform.includes("Mac")
}

// ---------------------------------------------------------------------------
// Main page component
// ---------------------------------------------------------------------------

export function IssuePageClient() {
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
  const [descDraft, setDescDraft] = useState("")

  // Sync descDraft when issue description changes (prevents patching empty on blur)
  useEffect(() => {
    if (issue?.description !== undefined) {
      setDescDraft(issue.description || "")
    }
  }, [issue?.description])

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
    // eslint-disable-next-line @typescript-eslint/no-explicit-any
    useCallback((payload: any) => {
      if (payload?.id && payload.id !== issue?.id) return
      fetchIssue()
      fetchActivity()
    }, [fetchIssue, fetchActivity, issue?.id]),
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
          <div className="flex-1 p-6 space-y-6">
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
        <Button variant="outline" onClick={() => router.push("/issues")}>
          <ArrowLeft className="h-4 w-4 mr-2" />
          Back to Orchestration
        </Button>
      </div>
    )
  }

  // -----------------------------------------------------------------------
  // Derived
  // -----------------------------------------------------------------------


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
          onClick={() => router.push("/issues")}
          aria-label="Back to issues"
        >
          <ArrowLeft className="h-4 w-4" />
        </Button>

        <nav className="flex items-center gap-1 text-body text-muted-foreground min-w-0">
          <button
            className="hover:text-foreground transition-colors"
            onClick={() => router.push("/issues")}
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
            className="ml-2 text-micro px-1.5 py-0 border-border text-muted-foreground"
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
                aria-label="Copy link"
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
          <div className="max-w-3xl mx-auto px-6 py-6 space-y-6">
            {/* Title */}
            <div>
              {editingTitle ? (
                <div className="flex items-center gap-2">
                  <Input
                    value={titleDraft}
                    onChange={(e) => setTitleDraft(e.target.value)}
                    className="text-title font-semibold bg-accent border-border h-10"
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
                    aria-label="Save title"
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
                    aria-label="Cancel editing"
                  >
                    <X className="h-4 w-4" />
                  </Button>
                </div>
              ) : (
                <h1
                  className="text-title font-semibold text-foreground leading-tight cursor-pointer hover:text-foreground/80 transition-colors group flex items-center gap-2"
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
                <p className="text-label text-muted-foreground font-mono mt-1">
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
                <h2 className="text-label font-semibold text-muted-foreground uppercase tracking-wider">
                  Comments
                </h2>
                {comments.length > 0 && (
                  <span className="text-label text-muted-foreground/50">
                    ({comments.length})
                  </span>
                )}
              </div>

              {loadingComments ? (
                <div className="flex items-center justify-center py-6">
                  <Loader2 className="h-4 w-4 animate-spin text-muted-foreground" />
                </div>
              ) : comments.length === 0 ? (
                <p className="text-label text-muted-foreground/50 py-3">
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
                          <div className="h-7 w-7 rounded-full bg-primary/10 flex items-center justify-center text-label font-medium text-primary">
                            {(comment.author_name ?? "U").charAt(0).toUpperCase()}
                          </div>
                        )}
                      </div>

                      {/* Content */}
                      <div className="flex-1 min-w-0">
                        <div className="flex items-baseline gap-2 mb-1">
                          <span className="text-body font-medium text-foreground">
                            {comment.author_name ?? "Unknown"}
                          </span>
                          <span className="text-label text-muted-foreground/60">
                            {timeAgo(comment.created_at)}
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
                  <div className="h-7 w-7 rounded-full bg-primary/10 flex items-center justify-center text-label font-medium text-primary">
                    {(session?.user?.name ?? session?.user?.email ?? "U").charAt(0).toUpperCase()}
                  </div>
                </div>
                <div className="flex-1 space-y-2">
                  <Textarea
                    ref={commentInputRef}
                    value={newComment}
                    onChange={(e) => setNewComment(e.target.value)}
                    placeholder="Write a comment..."
                    className="min-h-[72px] text-body bg-card border-border resize-none"
                    onKeyDown={(e) => {
                      if (e.key === "Enter" && (e.metaKey || e.ctrlKey)) {
                        handleSubmitComment()
                      }
                    }}
                  />
                  <div className="flex items-center justify-between">
                    <span className="text-label text-muted-foreground/50">
                      {isMac() ? "Cmd" : "Ctrl"}+Enter to send
                    </span>
                    <Button
                      size="sm"
                      className="h-7 text-label gap-1.5"
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
            {loadingActivity ? (
              <div className="flex items-center justify-center py-4">
                <Loader2 className="h-4 w-4 animate-spin text-muted-foreground" />
              </div>
            ) : (
              <ActivityFeed activities={activities} />
            )}

            {/* Footer metadata */}
            <div className="text-label text-muted-foreground/60 font-mono space-y-0.5 pt-4">
              <div>Created {formatDate(issue.created_at)}</div>
              <div>Updated {formatDate(issue.updated_at)}</div>
            </div>
          </div>
        </ScrollArea>

        {/* ---- Right: Sidebar ---- */}
        <IssueSidebar
          issue={issue}
          agents={agents}
          allLabels={allLabels}
          projects={projects}
          relations={relations}
          patchIssue={patchIssue}
          handleToggleLabel={handleToggleLabel}
          handleAction={handleAction}
          actionLoading={actionLoading}
        />
      </div>

      {/* ---- Mobile sidebar (below content on small screens) ---- */}
      <IssueSidebarMobile
        issue={issue}
        agents={agents}
        allLabels={allLabels}
        projects={projects}
        relations={relations}
        patchIssue={patchIssue}
        handleToggleLabel={handleToggleLabel}
        handleAction={handleAction}
        actionLoading={actionLoading}
      />
    </div>
  )
}
