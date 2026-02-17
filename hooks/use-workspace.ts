"use client"

import { useState, useEffect } from "react"

interface WorkspaceData {
  id: string
  name: string
  slug: string
  currentUserRole: string | null
}

interface UseWorkspaceReturn {
  workspaceId: string | null
  role: string | null
  loading: boolean
}

/**
 * Fetch the current user's workspaces and return the first org ID + role.
 *
 * MVP: single-org assumption — always uses the first workspace.
 * Will be replaced by the org switcher once wired.
 */
export function useWorkspace(): UseWorkspaceReturn {
  const [workspaceId, setWorkspaceId] = useState<string | null>(null)
  const [role, setRole] = useState<string | null>(null)
  const [loading, setLoading] = useState(true)

  useEffect(() => {
    let cancelled = false

    async function fetchWorkspaces() {
      try {
        const res = await fetch("/api/v1/workspaces")
        if (!res.ok) {
          setLoading(false)
          return
        }
        const orgs: WorkspaceData[] = await res.json()
        if (!cancelled && orgs.length > 0) {
          setWorkspaceId(orgs[0].id)
          setRole(orgs[0].currentUserRole)
        }
      } catch {
        // Silently fail — workspaceId stays null
      } finally {
        if (!cancelled) setLoading(false)
      }
    }

    fetchWorkspaces()
    return () => {
      cancelled = true
    }
  }, [])

  return { workspaceId, role, loading }
}
