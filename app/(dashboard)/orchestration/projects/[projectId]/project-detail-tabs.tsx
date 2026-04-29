"use client"

import { useRouter } from "next/navigation"
import { Calendar, FolderKanban, Pencil, Plus } from "lucide-react"
import { getAgentAvatarUrl } from "@/lib/agent-avatar"
import { StatusIcon, statusLabel } from "@/components/features/issues/status-icon"
import { PriorityIcon, priorityLabel } from "@/components/features/issues/priority-icon"
import { MarkdownContent } from "@/components/features/issues/markdown-content"
import { ProjectStatusIcon } from "@/components/features/issues/project-status-icon"
import { PROJECT_STATUSES, PRIORITY_OPTIONS } from "@/components/features/issues/issue-constants"
import { CrewIconPopover } from "@/components/crew-icon-popover"
import { Button } from "@/components/ui/button"
import { Separator } from "@/components/ui/separator"
import { Spinner } from "@/components/ui/spinner"
import { formatShortDate } from "@/lib/time"
import type {
  Milestone,
  Mission,
  Project,
  ProjectStats,
} from "@/lib/types/mission"

// Tabs extracted from project-detail-client.tsx for readability.
// OverviewTab renders project description, stats, and recent activity;
// IssuesTab renders the tag-grouped issue list. Both consume the same
// project + workspace context the parent passes in.

