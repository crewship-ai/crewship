"use client"

import { IssuesBoardView } from "@/components/features/issues/issues-board-view"
import { IssuesListView } from "@/components/features/issues/issues-list-view"
import type { Mission } from "@/lib/types/mission"

// The detail re-exports that lived here are gone with the panels themselves:
// /issues and /issues/<identifier> both render the issue-detail-surface now,
// so an issue has one screen instead of two.

/* -------------------------------------------------------------------------- */
/*  IssuesBoardInline — center board view wrapper                             */
/* -------------------------------------------------------------------------- */

interface IssuesBoardInlineProps {
  issues: Mission[]
  onIssueClick: (issue: Mission) => void
  selectedIssueId?: string | null
}

export function IssuesBoardInline({ issues, onIssueClick, selectedIssueId }: IssuesBoardInlineProps) {
  return <IssuesBoardView issues={issues} onIssueClick={onIssueClick} selectedIssueId={selectedIssueId} />
}

/* -------------------------------------------------------------------------- */
/*  IssuesListInline — center list view wrapper                               */
/* -------------------------------------------------------------------------- */

interface IssuesListInlineProps {
  issues: Mission[]
  onIssueClick: (issue: Mission) => void
  selectedIssueId?: string | null
  /**
   * Required for the bulk-edit bar to reach the server. Without it
   * `IssuesListView.handleBulkUpdate` returns at `if (!workspaceId) return`
   * and the selection + status pick do nothing at all, silently — which is
   * exactly what this wrapper used to do.
   */
  workspaceId: string
  /** Let the caller own the write instead of the built-in bulk PATCH. */
  onBulkAction?: (ids: string[], updates: Record<string, unknown>) => void
}

export function IssuesListInline({
  issues,
  onIssueClick,
  selectedIssueId,
  workspaceId,
  onBulkAction,
}: IssuesListInlineProps) {
  return (
    <IssuesListView
      issues={issues}
      onIssueClick={onIssueClick}
      selectedIssueId={selectedIssueId}
      workspaceId={workspaceId}
      onBulkAction={onBulkAction}
    />
  )
}
