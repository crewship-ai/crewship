"use client"

import { useCallback, useEffect, useMemo } from "react"
import { useUrlSelection } from "@/hooks/use-issue-detail"
import type { Project } from "@/lib/types/mission"

/**
 * Which project the /issues page has open.
 *
 * `?project=<id>` used to be read on arrival and never written — the ⌘K
 * palette could send you to a project, but selecting one in the page left the
 * URL saying nothing, so the two halves of the same screen disagreed about
 * whether it meant anything. It is now the source of truth in both
 * directions, through the same helper the issue selection uses, which is also
 * what keeps the two params from clobbering each other.
 */
export function useProjectDetail({ projects }: { projects: Project[] }) {
  const [selectedProjectId, setSelectedProjectId] = useUrlSelection("project")

  const selectedProject = useMemo(
    () => (selectedProjectId ? (projects.find((p) => p.id === selectedProjectId) ?? null) : null),
    [selectedProjectId, projects],
  )

  // Drop a selection whose project has left the list — deleted by somebody
  // else, or filtered away — so the layout never shows "detail open, empty".
  //
  // An empty list means "not loaded yet", not "deleted": on the first render
  // `projects` is [] while the fetch is in flight, and clearing there would
  // wipe the very id the URL arrived with. That was the bug the old
  // once-ever latch existed to work around.
  useEffect(() => {
    if (!selectedProjectId || projects.length === 0) return
    if (!projects.some((p) => p.id === selectedProjectId)) setSelectedProjectId(null)
  }, [selectedProjectId, projects, setSelectedProjectId])

  const handleProjectClose = useCallback(() => {
    setSelectedProjectId(null)
  }, [setSelectedProjectId])

  return {
    selectedProjectId,
    setSelectedProjectId,
    selectedProject,
    handleProjectClose,
  } as const
}
