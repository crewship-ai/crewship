"use client"

import { useState, useEffect, useCallback } from "react"

interface RuntimeStatus {
  available: boolean
  runtime: string | null
  version: string | null
  socket: string | null
  install_links?: Record<string, string>
}

interface UseRuntimeReturn {
  runtime: RuntimeStatus | null
  loading: boolean
  error: string | null
  refresh: () => Promise<void>
}

/** Fetches container runtime status and exposes a refresh helper. */
export function useRuntime(): UseRuntimeReturn {
  const [runtime, setRuntime] = useState<RuntimeStatus | null>(null)
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)

  const refresh = useCallback(async () => {
    try {
      setLoading(true)
      setError(null)
      const res = await fetch("/api/v1/system/runtime")
      if (!res.ok) {
        setError("Failed to check runtime status")
        return
      }
      const data: RuntimeStatus = await res.json()
      setRuntime(data)
    } catch {
      setError("Network error checking runtime")
    } finally {
      setLoading(false)
    }
  }, [])

  useEffect(() => {
    refresh()
  }, [refresh])

  return { runtime, loading, error, refresh }
}
