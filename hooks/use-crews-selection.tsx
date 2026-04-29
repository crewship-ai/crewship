"use client"

import { useCallback } from "react"
import { usePathname, useRouter, useSearchParams } from "next/navigation"

export interface CrewsSelectionUpdate {
  agent?: string | null
  crew?: string | null
}

export interface CrewsSelection {
  selectedAgentSlug: string | null
  selectedCrewSlug: string | null
  update: (updates: CrewsSelectionUpdate) => void
  selectAgent: (slug: string | null) => void
  selectCrew: (slug: string | null) => void
  clearSelection: () => void
}

/**
 * URL-driven selection state for /crews. Reads/writes `?agent=<slug>` and
 * `?crew=<slug>` via shallow routing so deep-links, refresh, and back-button
 * behave naturally.
 *
 * `selectCrew` clears the agent (focus is on the crew). Use
 * `update({ agent, crew })` for atomic multi-field changes.
 */
export function useCrewsSelection(): CrewsSelection {
  const searchParams = useSearchParams()
  const router = useRouter()
  const pathname = usePathname()

  const selectedAgentSlug = searchParams.get("agent")
  const selectedCrewSlug = searchParams.get("crew")

  const update = useCallback(
    (updates: CrewsSelectionUpdate) => {
      const params = new URLSearchParams(searchParams.toString())
      if ("agent" in updates) {
        if (updates.agent) params.set("agent", updates.agent)
        else params.delete("agent")
      }
      if ("crew" in updates) {
        if (updates.crew) params.set("crew", updates.crew)
        else params.delete("crew")
      }
      const query = params.toString()
      router.replace(query ? `${pathname}?${query}` : pathname, { scroll: false })
    },
    [pathname, router, searchParams],
  )

  const selectAgent = useCallback(
    (slug: string | null) => {
      update({ agent: slug })
    },
    [update],
  )

  const selectCrew = useCallback(
    (slug: string | null) => {
      update({ crew: slug, agent: null })
    },
    [update],
  )

  const clearSelection = useCallback(() => {
    update({ agent: null, crew: null })
  }, [update])

  return {
    selectedAgentSlug,
    selectedCrewSlug,
    update,
    selectAgent,
    selectCrew,
    clearSelection,
  }
}
