"use client"

import { useParams, useRouter } from "next/navigation"
import { useCallback, useEffect, useState } from "react"
import { ArrowLeft, Calendar, Flag, Send, Tag, User } from "lucide-react"

import { useWorkspace } from "@/hooks/use-workspace"
import { useSession } from "@/hooks/use-auth"
import { useRealtimeEvent } from "@/hooks/use-realtime"
import { PageHeader } from "@/components/layout/page-header"
import { PropertyRow } from "@/components/layout/property-row"
import { EmptyState } from "@/components/layout/empty-state"
import { SectionCard } from "@/components/ui/section-card"
import { StatusBadge } from "@/components/ui/status-badge"
import { Card, CardContent } from "@/components/ui/card"
import { Button } from "@/components/ui/button"
import { Skeleton } from "@/components/ui/skeleton"
import { Separator } from "@/components/ui/separator"
import { ScrollArea } from "@/components/ui/scroll-area"
import { Textarea } from "@/components/ui/textarea"
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select"
import { PriorityIcon, priorityLabel } from "@/components/features/issues/priority-icon"
import { LabelBadge } from "@/components/features/issues/label-badge"
import { formatDateTime, timeAgo } from "@/lib/time"
import { toast } from "sonner"
import type {
  IssueComment,
  IssuePriority,
  Mission,
  MissionStatus,
} from "@/lib/types/mission"

// ---------------------------------------------------------------------------
// Constants
// ---------------------------------------------------------------------------

const STATUS_OPTIONS: { value: MissionStatus; label: string }[] = [
  { value: "BACKLOG", label: "Backlog" },
  { value: "TODO", label: "Todo" },
  { value: "PLANNING", label: "Planning" },
  { value: "IN_PROGRESS", label: "In Progress" },
  { value: "REVIEW", label: "In Review" },
  { value: "COMPLETED", label: "Completed" },
  { value: "DONE", label: "Done" },
  { value: "FAILED", label: "Failed" },
  { value: "CANCELLED", label: "Cancelled" },
  { value: "DUPLICATE", label: "Duplicate" },
]

const PRIORITY_OPTIONS: IssuePriority[] = ["urgent", "high", "medium", "low", "none"]

// ---------------------------------------------------------------------------
// Main page component
// ---------------------------------------------------------------------------

