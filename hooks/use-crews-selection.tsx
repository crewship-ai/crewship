"use client"

import { useCallback } from "react"
import { usePathname, useRouter, useSearchParams } from "next/navigation"

export type CrewsTab = "overview" | "activity" | "health"
export type CrewsDrawer = "chat" | "logs" | "settings"

const VALID_TABS: readonly CrewsTab[] = ["overview", "activity", "health"] as const
const VALID_DRAWERS: readonly CrewsDrawer[] = ["chat", "logs", "settings"] as const

export interface CrewsSelectionUpdate {
  agent?: string | null
  crew?: string | null
  tab?: CrewsTab
  drawer?: CrewsDrawer | null
}

export interface CrewsSelection {
  selectedAgentSlug: string | null
  selectedCrewSlug: string | null
  activeTab: CrewsTab
  activeDrawer: CrewsDrawer | null
  update: (updates: CrewsSelectionUpdate) => void
  selectAgent: (slug: string | null) => void
  selectCrew: (slug: string | null) => void
  setTab: (tab: CrewsTab) => void
  openDrawer: (drawer: CrewsDrawer) => void
  closeDrawer: () => void
  clearSelection: () => void
}

function parseTab(raw: string | null): CrewsTab {
  return raw !== null && (VALID_TABS as readonly string[]).includes(raw)
    ? (raw as CrewsTab)
    : "overview"
}

function parseDrawer(raw: string | null): CrewsDrawer | null {
  return raw !== null && (VALID_DRAWERS as readonly string[]).includes(raw)
    ? (raw as CrewsDrawer)
    : null
}

/**
 * URL-driven selection + view state for the Crews page. Reads/writes
 * `?agent=<slug>`, `?crew=<slug>`, `?tab=<overview|activity|health>`, and
 * `?drawer=<chat|logs|settings>` via shallow routing so deep-links, refresh,
 * and back-button behave naturally.
 *
 * `selectCrew` clears the agent (focus is on the crew). Use
 * `update({ agent, crew, tab, drawer })` for atomic multi-field changes.
 *
 * `tab` defaults to "overview" when absent or invalid. `drawer` is `null`
 * (closed) when absent or invalid — so a stale `?drawer=xyz` silently
 * renders as closed rather than blowing up.
 */
export function useCrewsSelection(): CrewsSelection {
  const searchParams = useSearchParams()
  const router = useRouter()
  const pathname = usePathname()

  const selectedAgentSlug = searchParams.get("agent")
  const selectedCrewSlug = searchParams.get("crew")
  const activeTab = parseTab(searchParams.get("tab"))
  const activeDrawer = parseDrawer(searchParams.get("drawer"))

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
      if ("tab" in updates && updates.tab !== undefined) {
        // Omit the param entirely when the value equals the default, keeping
        // URLs short and letting `?tab=overview` and no param mean the same.
        if (updates.tab === "overview") params.delete("tab")
        else params.set("tab", updates.tab)
      }
      if ("drawer" in updates) {
        if (updates.drawer) params.set("drawer", updates.drawer)
        else params.delete("drawer")
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

  const setTab = useCallback(
    (tab: CrewsTab) => {
      update({ tab })
    },
    [update],
  )

  const openDrawer = useCallback(
    (drawer: CrewsDrawer) => {
      update({ drawer })
    },
    [update],
  )

  const closeDrawer = useCallback(() => {
    update({ drawer: null })
  }, [update])

  const clearSelection = useCallback(() => {
    update({ agent: null, crew: null })
  }, [update])

  return {
    selectedAgentSlug,
    selectedCrewSlug,
    activeTab,
    activeDrawer,
    update,
    selectAgent,
    selectCrew,
    setTab,
    openDrawer,
    closeDrawer,
    clearSelection,
  }
}
