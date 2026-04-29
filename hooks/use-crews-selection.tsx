"use client"

import { useCallback } from "react"

import { useShallowSearchParam } from "@/hooks/use-shallow-search-param"

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
 * `?crew=<slug>` via window.history.replaceState — NOT next/navigation —
 * so picking another agent/crew never re-evaluates the dashboard layout
 * subtree. (router.replace caused a full-screen Loader2 spinner blip
 * from layout.tsx on every selection in production builds; see the
 * chat-page-client fix on feat/chat-ui-overhaul for the precedent.)
 *
 * `selectCrew` clears the agent (focus is on the crew). Use
 * `update({ agent, crew })` for atomic multi-field changes.
 */
export function useCrewsSelection(): CrewsSelection {
  const [selectedAgentSlug, setAgent] = useShallowSearchParam("agent")
  const [selectedCrewSlug, setCrew] = useShallowSearchParam("crew")

  const update = useCallback(
    (updates: CrewsSelectionUpdate) => {
      if ("agent" in updates) setAgent(updates.agent ?? null)
      if ("crew" in updates) setCrew(updates.crew ?? null)
    },
    [setAgent, setCrew],
  )

  const selectAgent = useCallback(
    (slug: string | null) => {
      setAgent(slug ?? null)
    },
    [setAgent],
  )

  const selectCrew = useCallback(
    (slug: string | null) => {
      setCrew(slug ?? null)
      setAgent(null)
    },
    [setCrew, setAgent],
  )

  const clearSelection = useCallback(() => {
    setAgent(null)
    setCrew(null)
  }, [setAgent, setCrew])

  return {
    selectedAgentSlug,
    selectedCrewSlug,
    update,
    selectAgent,
    selectCrew,
    clearSelection,
  }
}
