"use client"

import { useCallback, useEffect, useState } from "react"
import { motion, AnimatePresence } from "motion/react"
import { Send, Plus, MessageSquare, GitBranch, FolderKanban, Tag } from "lucide-react"
import { Popover, PopoverContent, PopoverTrigger } from "@/components/ui/popover"
import { Command, CommandEmpty, CommandGroup, CommandInput, CommandItem, CommandList } from "@/components/ui/command"
import { LabelBadge } from "@/components/features/issues/label-badge"
import { MarkdownContent } from "@/components/features/issues/markdown-content"
import { StatusIcon, statusLabel } from "@/components/features/issues/status-icon"
import { PriorityIcon, priorityLabel } from "@/components/features/issues/priority-icon"
import { getCrewIconDef } from "@/lib/entities"
import { cn } from "@/lib/utils"
import { LABEL_PRESET_COLORS, STATUS_COLORS } from "@/lib/colors"
import { apiFetch } from "@/lib/api-fetch"
import { toast } from "sonner"
import { AgentAvatar } from "@/components/ui/agent-avatar"
import { ActivityFeed } from "@/components/features/issues/activity-feed"
import { timeAgo } from "@/lib/time"
import type { Mission, IssueLabel, IssueComment, Project, IssueActivity, IssuePriority } from "@/lib/types/mission"
import type { Pipeline } from "@/hooks/use-pipelines"
import { IssueRelationsPanel } from "./issue-relations-panel"
import { IssueRoutineBinder } from "./issue-routine-binder"
import { IssuePropertiesPanel } from "./issue-properties-panel"
import { Card, Pill } from "@/components/features/routines/_shared"

// Visual status → semantic Pill tone mapping. Mirrors what the
// routines detail panel header does so the status badge has the same
// tonal language across the app.
function statusToneFor(status: string | undefined): "emerald" | "rose" | "amber" | "blue" | "violet" | "default" {
  switch (status) {
    case "COMPLETED":
    case "DONE":
      return "emerald"
    case "FAILED":
    case "CANCELLED":
    case "DUPLICATE":
      return "rose"
    case "IN_PROGRESS":
      return "blue"
    case "REVIEW":
      return "violet"
    case "PLANNING":
    case "TODO":
      return "amber"
    default:
      return "default"
  }
}

function priorityToneFor(p: IssuePriority | undefined): "rose" | "amber" | "blue" | "default" {
  switch (p) {
    case "urgent":
      return "rose"
    case "high":
      return "amber"
    case "medium":
    case "low":
      return "blue"
    default:
      return "default"
  }
}

interface IssueDetailInlineProps {
  issue: Mission
  comments: IssueComment[]
  labels: IssueLabel[]
  projects: Project[]
  // Routines available to bind to this issue. Optional: if not
  // supplied, the routine section renders nothing (the host page
  // hasn't loaded pipelines yet, or doesn't expose routine binding).
  routines?: Pipeline[]
  workspaceId: string
  onClose: () => void
  onUpdated: () => void
}