export function IssuePageClient() {
  const params = useParams()
  const router = useRouter()
  const { data: session, status: sessionStatus } = useSession()
  const { workspaceId } = useWorkspace()
  const identifier = params.identifier as string

  const [issue, setIssue] = useState<Mission | null>(null)
  const [comments, setComments] = useState<IssueComment[]>([])
  const [loading, setLoading] = useState(true)
  const [newComment, setNewComment] = useState("")
  const [sendingComment, setSendingComment] = useState(false)

  const fetchIssue = useCallback(async () => {
    if (!workspaceId || !identifier) return
    try {
      const res = await fetch(
        `/api/v1/issues/${encodeURIComponent(identifier)}?workspace_id=${encodeURIComponent(workspaceId)}`
      )
      if (!res.ok) return
      const found: Mission = await res.json()
      if (found) {
        setIssue(found)
        if (found.crew_id && found.identifier) {
          const commentsRes = await fetch(
            `/api/v1/crews/${encodeURIComponent(found.crew_id)}/issues/${encodeURIComponent(found.identifier)}/comments?workspace_id=${encodeURIComponent(workspaceId)}`
          )
          if (commentsRes.ok) {
            setComments(await commentsRes.json())
          }
        }
      }
    } catch {
      // ignore
    } finally {
      setLoading(false)
    }
  }, [workspaceId, identifier])

  useEffect(() => {
    if (sessionStatus === "authenticated") fetchIssue()
  }, [sessionStatus, fetchIssue])

  useRealtimeEvent(
    "mission.updated",
    useCallback(() => {
      fetchIssue()
    }, [fetchIssue])
  )

  const patchIssue = async (field: string, value: string | null) => {
    if (!issue || !workspaceId) return
    const res = await fetch(
      `/api/v1/crews/${encodeURIComponent(issue.crew_id)}/issues/${encodeURIComponent(issue.identifier!)}?workspace_id=${encodeURIComponent(workspaceId)}`,
      {
        method: "PATCH",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ [field]: value }),
      }
    )
    if (res.ok) {
      fetchIssue()
    } else {
      const err = await res.json().catch(() => null)
      toast.error(err?.detail || "Failed to update")
    }
  }

  const submitComment = async () => {
    if (!issue || !workspaceId || !newComment.trim()) return
    setSendingComment(true)
    try {
      const res = await fetch(
        `/api/v1/crews/${encodeURIComponent(issue.crew_id)}/issues/${encodeURIComponent(issue.identifier!)}/comments?workspace_id=${encodeURIComponent(workspaceId)}`,
        {
          method: "POST",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify({ body: newComment.trim() }),
        }
      )
      if (res.ok) {
        setNewComment("")
        fetchIssue()
      }
    } finally {
      setSendingComment(false)
    }
  }

  // -------------------------------------------------------------------------
  // Loading / empty states
  // -------------------------------------------------------------------------

  if (sessionStatus === "loading" || loading) {
    return (
      <div className="flex flex-col gap-6 p-6">
        <Skeleton className="h-8 w-64" />
        <div className="grid grid-cols-1 lg:grid-cols-[1fr_320px] gap-6">
          <Card>
            <CardContent className="space-y-4 py-6">
              <Skeleton className="h-6 w-3/4" />
              <Skeleton className="h-4 w-full" />
              <Skeleton className="h-4 w-5/6" />
              <Skeleton className="h-32 w-full" />
            </CardContent>
          </Card>
          <Card>
            <CardContent className="space-y-3 py-6">
              {Array.from({ length: 6 }).map((_, i) => (
                <Skeleton key={i} className="h-6 w-full" />
              ))}
            </CardContent>
          </Card>
        </div>
      </div>
    )
  }

  if (!issue) {
    return (
      <div className="flex flex-col gap-6 p-6">
        <Button
          variant="ghost"
          size="sm"
          onClick={() => router.push("/orchestration")}
          className="self-start"
        >
          <ArrowLeft className="mr-2 h-4 w-4" />
          Back to Issues
        </Button>
        <EmptyState
          icon={Flag}
          title="Issue not found"
          description={`No issue matches ${identifier}. It may have been deleted or moved.`}
        />
      </div>
    )
  }

  const assigneeDisplay = issue.assignee_name ?? "Unassigned"
  const currentPriority = (issue.priority ?? "none") as IssuePriority

  // -------------------------------------------------------------------------
  // Render
  // -------------------------------------------------------------------------

  return (
    <div className="flex h-full flex-col">
      <div className="flex flex-col gap-6 p-6">
        <PageHeader
          title={issue.title}
          description={`${issue.identifier ?? ""}${issue.crew_name ? ` · ${issue.crew_name}` : ""}`}
        >
          <Button
            variant="ghost"
            size="sm"
            onClick={() => router.push("/orchestration")}
          >
            <ArrowLeft className="mr-2 h-4 w-4" />
            Back to Issues
          </Button>
        </PageHeader>

        <div className="grid grid-cols-1 gap-6 lg:grid-cols-[1fr_320px]">
          {/* ---- Left column: body + comments ---- */}
          <Card>
            <CardContent className="py-6">
              <ScrollArea className="max-h-[calc(100vh-14rem)] pr-2">
                <div className="space-y-6">
                  {issue.description ? (
                    <p className="whitespace-pre-wrap text-body text-foreground/90">
                      {issue.description}
                    </p>
                  ) : (
                    <p className="text-body text-muted-foreground italic">
                      No description provided.
                    </p>
                  )}

                  <Separator />

                  <div className="space-y-4">
                    <h3 className="text-label font-semibold text-muted-foreground uppercase tracking-wider">
                      Comments ({comments.length})
                    </h3>

                    {comments.length === 0 ? (
                      <p className="text-body text-muted-foreground">
                        No comments yet. Be the first to comment.
                      </p>
                    ) : (
                      comments.map((comment) => (
                        <div key={comment.id} className="flex gap-3">
                          <div className="flex h-7 w-7 shrink-0 items-center justify-center rounded-full bg-muted text-label font-medium">
                            {(comment.author_name || "?")[0].toUpperCase()}
                          </div>
                          <div className="min-w-0 flex-1">
                            <div className="flex items-center gap-2">
                              <span className="text-body font-medium">
                                {comment.author_name || comment.author_id}
                              </span>
                              <span className="text-label text-muted-foreground">
                                {timeAgo(comment.created_at)}
                              </span>
                            </div>
                            <p className="mt-1 whitespace-pre-wrap text-body text-foreground/80">
                              {comment.body}
                            </p>
                          </div>
                        </div>
                      ))
                    )}

                    <div className="flex gap-3 pt-2">
                      <div className="flex h-7 w-7 shrink-0 items-center justify-center rounded-full bg-primary/10 text-label font-medium">
                        {(session?.user?.name || session?.user?.email || "U")
                          .charAt(0)
                          .toUpperCase()}
                      </div>
                      <div className="flex-1 space-y-2">
                        <Textarea
                          placeholder="Leave a comment..."
                          value={newComment}
                          onChange={(e) => setNewComment(e.target.value)}
                          className="min-h-[80px] text-body"
                          onKeyDown={(e) => {
                            if (e.key === "Enter" && (e.metaKey || e.ctrlKey)) {
                              submitComment()
                            }
                          }}
                        />
                        <div className="flex justify-end">
                          <Button
                            size="sm"
                            onClick={submitComment}
                            disabled={!newComment.trim() || sendingComment}
                          >
                            <Send className="mr-2 h-3.5 w-3.5" />
                            Comment
                          </Button>
                        </div>
                      </div>
                    </div>
                  </div>
                </div>
              </ScrollArea>
            </CardContent>
          </Card>

          {/* ---- Right column: metadata sidebar ---- */}
          <SectionCard surface="subtle" title="Details">
            <div className="flex flex-col">
              <PropertyRow label="Status" icon={Flag}>
                <Select
                  value={issue.status}
                  onValueChange={(v) => patchIssue("status", v)}
                >
                  <SelectTrigger className="h-8 w-full border-0 bg-transparent px-2 text-body shadow-none focus:ring-0">
                    <SelectValue>
                      <StatusBadge status={issue.status} withDot />
                    </SelectValue>
                  </SelectTrigger>
                  <SelectContent>
                    {STATUS_OPTIONS.map((opt) => (
                      <SelectItem key={opt.value} value={opt.value}>
                        <StatusBadge status={opt.value} label={opt.label} withDot />
                      </SelectItem>
                    ))}
                  </SelectContent>
                </Select>
              </PropertyRow>

              <PropertyRow label="Priority" icon={Flag}>
                <Select
                  value={currentPriority}
                  onValueChange={(v) => patchIssue("priority", v)}
                >
                  <SelectTrigger className="h-8 w-full border-0 bg-transparent px-2 text-body shadow-none focus:ring-0">
                    <SelectValue>
                      <span className="inline-flex items-center gap-2">
                        <PriorityIcon priority={currentPriority} className="h-3.5 w-3.5" />
                        {priorityLabel[currentPriority]}
                      </span>
                    </SelectValue>
                  </SelectTrigger>
                  <SelectContent>
                    {PRIORITY_OPTIONS.map((p) => (
                      <SelectItem key={p} value={p}>
                        <span className="inline-flex items-center gap-2">
                          <PriorityIcon priority={p} className="h-3.5 w-3.5" />
                          {priorityLabel[p]}
                        </span>
                      </SelectItem>
                    ))}
                  </SelectContent>
                </Select>
              </PropertyRow>

              <PropertyRow label="Assignee" icon={User}>
                <span className={issue.assignee_name ? "text-foreground" : "text-muted-foreground"}>
                  {assigneeDisplay}
                </span>
              </PropertyRow>

              <PropertyRow label="Due date" icon={Calendar}>
                <input
                  type="date"
                  className="h-8 w-full rounded-md border border-border bg-background px-2 text-body"
                  value={issue.due_date?.split("T")[0] || ""}
                  onChange={(e) => patchIssue("due_date", e.target.value || null)}
                />
              </PropertyRow>

              {issue.labels && issue.labels.length > 0 && (
                <PropertyRow label="Labels" icon={Tag}>
                  <div className="flex flex-wrap gap-1">
                    {issue.labels.map((label) => (
                      <LabelBadge key={label.id} label={label} />
                    ))}
                  </div>
                </PropertyRow>
              )}

              <PropertyRow label="Created">
                <span className="text-muted-foreground">
                  {formatDateTime(issue.created_at)}
                </span>
              </PropertyRow>

              <PropertyRow label="Updated">
                <span className="text-muted-foreground">
                  {formatDateTime(issue.updated_at)}
                </span>
              </PropertyRow>
            </div>
          </SectionCard>
        </div>
      </div>
    </div>
  )
}
