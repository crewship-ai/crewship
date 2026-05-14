"use client"

import { useCallback, useState } from "react"
import type { Mission, IssueComment } from "@/lib/types/mission"

/**
 * Manages the orchestration "selected issue" panel: which issue is open,
 * its comments, and the lifecycle callbacks used by the detail pane.
 *
 * Extracted from orchestration-layout.tsx where it lived inline as five
 * stateful hooks + ~80 lines of handlers; consolidating here keeps the
 * comment-fetch URL shape, optimistic close, and refresh-after-update
 * flows in one place (and out of the layout component).
 */
export function useIssueDetail({
  workspaceId,
  onIssueSelected,
  fetchIssues,
  fetchProjects,
}: {
  workspaceId: string
  /** Called whenever the user opens an issue — orchestration uses this to clear the task detail-context. */
  onIssueSelected?: () => void
  /** Refreshes the orchestration issues list after an in-place update. */
  fetchIssues: () => Promise<void> | void
  /** Refreshes the projects list (project membership may have changed via issue edits). */
  fetchProjects: () => Promise<void> | void
}) {
  const [selectedIssue, setSelectedIssue] = useState<Mission | null>(null)
  const [issueComments, setIssueComments] = useState<IssueComment[]>([])

  const fetchComments = useCallback(
    async (crewId: string, identifier: string) => {
      try {
        const res = await fetch(
          `/api/v1/crews/${encodeURIComponent(crewId)}/issues/${encodeURIComponent(identifier)}/comments?workspace_id=${encodeURIComponent(workspaceId)}`,
        )
        if (res.ok) setIssueComments(await res.json())
        else setIssueComments([])
      } catch {
        setIssueComments([])
      }
    },
    [workspaceId],
  )

  const handleIssueSelect = useCallback(
    async (issue: Mission) => {
      // Toggle: clicking the same issue again deselects it.
      if (selectedIssue?.id === issue.id) {
        setSelectedIssue(null)
        setIssueComments([])
        return
      }
      setSelectedIssue(issue)
      onIssueSelected?.()
      if (issue.crew_id && issue.identifier) {
        await fetchComments(issue.crew_id, issue.identifier)
      }
    },
    [selectedIssue?.id, onIssueSelected, fetchComments],
  )

  const handleIssueClose = useCallback(() => {
    setSelectedIssue(null)
    setIssueComments([])
  }, [])

  const handleIssueUpdated = useCallback(async () => {
    await fetchIssues()
    if (selectedIssue?.crew_id && selectedIssue?.identifier) {
      try {
        const res = await fetch(
          `/api/v1/issues/${encodeURIComponent(selectedIssue.identifier)}?workspace_id=${encodeURIComponent(workspaceId)}`,
        )
        if (res.ok) {
          const fresh: Mission = await res.json()
          setSelectedIssue(fresh)
          if (fresh.crew_id && fresh.identifier) {
            await fetchComments(fresh.crew_id, fresh.identifier)
          }
        }
      } catch {
        /* ignore — fetchIssues already refreshed the list */
      }
    }
    await fetchProjects()
  }, [fetchIssues, fetchProjects, fetchComments, selectedIssue?.crew_id, selectedIssue?.identifier, workspaceId])

  return {
    selectedIssue,
    setSelectedIssue,
    issueComments,
    setIssueComments,
    handleIssueSelect,
    handleIssueClose,
    handleIssueUpdated,
  } as const
}
