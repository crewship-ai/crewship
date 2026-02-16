"use client"

import { useState, useEffect } from "react"

interface OrgData {
  id: string
  name: string
  slug: string
}

interface UseOrgReturn {
  orgId: string | null
  loading: boolean
}

/**
 * Fetch the current user's organizations and return the first org ID.
 *
 * MVP: single-org assumption — always uses the first organization.
 * Will be replaced by the org switcher once wired.
 */
export function useOrg(): UseOrgReturn {
  const [orgId, setOrgId] = useState<string | null>(null)
  const [loading, setLoading] = useState(true)

  useEffect(() => {
    let cancelled = false

    async function fetchOrgs() {
      try {
        const res = await fetch("/api/v1/orgs")
        if (!res.ok) {
          setLoading(false)
          return
        }
        const orgs: OrgData[] = await res.json()
        if (!cancelled && orgs.length > 0) {
          setOrgId(orgs[0].id)
        }
      } catch {
        // Silently fail — orgId stays null
      } finally {
        if (!cancelled) setLoading(false)
      }
    }

    fetchOrgs()
    return () => {
      cancelled = true
    }
  }, [])

  return { orgId, loading }
}
