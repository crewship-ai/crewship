"use client"

import { useState, useCallback, useEffect } from "react"
import type { Credential } from "../types"

export function useCredentials(workspaceId: string | undefined) {
  const [credentials, setCredentials] = useState<Credential[]>([])
  const [loading, setLoading] = useState(false)

  const fetchCredentials = useCallback(async () => {
    if (!workspaceId) return
    setLoading(true)
    try {
      const res = await fetch(`/api/v1/credentials?workspace_id=${workspaceId}`)
      if (res.ok) {
        const data: Credential[] = await res.json()
        setCredentials(data)
      }
    } catch {
      // Silently fail — credentials are enhancement, not critical
    } finally {
      setLoading(false)
    }
  }, [workspaceId])

  useEffect(() => {
    if (workspaceId) {
      fetchCredentials()
    }
  }, [workspaceId, fetchCredentials])

  const addCredential = useCallback((cred: Credential) => {
    setCredentials((prev) => [...prev, cred])
  }, [])

  return { credentials, loading, fetchCredentials, addCredential }
}
