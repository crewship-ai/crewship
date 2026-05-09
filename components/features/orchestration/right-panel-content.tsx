"use client"

import { IssueDetailInline, ProjectDetailInline } from "@/components/features/orchestration/issues-inline"
import { ContextDetailPanel, type DetailContext } from "@/components/features/orchestration/context-detail-panel"
import type { Mission, IssueLabel, IssueComment, Project } from "@/lib/types/mission"
import type { Pipeline } from "@/hooks/use-pipelines"

export interface RightPanelContentProps {
  selectedIssue: Mission | null
  issueComments: IssueComment[]
  issueLabels: IssueLabel[]
  projects: Project[]
  routines?: Pipeline[]
  selectedProject: Project | null
  workspaceId: string
  detailContext: DetailContext
  onIssueClose: () => void
  onIssueUpdated: () => Promise<void>
  onProjectClose: () => void
  onProjectUpdated: () => void
  onDetailClose: () => void
  onTaskAction: (action: "edit" | "retry" | "skip", taskId: string, missionId: string) => void
}

/** Shared right panel content used in both mobile and desktop layouts */
export function RightPanelContent({
  selectedIssue,
  issueComments,
  issueLabels,
  projects,
  routines,
  selectedProject,
  workspaceId,
  detailContext,
  onIssueClose,
  onIssueUpdated,
  onProjectClose,
  onProjectUpdated,
  onDetailClose,
  onTaskAction,
}: RightPanelContentProps) {
  if (selectedIssue) {
    return (
      <IssueDetailInline
        key={selectedIssue.id}
        issue={selectedIssue}
        comments={issueComments}
        labels={issueLabels}
        projects={projects}
        routines={routines}
        workspaceId={workspaceId}
        onClose={onIssueClose}
        onUpdated={onIssueUpdated}
      />
    )
  }
  if (selectedProject) {
    return (
      <ProjectDetailInline
        key={selectedProject.id}
        project={selectedProject}
        workspaceId={workspaceId}
        onClose={onProjectClose}
        onUpdated={onProjectUpdated}
      />
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
