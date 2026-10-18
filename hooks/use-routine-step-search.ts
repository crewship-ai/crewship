"use client"

import { useEffect, useState } from "react"
import { apiFetch } from "@/lib/api-fetch"

const DEBOUNCE_MS = 300
const MIN_CHARS = 2

/** Slugs of the routines whose name, description or step text match `search`
 *  (GET /pipelines?q=, ODCH-RT-03), or null when there is nothing to add: no
 *  search, a single character, still typing, or the server refused — the
 *  panel's own name search keeps working then. */
export function useRoutineStepSearch(workspaceId: string, search: string): ReadonlySet<string> | null {
  const query = search.trim()
  const [found, setFound] = useState<{ query: string; slugs: ReadonlySet<string> } | null>(null)
  useEffect(() => {
    if (query.length < MIN_CHARS) return
    const controller = new AbortController()
    const timer = setTimeout(() => {
      apiFetch(`/api/v1/workspaces/${encodeURIComponent(workspaceId)}/pipelines?q=${encodeURIComponent(query)}&limit=500`, { signal: controller.signal })
        .then((r) => (r.ok ? r.json() : null))
        .then((rows: { slug?: string }[] | null) => {
          if (controller.signal.aborted || !Array.isArray(rows)) return
          setFound({ query, slugs: new Set(rows.map((r) => r.slug).filter((s): s is string => Boolean(s))) })
        })
        .catch(() => {})
    }, DEBOUNCE_MS)
    return () => {
      clearTimeout(timer)
      controller.abort()
    }
  }, [workspaceId, query])
  // An answer for an older query is not shown for the current one.
  return query.length >= MIN_CHARS && found?.query === query ? found.slugs : null
}
