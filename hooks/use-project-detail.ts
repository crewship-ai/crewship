"use client"

import { useCallback, useEffect, useMemo, useState } from "react"
import type { Project } from "@/lib/types/mission"

/**
 * Manages the orchestration "selected project" panel. State is tiny —
 * just the selectedProjectId + a derived selectedProject lookup — but
 * extracting it mirrors useIssueDetail so the two detail panes have a
 * symmetrical surface and the layout component stops accumulating
 * one-off useState calls for each new detail kind.
 */
export function useProjectDetail({ projects }: { projects: Project[] }) {
  const [selectedProjectId, setSelectedProjectId] = useState<string | null>(null)

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
