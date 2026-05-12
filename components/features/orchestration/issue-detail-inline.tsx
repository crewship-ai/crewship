"use client"

import { useCallback, useEffect, useState } from "react"
import { motion, AnimatePresence } from "motion/react"
import { X, Send, Plus } from "lucide-react"
import { Popover, PopoverContent, PopoverTrigger } from "@/components/ui/popover"
import { Command, CommandEmpty, CommandGroup, CommandInput, CommandItem, CommandList } from "@/components/ui/command"
import { LabelBadge } from "@/components/features/issues/label-badge"
import { MarkdownContent } from "@/components/features/issues/markdown-content"
import { getCrewIconDef } from "@/lib/entities"
import { cn } from "@/lib/utils"
import { LABEL_PRESET_COLORS, STATUS_COLORS } from "@/lib/colors"
import { toast } from "sonner"
import { getAgentAvatarUrl } from "@/lib/agent-avatar"
import { SectionHeader } from "@/components/features/issues/property-row"
import { ActivityFeed } from "@/components/features/issues/activity-feed"
import { timeAgo } from "@/lib/time"
import type { Mission, IssueLabel, IssueComment, Project, IssueActivity } from "@/lib/types/mission"
import type { Pipeline } from "@/hooks/use-pipelines"
import { IssueRelationsPanel } from "./issue-relations-panel"
import { IssueRoutineBinder } from "./issue-routine-binder"
import { IssuePropertiesPanel } from "./issue-properties-panel"

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
    fetch(`/api/v1/crews/${issue.crew_id}/issues/${issue.identifier}/activity?workspace_id=${workspaceId}`)
      .then(r => r.ok ? r.json() : [])
      .then(setActivities)
      .catch(() => {})
  }, [issue.crew_id, issue.identifier, workspaceId, issue.updated_at])

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

  // Assigned labels on this issue
  const issueLabels = issue.labels ?? []

  return (
    <div className="flex flex-col h-full bg-card overflow-hidden">
      {/* ── Header: identifier badge + actions + close ──────────────────── */}
      <div className="flex items-center gap-1.5 px-3 py-2 border-b border-white/[0.06] shrink-0">
        <span className="text-[11px] font-mono text-muted-foreground/70 bg-white/[0.06] px-1.5 py-0.5 rounded">
          {issue.identifier || "--"}
        </span>

        {/* Workflow action buttons */}
        {(issue.status === "BACKLOG" || issue.status === "TODO") && issue.assignee_id && (
          <button
            disabled={isTransitioning !== null}
            onClick={async () => {
              if (isTransitioning !== null) return
              setIsTransitioning("start")
              try {
                const qs = `?workspace_id=${encodeURIComponent(workspaceId)}`
                const res = await fetch(`/api/v1/crews/${issue.crew_id}/issues/${issue.identifier}/start${qs}`, { method: "POST" })
                if (res.ok) { toast.success("Issue started"); onUpdated() }
                else { const e = await res.json().catch(() => null); toast.error(e?.detail || "Failed to start") }
              } finally {
                setIsTransitioning(null)
              }
            }}
            className="flex items-center gap-1 h-6 px-2.5 rounded-md text-[11px] font-medium transition-colors disabled:opacity-50 disabled:cursor-not-allowed"
            style={{ backgroundColor: `${STATUS_COLORS.IN_PROGRESS}18`, color: STATUS_COLORS.IN_PROGRESS }}
          >
            <svg className="h-2.5 w-2.5" viewBox="0 0 16 16" fill="currentColor"><path d="M4 2.5v11l9-5.5z"/></svg>
            Start
          </button>
        )}
        {issue.status === "IN_PROGRESS" && (
          <button
            disabled={isTransitioning !== null}
            onClick={async () => {
              if (isTransitioning !== null) return
              setIsTransitioning("stop")
              try {
                const qs = `?workspace_id=${encodeURIComponent(workspaceId)}`
                const res = await fetch(`/api/v1/crews/${issue.crew_id}/issues/${issue.identifier}/stop${qs}`, { method: "POST" })
                if (res.ok) { toast.success("Issue stopped"); onUpdated() }
                else { const e = await res.json().catch(() => null); toast.error(e?.detail || "Failed to stop") }
              } finally {
                setIsTransitioning(null)
              }
            }}
            className="flex items-center gap-1 h-6 px-2.5 rounded-md text-[11px] font-medium transition-colors disabled:opacity-50 disabled:cursor-not-allowed"
            style={{ backgroundColor: `${STATUS_COLORS.FAILED}18`, color: STATUS_COLORS.FAILED }}
          >
            <svg className="h-2.5 w-2.5" viewBox="0 0 16 16" fill="currentColor"><rect x="3" y="3" width="10" height="10" rx="1"/></svg>
            Stop
          </button>
        )}
        {issue.status === "REVIEW" && (
          <>
            <button
              disabled={isTransitioning !== null}
              onClick={async () => {
                if (isTransitioning !== null) return
                setIsTransitioning("review_approved")
                try {
                  const qs = `?workspace_id=${encodeURIComponent(workspaceId)}`
                  const res = await fetch(`/api/v1/crews/${issue.crew_id}/issues/${issue.identifier}/review${qs}`, {
                    method: "POST", headers: { "Content-Type": "application/json" },
                    body: JSON.stringify({ action: "approve" }),
                  })
                  if (res.ok) { toast.success("Issue approved"); onUpdated() }
                  else { const e = await res.json().catch(() => null); toast.error(e?.detail || "Failed") }
                } finally {
                  setIsTransitioning(null)
                }
              }}
              className="flex items-center gap-1 h-6 px-2.5 rounded-md text-[11px] font-medium transition-colors disabled:opacity-50 disabled:cursor-not-allowed"
              style={{ backgroundColor: `${STATUS_COLORS.COMPLETED}18`, color: STATUS_COLORS.COMPLETED }}
            >
              &#10003; Approve
            </button>
            <button
              onClick={() => setReviewChangesOpen(!reviewChangesOpen)}
              className="flex items-center gap-1 h-6 px-2.5 rounded-md text-[11px] font-medium transition-colors"
              style={{ backgroundColor: `${STATUS_COLORS.BLOCKED}18`, color: STATUS_COLORS.BLOCKED }}
            >
              Changes
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
            className="flex items-center gap-1 h-6 px-2.5 rounded-md text-[11px] font-medium transition-colors disabled:opacity-50 disabled:cursor-not-allowed"
            style={{ backgroundColor: `${STATUS_COLORS.IN_PROGRESS}18`, color: STATUS_COLORS.IN_PROGRESS }}
          >
            <svg className="h-2.5 w-2.5" viewBox="0 0 16 16" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round"><path d="M2 8a6 6 0 0 1 10.47-4M14 8a6 6 0 0 1-10.47 4"/><path d="M14 2v4h-4M2 14v-4h4"/></svg>
            Reopen
          </button>
        )}

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
              <div className="mt-1.5">
                <div className="line-clamp-4 overflow-hidden">
                  <MarkdownContent compact>{issue.description}</MarkdownContent>
                </div>
                {issue.description.length > 200 && issue.identifier && (
                  <a
                    href={`/orchestration/issues/${issue.identifier}`}
                    className="text-[11px] text-blue-400 hover:text-blue-300 mt-1 inline-block"
                  >
                    Show full issue →
                  </a>
                )}
              </div>
            )}
          </div>

          {/* Review changes modal (triggered from header) */}
          <AnimatePresence>
          {reviewChangesOpen && (
            <motion.div key="review-changes" initial={{ opacity: 0, height: 0 }} animate={{ opacity: 1, height: "auto" }} exit={{ opacity: 0, height: 0 }} className="px-3 py-1.5">
              <div className="border border-white/[0.06] rounded-md p-2 space-y-1.5">
                <textarea
                  className="w-full h-14 bg-transparent border border-white/[0.08] rounded px-2 py-1.5 text-[11px] text-foreground outline-none resize-none"
                  placeholder="What needs to change..."
                  value={reviewComment}
                  onChange={(e) => setReviewComment(e.target.value)}
                />
                <div className="flex gap-1.5">
                  <button
                    onClick={() => { setReviewChangesOpen(false); setReviewComment("") }}
                    className="flex-1 h-6 rounded text-[11px] text-muted-foreground hover:text-foreground border border-white/[0.06]"
                  >Cancel</button>
                  <button
                    onClick={async () => {
                      const qs = `?workspace_id=${encodeURIComponent(workspaceId)}`
                      const res = await fetch(`/api/v1/crews/${issue.crew_id}/issues/${issue.identifier}/review${qs}`, {
                        method: "POST", headers: { "Content-Type": "application/json" },
                        body: JSON.stringify({ action: "request_changes", comment: reviewComment }),
                      })
                      if (res.ok) { toast.success("Changes requested"); setReviewChangesOpen(false); setReviewComment(""); onUpdated() }
                      else { const e = await res.json().catch(() => null); toast.error(e?.detail || "Failed") }
                    }}
                    className="flex-1 h-6 rounded text-[11px] transition-colors"
                    style={{ backgroundColor: STATUS_COLORS.BLOCKED, color: "white" }}
                  >Send</button>
                </div>
              </div>
            </motion.div>
          )}
          </AnimatePresence>

          {/* ── Properties section ───────────────────────────────────────── */}
          <IssuePropertiesPanel
            issue={issue}
            workspaceId={workspaceId}
            patchIssue={patchIssue}
          />

          {/* ── Routine section ──────────────────────────────────────────
            * Lets the user bind a saved routine to this issue and fire
            * it on demand. The picker writes routine_id via PATCH; the
            * Run button hits /pipelines/{slug}/run directly because
            * the routine is already resolved server-side (slug arrives
            * in the issue payload via the LEFT JOIN). Hidden entirely
            * when no routines are loaded (e.g. host page didn't pass
            * the prop) — keeps the panel tidy on /missions or other
            * surfaces that don't need it.
            */}
          <IssueRoutineBinder
            issue={issue}
            routines={routines}
            workspaceId={workspaceId}
            patchIssue={patchIssue}
            onUpdated={onUpdated}
          />

          {/* ── Labels section ───────────────────────────────────────────── */}
          <div className="mt-1 mx-2 rounded-lg border border-white/[0.04] bg-background">
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
                    <Command shouldFilter={true}>
                      <CommandInput placeholder="Search labels..." className="text-xs h-8" onValueChange={setLabelSearch} />
                      <CommandList>
                        <CommandEmpty>
                          {labelSearch.trim() ? (
                            <button
                              disabled={creatingLabel}
                              className="flex items-center gap-2 w-full px-2 py-1.5 text-xs text-foreground/80 hover:bg-white/[0.06] transition-colors"
                              onClick={async () => {
                                setCreatingLabel(true)
                                try {
                                  const color = LABEL_PRESET_COLORS[Math.floor(Math.random() * LABEL_PRESET_COLORS.length)].value
                                  const res = await fetch(`/api/v1/labels?workspace_id=${workspaceId}`, {
                                    method: "POST",
                                    headers: { "Content-Type": "application/json" },
                                    body: JSON.stringify({ name: labelSearch.trim(), color }),
                                  })
                                  if (res.ok) {
                                    const created = await res.json()
                                    const updated = [...(issue.labels || []).map(l => l.id), created.id]
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
                                    const remaining = (issue.labels || []).filter(l => l.id !== label.id).map(l => l.id)
                                    patchIssue({ labels: remaining })
                                  } else {
                                    const updated = [...(issue.labels || []).map(l => l.id), label.id]
                                    patchIssue({ labels: updated })
                                  }
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
                  <span className="text-[11px] text-foreground/40 pl-0.5">
                    No labels
                  </span>
                )}
              </div>
            )}
          </div>

          {/* ── Project section ────────────────────────────────────────── */}
          <div className="mt-1 mx-2 rounded-lg border border-white/[0.04] bg-background">
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
                      <div className="flex items-center gap-2 py-1 w-full group">
                        <button className="flex items-center gap-2 flex-1 text-left">
                          {(() => { const PIcon = getCrewIconDef(matchingProject?.icon || "folder").icon; return <PIcon className="h-3.5 w-3.5 shrink-0" style={{ color: matchingProject?.color || '#6B7280' }} /> })()}
                          <span className="text-[12px] text-foreground/80 hover:text-foreground transition-colors">{matchingProject?.name || "Unknown"}</span>
                        </button>
                        <a href={`/orchestration/projects/${issue.project_id}`}
                           className="opacity-0 group-hover:opacity-100 p-0.5 rounded hover:bg-white/[0.06] text-muted-foreground/40 hover:text-blue-400 transition-all"
                           title="Open project"
                           onClick={(e) => e.stopPropagation()}>
                          <svg className="h-3 w-3" viewBox="0 0 16 16" fill="none" stroke="currentColor" strokeWidth="1.5" strokeLinecap="round" strokeLinejoin="round"><path d="M6 2H3a1 1 0 0 0-1 1v10a1 1 0 0 0 1 1h10a1 1 0 0 0 1-1v-3"/><path d="M10 2h4v4"/><path d="M14 2L7 9"/></svg>
                        </a>
                      </div>
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
                                patchIssue({ project_id: null })
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
                              {(() => { const PIco = getCrewIconDef(p.icon || "folder").icon; return <PIco className="h-3.5 w-3.5 shrink-0" style={{ color: p.color }} /> })()}
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

          {/* ── Relations + Sub-issues sections ─────────────────────────── */}
          <IssueRelationsPanel issue={issue} workspaceId={workspaceId} />

          {/* ── Comments section ─────────────────────────────────────────── */}
          <div className="border-t border-white/[0.06] mt-1">
            <div className="px-3 py-1.5">
              <span className="text-[11px] uppercase tracking-wider text-foreground/50 font-medium">
                Comments ({comments.length})
              </span>
            </div>

            <div className="px-3">
              {comments.length > 0 ? (
                <div className="space-y-2.5">
                  {comments.map((comment) => (
                    <div key={comment.id} className="flex gap-2">
                      {comment.author_type === "agent" && comment.author_id ? (
                        <img src={getAgentAvatarUrl(comment.author_id)} alt="" className="w-5 h-5 rounded-full shrink-0 mt-0.5" />
                      ) : (
                        <div className="w-5 h-5 rounded-full bg-primary/20 flex items-center justify-center shrink-0 mt-0.5">
                          <span className="text-[9px] font-semibold text-primary">
                            {(comment.author_name || comment.author_type || "?")[0].toUpperCase()}
                          </span>
                        </div>
                      )}
                      <div className="flex-1 min-w-0">
                        <div className="flex items-center gap-2">
                          <span className="text-[11px] font-medium text-foreground/80">
                            {comment.author_name || comment.author_type}
                          </span>
                          <span className="text-[10px] text-foreground/35">
                            {timeAgo(comment.created_at)}
                          </span>
                        </div>
                        <div className="mt-0.5">
                          <MarkdownContent compact>{comment.body}</MarkdownContent>
                        </div>
                      </div>
                    </div>
                  ))}
                </div>
              ) : (
                <p className="text-[11px] text-foreground/40">No comments yet</p>
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

          {/* ── Activity timeline ───────────────────────────────────────── */}
          <div className="border-t border-white/[0.06] pt-3 px-4 pb-4">
            <div className="flex items-center justify-between mb-3">
              <span className="text-[11px] font-semibold text-foreground/80">Activity</span>
            </div>
            <ActivityFeed activities={activities} />
          </div>
        </div>
      </div>
    </div>
  )
}
