"use client"

import { useEffect, useState } from "react"

/**
 * Wraps the AbortController + loading + error pattern shared by the
 * right-panel sub-tabs (Triggers / SharedContext / TeamChat). The
 * `fetcher` is re-invoked whenever any dependency in `deps` changes;
 * the previous request is aborted before a new one is started. The
 * `enabled` flag (default true) is the "don't fetch yet" guard — when
 * false the hook resets to the empty state and skips the request.
 *
 * The fetcher receives the AbortSignal and is expected to return the
 * data payload (or null / undefined when there's nothing to render).
 */
export function useAgentFetch<T>(
  fetcher: (signal: AbortSignal) => Promise<T | null>,
  deps: ReadonlyArray<unknown>,
  options?: { enabled?: boolean; logLabel?: string },
): { data: T | null; loading: boolean; error: unknown } {
  const enabled = options?.enabled ?? true
  const label = options?.logLabel
  const [data, setData] = useState<T | null>(null)
  const [loading, setLoading] = useState(enabled)
  const [error, setError] = useState<unknown>(null)

  useEffect(() => {
    if (!enabled) {
      setLoading(false)
      setData(null)
      setError(null)
      return
    }
    const controller = new AbortController()
    setLoading(true)
    fetcher(controller.signal)
      .then((result) => {
        if (controller.signal.aborted) return
        setData(result ?? null)
        setError(null)
      })
      .catch((err) => {
        if (err instanceof DOMException && err.name === "AbortError") return
        if (label) console.error(`${label}: fetch failed`, err)
        setError(err)
        setData(null)
      })
      .finally(() => {
        if (!controller.signal.aborted) setLoading(false)
      })
    return () => controller.abort()
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, deps)

  return { data, loading, error }
}