function OverviewTab({
  project,
  stats,
  editingTitle,
  titleDraft,
  editingDesc,
  descDraft,
  patchProject,
  onEditTitle,
  onTitleChange,
  onTitleSave,
  onTitleCancel,
  onEditDesc,
  onDescChange,
  onDescSave,
  onDescCancel,
  milestones,
}: {
  project: Project
  stats: ProjectStats | null
  editingTitle: boolean
  titleDraft: string
  editingDesc: boolean
  descDraft: string
  patchProject: (fields: Record<string, unknown>) => Promise<void>
  onEditTitle: () => void
  onTitleChange: (v: string) => void
  onTitleSave: () => void
  onTitleCancel: () => void
  onEditDesc: () => void
  onDescChange: (v: string) => void
  onDescSave: () => void
  onDescCancel: () => void
  milestones: Milestone[]
}) {
  const statusInfo = PROJECT_STATUSES.find((s) => s.value === project.status)
  const priorityInfo = PRIORITY_OPTIONS.find((p) => p.value === project.priority)

  return (
    <div className="max-w-3xl mx-auto px-6 py-6 space-y-6">
      {/* Icon + Title */}
      <div className="flex items-start gap-4">
        <CrewIconPopover
          icon={project.icon || "folder"}
          color={project.color || "blue"}
          onIconChange={(icon) => patchProject({ icon })}
          onColorChange={(color) => patchProject({ color })}
        />
        <div className="flex-1 min-w-0">
          {editingTitle ? (
            <input
              className="text-display font-bold text-foreground bg-transparent border-b-2 border-primary outline-none w-full pb-1"
              value={titleDraft}
              onChange={(e) => onTitleChange(e.target.value)}
              onBlur={onTitleSave}
              onKeyDown={(e) => {
                if (e.key === "Enter") (e.target as HTMLInputElement).blur()
                if (e.key === "Escape") onTitleCancel()
              }}
              autoFocus
            />
          ) : (
            <h1
              className="text-display font-bold text-foreground cursor-pointer hover:text-foreground/80 transition-colors"
              onClick={onEditTitle}
            >
              {project.name}
            </h1>
          )}

          {/* Summary placeholder */}
          {!editingDesc && !project.description && (
            <button
              onClick={onEditDesc}
              className="text-body text-muted-foreground hover:text-foreground transition-colors mt-1"
            >
              Add a short summary...
            </button>
          )}
        </div>
      </div>

      {/* Properties bar */}
      <div className="flex items-center gap-3 flex-wrap">
        <div className="flex items-center gap-1.5 px-2.5 py-1 rounded-md bg-surface-subtle border border-white/[0.08]">
          <ProjectStatusIcon status={project.status} className="h-3.5 w-3.5 text-muted-foreground" />
          <span className="text-label text-foreground/80">{statusInfo?.label || project.status}</span>
        </div>
        <div className="flex items-center gap-1.5 px-2.5 py-1 rounded-md bg-surface-subtle border border-white/[0.08]">
          <PriorityIcon priority={project.priority || "none"} className="h-3.5 w-3.5" />
          <span className="text-label text-foreground/80">{priorityInfo?.label || "No priority"}</span>
        </div>
        {project.lead_name && (
          <div className="flex items-center gap-1.5 px-2.5 py-1 rounded-md bg-surface-subtle border border-white/[0.08]">
            {project.lead_id && (
              <img src={getAgentAvatarUrl(project.lead_id)} alt="" className="h-4 w-4 rounded-full" />
            )}
            <span className="text-label text-foreground/80">{project.lead_name}</span>
          </div>
        )}
        {project.target_date && (
          <div className="flex items-center gap-1.5 px-2.5 py-1 rounded-md bg-surface-subtle border border-white/[0.08]">
            <Calendar className="h-3 w-3 text-muted-foreground" />
            <span className="text-label text-foreground/80">{formatShortDate(project.target_date)}</span>
          </div>
        )}
        {stats?.crews && stats.crews.length > 0 && stats.crews.map((crew) => (
          <div
            key={crew}
            className="flex items-center gap-1.5 px-2.5 py-1 rounded-md bg-surface-subtle border border-white/[0.08]"
          >
            <span className="text-label text-foreground/80">{crew}</span>
          </div>
        ))}
      </div>

      <Separator className="bg-white/[0.08]" />

      {/* Resources placeholder */}
      <div>
        <h3 className="text-label font-semibold text-muted-foreground uppercase tracking-wider mb-2">Resources</h3>
        <button className="flex items-center gap-2 text-label text-muted-foreground hover:text-foreground transition-colors py-1.5">
          <Plus className="h-3.5 w-3.5" />
          Add document or link...
        </button>
      </div>

      <Separator className="bg-white/[0.08]" />

      {/* Project update placeholder */}
      <button className="w-full flex items-center justify-center gap-2 py-3 px-4 rounded-lg border border-dashed border-white/[0.12] hover:border-white/[0.2] text-muted-foreground hover:text-foreground transition-colors">
        <Pencil className="h-3.5 w-3.5" />
        <span className="text-body">Write first project update</span>
      </button>

      <Separator className="bg-white/[0.08]" />

      {/* Description */}
      <div>
        <h3 className="text-label font-semibold text-muted-foreground uppercase tracking-wider mb-3">Description</h3>
        {editingDesc ? (
          <div className="space-y-2">
            <textarea
              className="w-full min-h-[120px] bg-surface-subtle border border-white/[0.12] rounded-md px-3 py-2 text-body text-foreground placeholder:text-muted-foreground outline-none focus:border-primary/50 resize-y"
              value={descDraft}
              onChange={(e) => onDescChange(e.target.value)}
              placeholder="Describe the project..."
              autoFocus
            />
            <div className="flex items-center gap-2">
              <Button size="sm" variant="default" onClick={onDescSave} className="text-label h-7">
                Save
              </Button>
              <Button size="sm" variant="ghost" onClick={onDescCancel} className="text-label h-7">
                Cancel
              </Button>
            </div>
          </div>
        ) : project.description ? (
          <div
            className="cursor-pointer hover:bg-accent/40 rounded-md p-2 -m-2 transition-colors"
            onClick={onEditDesc}
          >
            <MarkdownContent className="text-body">{project.description}</MarkdownContent>
          </div>
        ) : (
          <button
            onClick={onEditDesc}
            className="text-body text-muted-foreground hover:text-foreground transition-colors"
          >
            Add description...
          </button>
        )}
      </div>

      <Separator className="bg-white/[0.08]" />

      {/* Milestones overview */}
      <div>
        <h3 className="text-label font-semibold text-muted-foreground uppercase tracking-wider mb-3">
          Milestones {milestones.length > 0 && `(${milestones.length})`}
        </h3>
        {milestones.length === 0 ? (
          <p className="text-label text-muted-foreground">No milestones yet. Add one from the sidebar.</p>
        ) : (
          <div className="space-y-2">
            {milestones.map((m) => {
              const progress = m.issue_count && m.issue_count > 0
                ? Math.round(((m.done_count ?? 0) / m.issue_count) * 100)
                : 0
              return (
                <div key={m.id} className="flex items-center gap-3">
                  <div className="flex-1 min-w-0">
                    <span className="text-label text-foreground/80 font-medium">{m.name}</span>
                    <div className="flex items-center gap-2 mt-0.5">
                      {m.target_date && (
                        <span className="text-micro text-muted-foreground">
                          {new Date(m.target_date).toLocaleDateString(undefined, { month: "short", day: "numeric" })}
                        </span>
                      )}
                      <span className="text-micro text-muted-foreground">{m.done_count ?? 0}/{m.issue_count ?? 0} done</span>
                    </div>
                  </div>
                  <div className="w-16 h-1 bg-white/[0.08] rounded-full overflow-hidden">
                    <div
                      className="h-full bg-primary rounded-full transition-all"
                      style={{ width: `${progress}%` }}
                    />
                  </div>
                </div>
              )
            })}
          </div>
        )}
      </div>
    </div>
  )
}

