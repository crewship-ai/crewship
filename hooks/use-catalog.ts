"use client"

import { useCallback, useEffect, useRef, useState } from "react"
import { apiFetch } from "@/lib/api-fetch"

export interface UseCatalogResult<T> {
  data: T[] | null
  loading: boolean
  error: Error | null
  refetch: () => void
}

export function useCatalog<T>(
  url: string,
  extract: (json: unknown) => T[],
  enabled = true,
): UseCatalogResult<T> {
  const [data, setData] = useState<T[] | null>(null)
  const [loading, setLoading] = useState<boolean>(enabled)
  const [error, setError] = useState<Error | null>(null)
  const abortRef = useRef<AbortController | null>(null)
  const [tick, setTick] = useState(0)

  const refetch = useCallback(() => setTick((n) => n + 1), [])

  useEffect(() => {
    if (!enabled) {
      setLoading(false)
      return
    }
    abortRef.current?.abort()
    const controller = new AbortController()
    abortRef.current = controller

    setLoading(true)
    setError(null)

    apiFetch(url, { signal: controller.signal })
      .then((r) => {
        if (!r.ok) throw new Error(`fetch ${url} failed: ${r.status}`)
        return r.json()
      })
      .then((json) => {
        if (controller.signal.aborted) return
        setData(extract(json))
      })
      .catch((e: unknown) => {
        if (controller.signal.aborted) return
        if (e instanceof DOMException && e.name === "AbortError") return
        setData([])
        setError(e instanceof Error ? e : new Error(String(e)))
      })
      .finally(() => {
        if (!controller.signal.aborted) setLoading(false)
      })

    return () => controller.abort()
  }, [url, enabled, extract, tick])

  return { data, loading, error, refetch }
}