export function IssueDetailInline({
  issue,
  comments,
  labels: workspaceLabels,
  projects,
  routines = [],
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

  // Workflow transition in-flight guard (prevents double-submit on Start/Stop/Approve/Reopen)
  const [isTransitioning, setIsTransitioning] = useState<string | null>(null)

  // Section collapse state
  const [labelsOpen, setLabelsOpen] = useState(true)
  const [projectOpen, setProjectOpen] = useState(true)

  // Popover open state
  const [labelsPopoverOpen, setLabelsPopoverOpen] = useState(false)
  const [projectPopoverOpen, setProjectPopoverOpen] = useState(false)
  const [labelSearch, setLabelSearch] = useState("")
  const [creatingLabel, setCreatingLabel] = useState(false)

  const matchingProject = projects.find((p) => p.id === issue.project_id)

  // Fetch activities
  useEffect(() => {
    if (!issue.crew_id || !issue.identifier) return
    apiFetch(`/api/v1/crews/${issue.crew_id}/issues/${issue.identifier}/activity?workspace_id=${workspaceId}`)
      .then(r => r.ok ? r.json() : [])
      .then(setActivities)
      .catch(() => {})
  }, [issue.crew_id, issue.identifier, workspaceId, issue.updated_at])

  const patchIssue = useCallback(
    async (patch: Record<string, unknown>) => {
      if (!issue.crew_id || !issue.identifier) return
      try {
        const res = await apiFetch(
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
      const res = await apiFetch(
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

  // Assigned labels on this issue
  const issueLabels = issue.labels ?? []

  const statusTone = statusToneFor(issue.status)
  const priorityTone = priorityToneFor(issue.priority)
  const subIssueCount = issue.sub_issues_count ?? 0

  // Workflow actions are status-dependent. Collecting them up here so
  // the hero render is one block, not a stack of inline ternaries.
  const triggerWorkflow = async (action: string, url: string, body?: object, successMsg = "Updated") => {
    if (isTransitioning !== null) return
    setIsTransitioning(action)
    try {
      const res = await apiFetch(url, {
        method: "POST",
        ...(body ? { headers: { "Content-Type": "application/json" }, body: JSON.stringify(body) } : {}),
      })
      if (res.ok) {
        toast.success(successMsg)
        onUpdated()
      } else {
        const e = await res.json().catch(() => null)
        toast.error(e?.detail || `Failed: ${action}`)
      }
    } finally {
      setIsTransitioning(null)
    }
  }
  const qs = `?workspace_id=${encodeURIComponent(workspaceId)}`

  return (
    <div className="flex h-full flex-col overflow-hidden">
      <div className="flex-1 overflow-y-auto">
        {/* ── Hero ───────────────────────────────────────────────────── */}
        <div className="border-b border-border bg-card/40 px-6 pb-5 pt-6">
          {/* Status / priority / identifier pills row */}
          <div className="flex flex-wrap items-center gap-2">
            <Pill tone={statusTone}>
              <StatusIcon status={issue.status} className="h-3 w-3" />
              {statusLabel[issue.status] || issue.status}
            </Pill>
            {issue.priority && issue.priority !== "none" && (
              <Pill tone={priorityTone}>
                <PriorityIcon priority={issue.priority} className="h-3 w-3" />
                {priorityLabel[issue.priority]} priority
              </Pill>
            )}
            {issue.identifier && (
              <span className="inline-flex items-center rounded-md bg-muted px-2 py-0.5 font-mono text-[11px] font-medium text-muted-foreground">
                {issue.identifier}
              </span>
            )}
            {issue.assignee_name && (
              <span className="inline-flex items-center gap-1.5 text-[11px] text-muted-foreground">
                <span className="text-muted-foreground/60">·</span>
                <AgentAvatar
                  seed={issue.assignee_id || issue.assignee_name}
                  className="h-4 w-4 rounded-full"
                />
                <span>{issue.assignee_name}</span>
              </span>
            )}
          </div>

          {/* Title + slug */}
          <div className="mt-3">
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
                className="w-full rounded-md border border-border bg-card px-3 py-2 text-2xl font-semibold tracking-tight text-foreground outline-none focus:border-primary/50"
              />
            ) : (
              <h1
                onClick={() => {
                  setEditingTitle(true)
                  setTitleDraft(issue.title)
                }}
                className="cursor-pointer text-2xl font-semibold leading-tight tracking-tight text-foreground transition-colors hover:text-primary"
                title="Click to edit"
              >
                {issue.title}
              </h1>
            )}
            {issue.description && (
              <div className="mt-3 max-w-3xl text-[14px] leading-relaxed text-foreground/80">
                <MarkdownContent compact>{issue.description}</MarkdownContent>
              </div>
            )}
          </div>

          {/* Workflow action group */}
          <div className="mt-5 flex flex-wrap items-center gap-2">
            {(issue.status === "BACKLOG" || issue.status === "TODO") && issue.assignee_id && (
              <button
                disabled={isTransitioning !== null}
                onClick={() => triggerWorkflow("start", `/api/v1/crews/${issue.crew_id}/issues/${issue.identifier}/start${qs}`, undefined, "Issue started")}
                className="inline-flex h-9 items-center gap-2 rounded-md bg-primary px-4 text-sm font-semibold text-primary-foreground transition-colors hover:brightness-110 disabled:opacity-50"
              >
                <svg className="h-3.5 w-3.5" viewBox="0 0 16 16" fill="currentColor">
                  <path d="M4 2.5v11l9-5.5z" />
                </svg>
                {isTransitioning === "start" ? "Starting…" : "Start work"}
              </button>
            )}
            {issue.status === "IN_PROGRESS" && (
              <button
                disabled={isTransitioning !== null}
                onClick={() => triggerWorkflow("stop", `/api/v1/crews/${issue.crew_id}/issues/${issue.identifier}/stop${qs}`, undefined, "Issue stopped")}
                className="inline-flex h-9 items-center gap-2 rounded-md border border-border bg-card px-4 text-sm font-medium text-foreground transition-colors hover:bg-muted disabled:opacity-50"
              >
                <svg className="h-3.5 w-3.5" viewBox="0 0 16 16" fill="currentColor">
                  <rect x="3" y="3" width="10" height="10" rx="1" />
                </svg>
                {isTransitioning === "stop" ? "Stopping…" : "Stop"}
              </button>
            )}
            {issue.status === "REVIEW" && (
              <>
                <button
                  disabled={isTransitioning !== null}
                  onClick={() => triggerWorkflow("review_approved", `/api/v1/crews/${issue.crew_id}/issues/${issue.identifier}/review${qs}`, { action: "approve" }, "Issue approved")}
                  className="inline-flex h-9 items-center gap-2 rounded-md bg-emerald-500/20 px-4 text-sm font-semibold text-emerald-400 transition-colors hover:bg-emerald-500/30 disabled:opacity-50"
                >
                  ✓ {isTransitioning === "review_approved" ? "Approving…" : "Approve"}
                </button>
                <button
                  onClick={() => setReviewChangesOpen(!reviewChangesOpen)}
                  className="inline-flex h-9 items-center gap-2 rounded-md bg-amber-500/20 px-4 text-sm font-medium text-amber-400 transition-colors hover:bg-amber-500/30"
                >
                  Request changes
                </button>
              </>
            )}
            {(issue.status === "CANCELLED" || issue.status === "DONE") && (
              <button
                disabled={isTransitioning !== null}
                onClick={async () => {
                  if (isTransitioning !== null) return
                  setIsTransitioning("reopen")
                  try {
                    await patchIssue({ status: "BACKLOG" })
                  } finally {
                    setIsTransitioning(null)
                  }
                }}
                className="inline-flex h-9 items-center gap-2 rounded-md border border-border bg-card px-4 text-sm font-medium text-foreground transition-colors hover:bg-muted disabled:opacity-50"
              >
                <svg className="h-3.5 w-3.5" viewBox="0 0 16 16" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round">
                  <path d="M2 8a6 6 0 0 1 10.47-4M14 8a6 6 0 0 1-10.47 4" />
                  <path d="M14 2v4h-4M2 14v-4h4" />
                </svg>
                {isTransitioning === "reopen" ? "Reopening…" : "Reopen"}
              </button>
            )}
          </div>
        </div>

        {/* ── KPI strip ──────────────────────────────────────────────── */}
        <div className="grid grid-cols-2 gap-3 px-6 pt-5 md:grid-cols-4">
          <KpiTile label="Comments" value={comments.length.toString()} sub={comments.length === 0 ? "no replies yet" : `last ${timeAgo(comments[comments.length - 1]?.created_at)}`} Icon={MessageSquare} tone="blue" />
          <KpiTile label="Sub-issues" value={subIssueCount.toString()} sub={subIssueCount === 0 ? "no children" : `linked below`} Icon={GitBranch} tone="violet" />
          <KpiTile label="Project" value={matchingProject?.name?.slice(0, 14) || "—"} sub={matchingProject ? "current home" : "unassigned"} Icon={FolderKanban} tone={matchingProject ? "emerald" : "default"} />
          <KpiTile label="Labels" value={issueLabels.length.toString()} sub={issueLabels.length === 0 ? "no labels" : "applied"} Icon={Tag} tone={issueLabels.length > 0 ? "amber" : "default"} />
        </div>

        {/* Review-changes inline form */}
        <AnimatePresence>
          {reviewChangesOpen && (
            <motion.div
              key="review-changes"
              initial={{ opacity: 0, height: 0 }}
              animate={{ opacity: 1, height: "auto" }}
              exit={{ opacity: 0, height: 0 }}
              className="px-6 pt-3"
            >
              <Card tone="amber" title="Request changes">
                <div className="space-y-2 p-3">
                  <textarea
                    className="h-20 w-full resize-none rounded-md border border-border bg-background p-2.5 text-[13px] outline-none focus:border-primary/50"
                    placeholder="What needs to change…"
                    value={reviewComment}
                    onChange={(e) => setReviewComment(e.target.value)}
                  />
                  <div className="flex justify-end gap-2">
                    <button
                      onClick={() => {
                        setReviewChangesOpen(false)
                        setReviewComment("")
                      }}
                      className="h-8 rounded-md border border-border bg-card px-3 text-xs text-muted-foreground hover:text-foreground"
                    >
                      Cancel
                    </button>
                    <button
                      onClick={async () => {
                        const res = await apiFetch(`/api/v1/crews/${issue.crew_id}/issues/${issue.identifier}/review${qs}`, {
                          method: "POST",
                          headers: { "Content-Type": "application/json" },
                          body: JSON.stringify({ action: "request_changes", comment: reviewComment }),
                        })
                        if (res.ok) {
                          toast.success("Changes requested")
                          setReviewChangesOpen(false)
                          setReviewComment("")
                          onUpdated()
                        } else {
                          const e = await res.json().catch(() => null)
                          toast.error(e?.detail || "Failed")
                        }
                      }}
                      className="h-8 rounded-md bg-amber-500/20 px-3 text-xs font-medium text-amber-400 hover:bg-amber-500/30"
                    >
                      Send
                    </button>
                  </div>
                </div>
              </Card>
            </motion.div>
          )}
        </AnimatePresence>

        {/* ── Two-column body ────────────────────────────────────────── */}
        <div className="grid gap-4 px-6 py-5 lg:grid-cols-[2fr_1fr]">
          {/* Main column: Comments + Activity */}
          <div className="space-y-4">
            {/* Comments */}
            <Card title="Comments" icon={MessageSquare} subtitle={`${comments.length} total`}>
              <div className="space-y-4 p-4">
                {comments.length > 0 ? (
                  <div className="space-y-4">
                    {comments.map((comment) => (
                      <div key={comment.id} className="flex gap-3">
                        {comment.author_type === "agent" && comment.author_id ? (
                          <AgentAvatar
                            seed={comment.author_id}
                            className="mt-0.5 h-7 w-7 shrink-0 rounded-full"
                          />
                        ) : (
                          <div className="mt-0.5 flex h-7 w-7 shrink-0 items-center justify-center rounded-full bg-primary/20">
                            <span className="text-[11px] font-semibold text-primary">
                              {(comment.author_name || comment.author_type || "?")[0].toUpperCase()}
                            </span>
                          </div>
                        )}
                        <div className="min-w-0 flex-1">
                          <div className="flex items-baseline gap-2">
                            <span className="text-sm font-semibold text-foreground/90">
                              {comment.author_name || comment.author_type}
                            </span>
                            <span className="text-[11px] text-muted-foreground/70">
                              {timeAgo(comment.created_at)}
                            </span>
                          </div>
                          <div className="mt-1 text-[13px] leading-relaxed text-foreground/85">
                            <MarkdownContent compact>{comment.body}</MarkdownContent>
                          </div>
                        </div>
                      </div>
                    ))}
                  </div>
                ) : (
                  <p className="text-[13px] text-muted-foreground">No comments yet</p>
                )}

                {/* New comment input */}
                <div className="flex gap-2 border-t border-border/40 pt-3">
                  <textarea
                    value={newComment}
                    onChange={(e) => setNewComment(e.target.value)}
                    placeholder="Write a comment…  (⌘+Enter to submit)"
                    rows={2}
                    className="flex-1 resize-none rounded-md border border-border bg-card px-3 py-2 text-[13px] text-foreground placeholder:text-muted-foreground/50 outline-none focus:border-primary/50"
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
                      "self-end rounded-md p-2 transition-colors",
                      newComment.trim() && !submitting
                        ? "bg-primary text-primary-foreground hover:brightness-110"
                        : "bg-muted text-muted-foreground/40",
                    )}
                  >
                    <Send className="h-4 w-4" />
                  </button>
                </div>
              </div>
            </Card>

            {/* Activity */}
            <Card title="Activity" subtitle={`${activities.length} events`}>
              <div className="p-4">
                <ActivityFeed activities={activities} />
              </div>
            </Card>
          </div>

          {/* Right rail: Properties / Routine / Labels / Project / Relations */}
          <div className="space-y-4">
            {/* Properties Card — reuse the existing panel inside the
                card chrome. The IssuePropertiesPanel still renders its
                own status/priority/assignee/due/estimate/milestone rows. */}
            <Card title="Properties">
              <div className="px-1 py-1">
                <IssuePropertiesPanel issue={issue} workspaceId={workspaceId} patchIssue={patchIssue} />
              </div>
            </Card>

            {/* Routine binding */}
            <Card title="Routine" icon={GitBranch}>
              <div className="px-1 py-1">
                <IssueRoutineBinder issue={issue} routines={routines} workspaceId={workspaceId} patchIssue={patchIssue} onUpdated={onUpdated} />
              </div>
            </Card>

            {/* Labels */}
            <Card
              title="Labels"
              icon={Tag}
              action={
                <Popover open={labelsPopoverOpen} onOpenChange={setLabelsPopoverOpen}>
                  <PopoverTrigger asChild>
                    <button className="rounded p-1 text-muted-foreground/50 transition-colors hover:bg-muted hover:text-foreground">
                      <Plus className="h-3.5 w-3.5" />
                    </button>
                  </PopoverTrigger>
                  <PopoverContent className="w-[220px] p-0" align="end" sideOffset={4}>
                    <Command shouldFilter={true}>
                      <CommandInput placeholder="Search labels..." className="h-8 text-xs" onValueChange={setLabelSearch} />
                      <CommandList>
                        <CommandEmpty>
                          {labelSearch.trim() ? (
                            <button
                              disabled={creatingLabel}
                              className="flex w-full items-center gap-2 px-2 py-1.5 text-xs text-foreground/80 transition-colors hover:bg-white/[0.06]"
                              onClick={async () => {
                                setCreatingLabel(true)
                                try {
                                  const color = LABEL_PRESET_COLORS[Math.floor(Math.random() * LABEL_PRESET_COLORS.length)].value
                                  const res = await apiFetch(`/api/v1/labels?workspace_id=${workspaceId}`, {
                                    method: "POST",
                                    headers: { "Content-Type": "application/json" },
                                    body: JSON.stringify({ name: labelSearch.trim(), color }),
                                  })
                                  if (res.ok) {
                                    const created = await res.json()
                                    const updated = [...(issue.labels || []).map((l) => l.id), created.id]
                                    await patchIssue({ labels: updated })
                                    toast.success(`Label "${created.name}" created`)
                                  } else {
                                    toast.error("Failed to create label")
                                  }
                                } catch {
                                  toast.error("Failed to create label")
                                } finally {
                                  setCreatingLabel(false)
                                  setLabelsPopoverOpen(false)
                                  setLabelSearch("")
                                }
                              }}
                            >
                              <Plus className="h-3 w-3 text-muted-foreground/60" />
                              Create &quot;{labelSearch.trim()}&quot;
                            </button>
                          ) : (
                            <span className="text-muted-foreground/40">No labels found.</span>
                          )}
                        </CommandEmpty>
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
                                    const remaining = (issue.labels || []).filter((l) => l.id !== label.id).map((l) => l.id)
                                    patchIssue({ labels: remaining })
                                  } else {
                                    const updated = [...(issue.labels || []).map((l) => l.id), label.id]
                                    patchIssue({ labels: updated })
                                  }
                                }}
                              >
                                <span className="h-2.5 w-2.5 shrink-0 rounded-full" style={{ backgroundColor: label.color }} />
                                <span className={isAssigned ? "text-muted-foreground/50" : ""}>{label.name}</span>
                                {isAssigned && <span className="ml-auto text-[10px] text-muted-foreground/40">assigned</span>}
                              </CommandItem>
                            )
                          })}
                        </CommandGroup>
                      </CommandList>
                    </Command>
                  </PopoverContent>
                </Popover>
              }
            >
              <div className="p-4">
                {issueLabels.length > 0 ? (
                  <div className="flex flex-wrap gap-1.5">
                    {issueLabels.map((label) => (
                      <LabelBadge key={label.id} label={label} />
                    ))}
                  </div>
                ) : (
                  <span className="text-[13px] text-muted-foreground">No labels assigned</span>
                )}
              </div>
            </Card>

            {/* Project */}
            <Card title="Project" icon={FolderKanban}>
              <div className="p-3">
                <Popover open={projectPopoverOpen} onOpenChange={setProjectPopoverOpen}>
                  <PopoverTrigger asChild>
                    {issue.project_id ? (
                      <button className="flex w-full items-center gap-2.5 rounded-md p-2 text-left transition-colors hover:bg-muted">
                        {(() => {
                          const PIcon = getCrewIconDef(matchingProject?.icon || "folder").icon
                          return <PIcon className="h-4 w-4 shrink-0" style={{ color: matchingProject?.color || "#6B7280" }} />
                        })()}
                        <span className="text-sm text-foreground/85">{matchingProject?.name || "Unknown"}</span>
                      </button>
                    ) : (
                      <button className="flex w-full items-center gap-1.5 rounded-md p-2 text-left text-[13px] text-muted-foreground transition-colors hover:bg-muted hover:text-foreground">
                        <Plus className="h-3.5 w-3.5" />
                        Set project
                      </button>
                    )}
                  </PopoverTrigger>
                  <PopoverContent className="w-[220px] p-1" align="start" sideOffset={4}>
                    {projects.length === 0 ? (
                      <p className="px-2 py-3 text-center text-[11px] text-muted-foreground/40">No projects</p>
                    ) : (
                      <>
                        {issue.project_id && (
                          <button
                            onClick={() => {
                              patchIssue({ project_id: null })
                              setProjectPopoverOpen(false)
                            }}
                            className="flex w-full items-center gap-2 rounded-sm px-2 py-1.5 text-left text-[12px] text-rose-400/80 transition-colors hover:bg-rose-500/10"
                          >
                            <span className="h-3 w-3 inline-block">×</span>
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
                              "flex w-full items-center gap-2 rounded-sm px-2 py-1.5 text-left text-[12px] transition-colors hover:bg-white/[0.06]",
                              p.id === issue.project_id && "bg-white/[0.04]",
                            )}
                          >
                            {(() => {
                              const PIco = getCrewIconDef(p.icon || "folder").icon
                              return <PIco className="h-3.5 w-3.5 shrink-0" style={{ color: p.color }} />
                            })()}
                            <span className="truncate text-foreground/80">{p.name}</span>
                            {p.id === issue.project_id && <span className="ml-auto text-[10px] text-muted-foreground/40">current</span>}
                          </button>
                        ))}
                      </>
                    )}
                  </PopoverContent>
                </Popover>
              </div>
            </Card>

            {/* Relations */}
            <Card title="Relations">
              <div className="px-1 py-1">
                <IssueRelationsPanel issue={issue} workspaceId={workspaceId} />
              </div>
            </Card>
          </div>
        </div>
      </div>
    </div>
  )
}

