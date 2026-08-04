"use client"

import { useCallback, useEffect, useMemo, useRef, useState } from "react"
import type { Project } from "@/lib/types/mission"

/**
 * Manages the orchestration "selected project" panel. State is tiny —
 * just the selectedProjectId + a derived selectedProject lookup — but
 * extracting it mirrors useIssueDetail so the two detail panes have a
 * symmetrical surface and the layout component stops accumulating
 * one-off useState calls for each new detail kind.
 */
export function useProjectDetail({
  projects,
  initialProjectId,
}: {
  projects: Project[]
  /**
   * Project named by the URL on arrival (/issues?project=<id>) — the ⌘K
   * palette's Projects rows, and any bookmark. Applied ONCE, and only after
   * the list has arrived: on the first render `projects` is still empty, so
   * seeding the state directly would be wiped by the cleanup effect below
   * before the fetch ever resolved.
   */
  initialProjectId?: string | null
}) {
  const [selectedProjectId, setSelectedProjectId] = useState<string | null>(null)
  const seeded = useRef(false)

  // Apply the incoming id when there is finally something to match it
  // against. Once is the point: a later refresh of the list must not drag the
  // user back to the project the URL named after they clicked away, and an id
  // no project has is dropped rather than left as an empty detail pane.
  useEffect(() => {
    if (seeded.current || !initialProjectId || projects.length === 0) return
    seeded.current = true
    if (projects.some((p) => p.id === initialProjectId)) {
      setSelectedProjectId(initialProjectId)
    }
  }, [initialProjectId, projects])

  const selectedProject = useMemo(
    () => (selectedProjectId ? projects.find((p) => p.id === selectedProjectId) ?? null : null),
    [selectedProjectId, projects],
  )

  // Clear the selection when the underlying project disappears from the
  // refreshed list (deleted by another user, filtered out, etc.). Without
  // this, selectedProject becomes null while selectedProjectId stays set,
  // and the layout enters a "detail open but empty" state.
  useEffect(() => {
    if (selectedProjectId && !projects.some((p) => p.id === selectedProjectId)) {
      setSelectedProjectId(null)
    }
  }, [selectedProjectId, projects])

  const handleProjectClose = useCallback(() => {
    setSelectedProjectId(null)
  }, [])

  return {
    selectedProjectId,
    setSelectedProjectId,
    selectedProject,
    handleProjectClose,
  } as const
}
