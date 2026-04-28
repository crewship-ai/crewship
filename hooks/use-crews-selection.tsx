"use client"

import { useCallback } from "react"
import { usePathname, useRouter, useSearchParams } from "next/navigation"

export type CrewsStatusFilter = "all" | "RUNNING" | "IDLE" | "ERROR" | "STOPPED"
export type CrewsRoleFilter = "all" | "AGENT" | "LEAD" | "COORDINATOR"

const VALID_STATUS: readonly CrewsStatusFilter[] = ["all", "RUNNING", "IDLE", "ERROR", "STOPPED"] as const
const VALID_ROLE: readonly CrewsRoleFilter[] = ["all", "AGENT", "LEAD", "COORDINATOR"] as const

export interface CrewsSelectionUpdate {
  agent?: string | null
  crew?: string | null
  status?: CrewsStatusFilter
  role?: CrewsRoleFilter
}

export interface CrewsSelection {
  selectedAgentSlug: string | null
  selectedCrewSlug: string | null
  statusFilter: CrewsStatusFilter
  roleFilter: CrewsRoleFilter
  update: (updates: CrewsSelectionUpdate) => void
  selectAgent: (slug: string | null) => void
  selectCrew: (slug: string | null) => void
  setStatus: (status: CrewsStatusFilter) => void
  setRole: (role: CrewsRoleFilter) => void
  clearSelection: () => void
}

function parseStatus(raw: string | null): CrewsStatusFilter {
  return raw !== null && (VALID_STATUS as readonly string[]).includes(raw)
    ? (raw as CrewsStatusFilter)
    : "all"
}

function parseRole(raw: string | null): CrewsRoleFilter {
  return raw !== null && (VALID_ROLE as readonly string[]).includes(raw)
    ? (raw as CrewsRoleFilter)
    : "all"
}

/**
 * URL-driven selection + filter state for /crews. Reads/writes
 * `?agent=<slug>`, `?crew=<slug>`, `?status=<filter>`, `?role=<filter>`
 * via shallow routing so deep-links, refresh, and back-button behave
 * naturally.
 *
 * `selectCrew` clears the agent (focus is on the crew). Use
 * `update({ agent, crew, status, role })` for atomic multi-field changes.
 *
 * Default filter values ("all") are omitted from the URL so canonical
 * `/crews` and `/crews?status=all` mean the same thing.
 */
export function useCrewsSelection(): CrewsSelection {
  const searchParams = useSearchParams()
  const router = useRouter()
  const pathname = usePathname()

  const selectedAgentSlug = searchParams.get("agent")
  const selectedCrewSlug = searchParams.get("crew")
  const statusFilter = parseStatus(searchParams.get("status"))
  const roleFilter = parseRole(searchParams.get("role"))

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
      if ("status" in updates && updates.status !== undefined) {
        if (updates.status === "all") params.delete("status")
        else params.set("status", updates.status)
      }
      if ("role" in updates && updates.role !== undefined) {
        if (updates.role === "all") params.delete("role")
        else params.set("role", updates.role)
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

  const setStatus = useCallback(
    (status: CrewsStatusFilter) => update({ status }),
    [update],
  )

  const setRole = useCallback(
    (role: CrewsRoleFilter) => update({ role }),
    [update],
  )

  const clearSelection = useCallback(() => {
    update({ agent: null, crew: null })
  }, [update])

  return {
    selectedAgentSlug,
    selectedCrewSlug,
    statusFilter,
    roleFilter,
    update,
    selectAgent,
    selectCrew,
    setStatus,
    setRole,
    clearSelection,
  }
}