/* ----------------------------------------------------------------- *
 *  KPI tile — same visual shape as the routine detail / List view  *
 *  tiles so the UI feels like the same dashboard family.            *
 * ----------------------------------------------------------------- */

const KPI_TONE = {
  default: "bg-muted text-muted-foreground",
  emerald: "bg-emerald-500/20 text-emerald-400",
  blue: "bg-blue-500/20 text-blue-400",
  violet: "bg-violet-500/20 text-violet-400",
  rose: "bg-rose-500/20 text-rose-400",
  amber: "bg-amber-500/20 text-amber-400",
} as const

function KpiTile({
  label,
  value,
  sub,
  tone = "default",
  Icon,
}: {
  label: string
  value: string
  sub?: string
  tone?: keyof typeof KPI_TONE
  Icon: typeof MessageSquare
}) {
  return (
    <div className="flex flex-col gap-1 rounded-xl border border-border/60 bg-card px-4 py-4">
      <div className="flex items-center justify-between">
        <div className="text-[11px] font-semibold uppercase tracking-wider text-muted-foreground">{label}</div>
        <div className={cn("flex h-6 w-6 items-center justify-center rounded-md", KPI_TONE[tone])}>
          <Icon className="h-3.5 w-3.5" />
        </div>
      </div>
      <div className="mt-1 truncate text-[28px] font-semibold leading-none tabular-nums sm:text-[32px]">
        {value}
      </div>
      {sub && <div className="mt-1 truncate text-[11px] text-muted-foreground">{sub}</div>}
    </div>
  )
}

// Original ~700-line return body (Properties / Routine / Labels /
// Project / Relations / Comments / Activity rendered flat) was
// replaced by the Stripe-style two-column return above.
