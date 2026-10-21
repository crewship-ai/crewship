"use client"

import { useCallback, useEffect, useRef, useState } from "react"
import { useSearchParams } from "next/navigation"
import { applySavedView } from "@/lib/saved-views"
import type { IssuePriority, MissionStatus, SavedView } from "@/lib/types/mission"

/**
 * The Issues page's facets, with the URL as their source of truth.
 *
 * A link from another page (`/issues?crew_id=…`, `?status=REVIEW`) lands on
 * the filtered list, a refresh keeps it, and a filter picked here can be
 * copied as a link. Writes use `history.replaceState`: narrowing a list is
 * not a navigation, so Back still leaves the page (or closes the open issue)
 * rather than stepping through every facet the reader tried.
 */
export interface IssueFilters {
  statuses: MissionStatus[]
  priority: IssuePriority | null
  crewId: string | null
  /** The worker (`?assignee_id=`, kept for older links): delegate agent or the person holding it. */
  agentId: string | null
  ownerId: string | null
  holder: "agent" | "person" | null
  /** A label name, as the API's `?label=` spells it. */
  label: string | null
}

export const NO_FILTERS: IssueFilters = {
  statuses: [],
  priority: null,
  crewId: null,
  agentId: null,
  ownerId: null,
  holder: null,
  label: null,
}

const PRIORITIES: readonly IssuePriority[] = ["urgent", "high", "medium", "low", "none"]

/** The URL keys this hook owns; everything else in the query is left alone. */
export const FILTER_PARAM_KEYS = ["status", "priority", "crew_id", "assignee_id", "owner", "holder", "label"] as const

export function filtersFromParams(params: URLSearchParams): IssueFilters {
  const statuses = (params.get("status") ?? "")
    .split(",")
    .map((s) => s.trim().toUpperCase())
    .filter(Boolean) as MissionStatus[]
  const priority = params.get("priority")
  const holder = params.get("holder")
  return {
    statuses,
    priority: priority && (PRIORITIES as readonly string[]).includes(priority) ? (priority as IssuePriority) : null,
    crewId: params.get("crew_id") || null,
    agentId: params.get("assignee_id") || null,
    ownerId: params.get("owner") || null,
    holder: holder === "agent" || holder === "person" ? holder : null,
    label: params.get("label") || null,
  }
}

/** A copy of `params` carrying `filters`; keys this hook does not own survive. */
export function filtersToParams(filters: IssueFilters, params: URLSearchParams): URLSearchParams {
  const next = new URLSearchParams(params)
  for (const k of FILTER_PARAM_KEYS) next.delete(k)
  if (filters.statuses.length) next.set("status", filters.statuses.join(","))
  if (filters.priority) next.set("priority", filters.priority)
  if (filters.crewId) next.set("crew_id", filters.crewId)
  if (filters.agentId) next.set("assignee_id", filters.agentId)
  if (filters.ownerId) next.set("owner", filters.ownerId)
  if (filters.holder) next.set("holder", filters.holder)
  if (filters.label) next.set("label", filters.label)
  return next
}

/** How many facets narrow the list (each status counts). */
export function activeFilterCount(f: IssueFilters): number {
  return (
    f.statuses.length +
    (f.priority ? 1 : 0) +
    (f.crewId ? 1 : 0) +
    (f.agentId ? 1 : 0) +
    (f.ownerId ? 1 : 0) +
    (f.holder ? 1 : 0) +
    (f.label ? 1 : 0)
  )
}

/** The facets a saved view carries. Unknown or malformed fields are ignored. */
export function filtersFromSavedView(view: SavedView): { filters: IssueFilters; projectId: string | null; search: string } {
  const base = applySavedView(view)
  let extra: Record<string, unknown> = {}
  try {
    extra = view.filters_json ? (JSON.parse(view.filters_json) as Record<string, unknown>) : {}
  } catch {
    extra = {}
  }
  const str = (v: unknown) => (typeof v === "string" && v ? v : null)
  const statuses = Array.isArray(extra.statuses)
    ? (extra.statuses.filter((s) => typeof s === "string") as MissionStatus[])
    : typeof extra.status === "string" && extra.status
      ? (extra.status.split(",") as MissionStatus[])
      : []
  const priority = str(extra.priority)
  const holder = str(extra.holder)
  return {
    filters: {
      statuses,
      priority: priority && (PRIORITIES as readonly string[]).includes(priority) ? (priority as IssuePriority) : null,
      crewId: base.crewId || null,
      agentId: base.agentId || null,
      ownerId: str(extra.owner_id) ?? str(extra.owner),
      holder: holder === "agent" || holder === "person" ? holder : null,
      label: str(extra.label),
    },
    projectId: base.projectId || null,
    search: base.search,
  }
}

/** What a new saved view stores — the same names the API's list filter reads. */
export function savedViewPayload(f: IssueFilters, projectId: string | null, search: string): Record<string, unknown> {
  const out: Record<string, unknown> = {}
  if (f.statuses.length) out.statuses = f.statuses
  if (f.priority) out.priority = f.priority
  if (f.crewId) out.crew_id = f.crewId
  if (f.agentId) out.assignee_id = f.agentId
  if (f.ownerId) out.owner_id = f.ownerId
  if (f.holder) out.holder = f.holder
  if (f.label) out.label = f.label
  if (projectId) out.project_id = projectId
  if (search) out.search = search
  return out
}

function filterKey(params: URLSearchParams): string {
  return FILTER_PARAM_KEYS.map((k) => `${k}=${params.get(k) ?? ""}`).join("&")
}

export function useIssueFilters() {
  const params = useSearchParams()
  const [filters, setFiltersState] = useState<IssueFilters>(() => filtersFromParams(new URLSearchParams(params.toString())))
  // An arriving link (⌘K, another page's "Issues of this crew") wins once;
  // our own replaceState writes are recorded first, so their echo is a no-op.
  const key = filterKey(new URLSearchParams(params.toString()))
  const applied = useRef(key)
  useEffect(() => {
    if (applied.current === key) return
    applied.current = key
    setFiltersState(filtersFromParams(new URLSearchParams(params.toString())))
  }, [key, params])

  const setFilters = useCallback((update: IssueFilters | ((prev: IssueFilters) => IssueFilters)) => {
    setFiltersState((prev) => {
      const next = typeof update === "function" ? update(prev) : update
      if (typeof window !== "undefined") {
        const nextParams = filtersToParams(next, new URLSearchParams(window.location.search))
        applied.current = filterKey(nextParams)
        const qs = nextParams.toString()
        const url = `${window.location.pathname}${qs ? `?${qs}` : ""}${window.location.hash}`
        if (url !== window.location.pathname + window.location.search + window.location.hash) {
          window.history.replaceState(window.history.state, "", url)
        }
      }
      return next
    })
  }, [])

  const patch = useCallback(
    (part: Partial<IssueFilters>) => setFilters((prev) => ({ ...prev, ...part })),
    [setFilters],
  )
  const clear = useCallback(() => setFilters(NO_FILTERS), [setFilters])

  return { filters, setFilters, patch, clear } as const
}
