"use client"

import { useCallback, useState } from "react"

import { DEFAULT_AUDIT_FILTERS, filtersFromSearch, filtersToSearch, type AuditFilters } from "./audit-filters"

/**
 * Audit filters kept in the page URL (audit_* params next to ?tab=audit), so a
 * reload, a shared link or Back lands on the same slice of the log. Uses the
 * History API directly: the filters are this section's state, not a route.
 */
export function useAuditFilters(): [AuditFilters, (next: Partial<AuditFilters>) => void] {
  const [filters, setState] = useState<AuditFilters>(() =>
    typeof window === "undefined" ? DEFAULT_AUDIT_FILTERS : filtersFromSearch(window.location.search),
  )
  const update = useCallback((next: Partial<AuditFilters>) => {
    setState((prev) => {
      const merged = { ...prev, ...next }
      if (typeof window !== "undefined") {
        const search = filtersToSearch(merged, window.location.search)
        window.history.replaceState(window.history.state, "", `${window.location.pathname}${search}${window.location.hash}`)
      }
      return merged
    })
  }, [])
  return [filters, update]
}

