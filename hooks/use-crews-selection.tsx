"use client"

import { useCallback } from "react"

import { useShallowSearchParam } from "@/hooks/use-shallow-search-param"

export interface CrewsSelectionUpdate {
  agent?: string | null
  crew?: string | null
  /** The open tab; any change of agent or crew without it goes back to Overview. */
  tab?: string | null
}

export interface CrewsSelection {
  selectedAgentSlug: string | null
  selectedCrewSlug: string | null
  /** Raw ?tab= of the open crew or agent; the canvas maps legacy values. */
  tab: string | null
  setTab: (tab: string | null) => void
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
 * `selectCrew` clears the agent (focus is on the crew). Picking another crew or
 * agent also clears ?tab=, so a new selection opens on Overview. Use
 * `update({ agent, crew })` for atomic multi-field changes.
 */
export function useCrewsSelection(): CrewsSelection {
  const [selectedAgentSlug, setAgent] = useShallowSearchParam("agent")
  const [selectedCrewSlug, setCrew] = useShallowSearchParam("crew")
  const [tab, setTab] = useShallowSearchParam("tab")

  const update = useCallback(
    (updates: CrewsSelectionUpdate) => {
      if ("agent" in updates) setAgent(updates.agent ?? null)
      if ("crew" in updates) setCrew(updates.crew ?? null)
      if ("tab" in updates) setTab(updates.tab ?? null)
      else if ("agent" in updates || "crew" in updates) setTab(null)
    },
    [setAgent, setCrew, setTab],
  )

  const selectAgent = useCallback(
    (slug: string | null) => {
      setAgent(slug ?? null)
      setTab(null)
    },
    [setAgent, setTab],
  )

  const selectCrew = useCallback(
    (slug: string | null) => {
      setCrew(slug ?? null)
      setAgent(null)
      setTab(null)
    },
    [setCrew, setAgent, setTab],
  )

  const clearSelection = useCallback(() => {
    setAgent(null)
    setCrew(null)
    setTab(null)
  }, [setAgent, setCrew, setTab])

  return {
    selectedAgentSlug,
    selectedCrewSlug,
    tab,
    setTab,
    update,
    selectAgent,
    selectCrew,
    clearSelection,
  }
}
