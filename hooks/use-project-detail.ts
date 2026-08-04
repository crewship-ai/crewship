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
  // The last id this hook acted on — NOT a once-ever latch.
  //
  // A latch was the first attempt, to survive the gap before the project list
  // arrives (on the first render `projects` is [], and the cleanup effect
  // below would wipe a directly-seeded selection). But it also swallowed
  // every LATER link: on /issues?project=A, opening ⌘K and picking project B
  // changed the URL and nothing else, because this component never unmounts.
  // That is the commoner path of the two — the palette is most reachable from
  // a page you are already on.
  //
  // Keyed on the id instead, each new one is applied exactly once: a refresh
  // of the roster is not a navigation, so it cannot drag the user back to the
  // project the URL named after they clicked away, and an id no project has
  // is marked handled rather than retried when a later fetch happens to
  // produce it.
  const appliedId = useRef<string | null>(null)

  useEffect(() => {
    if (!initialProjectId || projects.length === 0) return
    if (appliedId.current === initialProjectId) return
    appliedId.current = initialProjectId
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
