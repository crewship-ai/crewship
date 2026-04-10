"use client"

import { useParams, useRouter } from "next/navigation"
import { useCallback, useEffect, useState } from "react"
import { useWorkspace } from "@/hooks/use-workspace"
import { useSession } from "@/hooks/use-auth"
import { useRealtimeEvent } from "@/hooks/use-realtime"
import { Spinner } from "@/components/ui/spinner"
import { Button } from "@/components/ui/button"
import { Badge } from "@/components/ui/badge"
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
import { ArrowLeft, Send } from "lucide-react"
import { formatDateTime, timeAgo } from "@/lib/time"
import { toast } from "sonner"
import type { Mission, IssueComment, IssuePriority, MissionStatus } from "@/lib/types/mission"

const STATUS_OPTIONS: { value: MissionStatus; label: string; color: string }[] = [
  { value: "BACKLOG", label: "Backlog", color: "bg-slate-400" },
  { value: "TODO", label: "Todo", color: "bg-slate-500" },
  { value: "PLANNING", label: "Planning", color: "bg-indigo-400" },
  { value: "IN_PROGRESS", label: "In Progress", color: "bg-blue-500" },
  { value: "REVIEW", label: "In Review", color: "bg-amber-500" },
  { value: "COMPLETED", label: "Completed", color: "bg-green-500" },
  { value: "DONE", label: "Done", color: "bg-emerald-500" },
  { value: "FAILED", label: "Failed", color: "bg-red-500" },
  { value: "CANCELLED", label: "Cancelled", color: "bg-gray-400" },
  { value: "DUPLICATE", label: "Duplicate", color: "bg-gray-300" },
]

const PRIORITY_OPTIONS: IssuePriority[] = ["urgent", "high", "medium", "low", "none"]

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

  if (sessionStatus === "loading" || loading) {
    return (
      <div className="flex items-center justify-center h-[50vh]">
        <Spinner className="h-6 w-6" />
      </div>
    )
  }

  if (!issue) {
    return (
      <div className="p-6 space-y-4">
        <Button variant="ghost" size="sm" onClick={() => router.push("/issues")}>
          <ArrowLeft className="mr-2 h-4 w-4" />
          Back to Issues
        </Button>
        <div className="text-center py-20">
          <p className="text-muted-foreground">Issue {identifier} not found</p>
        </div>
      </div>
    )
  }

  return (
    <div className="flex flex-col h-full">
      {/* Header */}
      <div className="flex items-center gap-3 px-6 py-4 border-b">
        <Button variant="ghost" size="sm" onClick={() => router.push("/issues")}>
          <ArrowLeft className="h-4 w-4" />
        </Button>
        <Badge variant="outline" className="font-mono text-xs">
          {issue.identifier}
        </Badge>
        <span className="text-sm text-muted-foreground">
          {issue.crew_name || issue.crew_slug}
        </span>
      </div>

      <div className="flex flex-1 overflow-hidden">
        {/* Main content */}
        <ScrollArea className="flex-1 p-6">
          <div className="max-w-3xl space-y-6">
            {/* Title */}
            <h1 className="text-2xl font-semibold">{issue.title}</h1>

            {/* Description */}
            {issue.description && (
              <p className="text-sm text-muted-foreground whitespace-pre-wrap">
                {issue.description}
              </p>
            )}

            <Separator />

            {/* Comments */}
            <div className="space-y-4">
              <h3 className="text-sm font-medium">
                Comments ({comments.length})
              </h3>

              {comments.map((comment) => (
                <div key={comment.id} className="flex gap-3">
                  <div className="h-7 w-7 rounded-full bg-muted flex items-center justify-center text-xs font-medium shrink-0">
                    {(comment.author_name || "?")[0].toUpperCase()}
                  </div>
                  <div className="flex-1 min-w-0">
                    <div className="flex items-center gap-2">
                      <span className="text-sm font-medium">
                        {comment.author_name || comment.author_id}
                      </span>
                      <span className="text-xs text-muted-foreground">
                        {timeAgo(comment.created_at)}
                      </span>
                    </div>
                    <p className="text-sm text-foreground/80 mt-1 whitespace-pre-wrap">
                      {comment.body}
                    </p>
                  </div>
                </div>
              ))}

              {/* New comment */}
              <div className="flex gap-3 pt-2">
                <div className="h-7 w-7 rounded-full bg-primary/10 flex items-center justify-center text-xs font-medium shrink-0">
                  {(session?.user?.name || session?.user?.email || "U").charAt(0).toUpperCase()}
                </div>
                <div className="flex-1 space-y-2">
                  <Textarea
                    placeholder="Leave a comment..."
                    value={newComment}
                    onChange={(e) => setNewComment(e.target.value)}
                    className="min-h-[80px] text-sm"
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

        {/* Sidebar properties */}
        <div className="w-64 border-l p-4 space-y-4 shrink-0 hidden lg:block">
          <div className="space-y-3">
            <div>
              <label className="text-xs text-muted-foreground mb-1 block">Status</label>
              <Select
                value={issue.status}
                onValueChange={(v) => patchIssue("status", v)}
              >
                <SelectTrigger className="h-8 text-xs">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  {STATUS_OPTIONS.map((opt) => (
                    <SelectItem key={opt.value} value={opt.value}>
                      <div className="flex items-center gap-2">
                        <span className={`h-2 w-2 rounded-full ${opt.color}`} />
                        {opt.label}
                      </div>
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </div>

            <div>
              <label className="text-xs text-muted-foreground mb-1 block">Priority</label>
              <Select
                value={issue.priority || "none"}
                onValueChange={(v) => patchIssue("priority", v)}
              >
                <SelectTrigger className="h-8 text-xs">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  {PRIORITY_OPTIONS.map((p) => (
                    <SelectItem key={p} value={p}>
                      <div className="flex items-center gap-2">
                        <PriorityIcon priority={p} className="h-3.5 w-3.5" />
                        {priorityLabel[p]}
                      </div>
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </div>

            {issue.assignee_name && (
              <div>
                <label className="text-xs text-muted-foreground mb-1 block">Assignee</label>
                <p className="text-sm">{issue.assignee_name}</p>
              </div>
            )}

            <div>
              <label className="text-xs text-muted-foreground mb-1 block">Due date</label>
              <input
                type="date"
                className="w-full h-8 text-xs px-2 border rounded-md bg-background"
                value={issue.due_date?.split("T")[0] || ""}
                onChange={(e) => patchIssue("due_date", e.target.value || null)}
              />
            </div>

            {issue.labels && issue.labels.length > 0 && (
              <div>
                <label className="text-xs text-muted-foreground mb-1 block">Labels</label>
                <div className="flex flex-wrap gap-1">
                  {issue.labels.map((label) => (
                    <LabelBadge key={label.id} label={label} />
                  ))}
                </div>
              </div>
            )}
          </div>

          <Separator />

          <div className="text-[11px] text-muted-foreground/60 space-y-1 font-mono">
            <div>Created: {formatDateTime(issue.created_at)}</div>
            <div>Updated: {formatDateTime(issue.updated_at)}</div>
            <div>ID: {issue.id}</div>
          </div>
        </div>
      </div>
    </div>
  )
}
