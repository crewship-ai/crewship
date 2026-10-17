"use client"

import { useCallback, useMemo } from "react"
import { useUrlSelection } from "@/hooks/use-issue-detail"
import type { Project } from "@/lib/types/mission"

/** The project page's tabs. Updates is not here: the backend has no project updates yet. */
export const PROJECT_TABS = ["overview", "tree", "issues", "files", "settings"] as const
export type ProjectTab = (typeof PROJECT_TABS)[number]

function asTab(v: string | null): ProjectTab {
  return v && (PROJECT_TABS as readonly string[]).includes(v) ? (v as ProjectTab) : "overview"
}

/**
 * Which project the /issues page has open, and which of its tabs.
 *
 * `?project=<id>` is the source of truth in both directions, through the
 * same helper the issue selection uses, which is also what keeps the params
 * from clobbering each other. `?tab=` names the project page's tab; it is
 * written with replaceState, since switching tabs is not a navigation.
 *
 * An id no project has is kept and reported as `projectNotFound`, so the
 * page can say so. It used to be cleared silently, which left a reader who
 * followed a link to a deleted project on the plain list with no word about
 * where the project went.
 */
export function useProjectDetail({ projects, loaded }: { projects: Project[]; loaded?: boolean }) {
  const [selectedProjectId, setSelectedProjectId] = useUrlSelection("project")
  const [rawTab, setRawTab] = useUrlSelection("tab")

  // Without an explicit flag an empty list means "not loaded yet", not
  // "deleted": on the first render `projects` is [] while the fetch is in
  // flight, and a verdict there would contradict the very id the URL carried.
  const listKnown = loaded ?? projects.length > 0

  const selectedProject = useMemo(
    () => (selectedProjectId ? (projects.find((p) => p.id === selectedProjectId) ?? null) : null),
    [selectedProjectId, projects],
  )
  const projectNotFound = Boolean(selectedProjectId) && listKnown && selectedProject === null

  const projectTab = asTab(rawTab)
  const setProjectTab = useCallback(
    (tab: ProjectTab) => setRawTab(tab === "overview" ? null : tab, { replace: true }),
    [setRawTab],
  )

  const handleProjectClose = useCallback(() => {
    // The tab goes with the project. Leaving a dead link is a correction,
    // not a navigation: pushed, Back would return to the not-found page.
    setRawTab(null, { replace: true })
    setSelectedProjectId(null, projectNotFound ? { replace: true } : undefined)
  }, [setRawTab, setSelectedProjectId, projectNotFound])

  return {
    selectedProjectId,
    setSelectedProjectId,
    selectedProject,
    projectNotFound,
    projectTab,
    setProjectTab,
    handleProjectClose,
  } as const
}