// ===========================================================================
// Issues tab
// ===========================================================================


function IssuesTab({
  issues,
  loading,
  router,
}: {
  issues: Mission[]
  loading: boolean
  router: ReturnType<typeof useRouter>
}) {
  if (loading) {
    return (
      <div className="flex items-center justify-center py-16">
        <Spinner className="h-5 w-5" />
      </div>
    )
  }

  if (issues.length === 0) {
    return (
      <div className="flex flex-col items-center justify-center py-16 text-muted-foreground">
        <FolderKanban className="h-8 w-8 mb-3" />
        <p className="text-body">No issues in this project</p>
      </div>
    )
  }

  return (
    <div className="p-4">
      <table className="w-full text-left">
        <thead>
          <tr className="border-b border-white/[0.08]">
            <th className="text-micro font-medium text-muted-foreground uppercase tracking-wider py-2 px-3 w-[80px]">ID</th>
            <th className="text-micro font-medium text-muted-foreground uppercase tracking-wider py-2 px-3">Title</th>
            <th className="text-micro font-medium text-muted-foreground uppercase tracking-wider py-2 px-3 w-[100px]">Status</th>
            <th className="text-micro font-medium text-muted-foreground uppercase tracking-wider py-2 px-3 w-[90px]">Priority</th>
            <th className="text-micro font-medium text-muted-foreground uppercase tracking-wider py-2 px-3 w-[120px]">Assignee</th>
          </tr>
        </thead>
        <tbody>
          {issues.map((issue) => (
            <tr
              key={issue.id}
              tabIndex={0}
              role="link"
              onClick={() => {
                if (issue.identifier) router.push(`/orchestration/issues/${issue.identifier}`)
              }}
              onKeyDown={(e) => {
                if ((e.key === "Enter" || e.key === " ") && issue.identifier) {
                  e.preventDefault()
                  router.push(`/orchestration/issues/${issue.identifier}`)
                }
              }}
              className="border-b border-white/[0.04] hover:bg-accent/40 transition-colors cursor-pointer focus:outline-none focus:bg-accent/60"
            >
              <td className="py-2 px-3">
                <span className="text-label font-mono text-muted-foreground">{issue.identifier || "--"}</span>
              </td>
              <td className="py-2 px-3">
                <span className="text-label text-foreground/80 truncate">{issue.title}</span>
              </td>
              <td className="py-2 px-3">
                <div className="flex items-center gap-1.5">
                  <StatusIcon status={issue.status} className="h-3.5 w-3.5" />
                  <span className="text-label text-muted-foreground">{statusLabel[issue.status] || issue.status}</span>
                </div>
              </td>
              <td className="py-2 px-3">
                <div className="flex items-center gap-1.5">
                  <PriorityIcon priority={issue.priority || "none"} className="h-3.5 w-3.5" />
                  <span className="text-label text-muted-foreground">{priorityLabel[issue.priority || "none"]}</span>
                </div>
              </td>
              <td className="py-2 px-3">
                {issue.assignee_name ? (
                  <div className="flex items-center gap-1.5">
                    {issue.assignee_id && (
                      <img
                        src={getAgentAvatarUrl(issue.assignee_id)}
                        alt=""
                        className="h-4 w-4 rounded-full"
                      />
                    )}
                    <span className="text-label text-muted-foreground truncate">{issue.assignee_name}</span>
                  </div>
                ) : (
                  <span className="text-label text-muted-foreground/60">--</span>
                )}
              </td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  )
}


export { OverviewTab, IssuesTab }
