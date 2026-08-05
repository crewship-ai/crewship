"use client"

import { IssueDetailSurface } from "@/components/features/issues/issue-detail-surface"
import { ProjectDetailSurface } from "@/components/features/issues/project-detail-surface"
import { ContextDetailPanel, type DetailContext } from "@/components/features/orchestration/context-detail-panel"
import type { Mission, Project } from "@/lib/types/mission"

export interface RightPanelContentProps {
  selectedIssue: Mission | null
  /** Every loaded issue — the project card lists the ones filed under it. */
  issues: Mission[]
  selectedProject: Project | null
  workspaceId: string
  detailContext: DetailContext
  onIssueUpdated: () => Promise<void> | void
  onProjectUpdated: () => void
  onDetailClose: () => void
  onTaskAction: (action: "edit" | "retry" | "skip", taskId: string, missionId: string) => void
}

/**
 * Shared right-panel content, used in both the mobile and desktop layouts.
 *
 * The issue and project arms render the same surfaces as the centre pane and
 * as /issues/<identifier>. They used to render IssueDetailInline and
 * ProjectDetailInline — a third and fourth copy of the same two screens,
 * reachable whenever a task detail-context was open on a non-issues tab.
 */
export function RightPanelContent({
  selectedIssue,
  issues,
  selectedProject,
  workspaceId,
  detailContext,
  onIssueUpdated,
  onProjectUpdated,
  onDetailClose,
  onTaskAction,
}: RightPanelContentProps) {
  if (selectedIssue) {
    return (
      <div className="h-full overflow-y-auto">
        <IssueDetailSurface
          key={selectedIssue.id}
          workspaceId={workspaceId}
          identifier={selectedIssue.identifier ?? selectedIssue.id}
          onChanged={onIssueUpdated}
        />
      </div>
    )
  }
  if (selectedProject) {
    return (
      <div className="h-full overflow-y-auto">
        <ProjectDetailSurface
          key={selectedProject.id}
          workspaceId={workspaceId}
          project={selectedProject}
          issues={issues}
          onChanged={onProjectUpdated}
        />
      </div>
    )
  }
  return (
    <ContextDetailPanel
      context={detailContext}
      onClose={onDetailClose}
      onTaskAction={onTaskAction}
    />
  )
}
