"use client"

import { useCallback, useEffect, useRef, useState } from "react"

/**
 * Per-user UI preference with cross-device sync via the server +
 * localStorage cache for instant first-paint reads.
 *
 * Flow on mount:
 *   1. Read localStorage synchronously — UI gets the user's last value
 *      immediately, no flash of default.
 *   2. Fetch /api/v1/me/preferences in the background; if the server
 *      has a different value, override and update localStorage.
 *
 * Flow on set:
 *   1. Update React state + localStorage synchronously (snappy UI).
 *   2. Debounced PUT /api/v1/me/preferences/{key} — 400ms tail so a
 *      drag gesture doesn't hammer the server with intermediate values.
 *
 * The hook is generic; key namespace is the caller's responsibility.
 * Use dotted keys like "crews.bottomPanel.height" to keep things tidy.
 */
export function useUserPreference<T>(
  key: string,
  defaultValue: T,
): [T, (next: T) => void, { ready: boolean }] {
  const lsKey = `crewship.pref.${key}`

  const [value, setLocal] = useState<T>(() => {
    if (typeof window === "undefined") return defaultValue
    try {
      const raw = window.localStorage.getItem(lsKey)
      if (raw == null) return defaultValue
      return JSON.parse(raw) as T
    } catch {
      return defaultValue
    }
  })
  const [ready, setReady] = useState(false)
  const debounceRef = useRef<ReturnType<typeof setTimeout> | null>(null)
  const pendingValueRef = useRef<T | null>(null)
  const hasPendingRef = useRef(false)
  const valueRef = useRef(value)
  valueRef.current = value

  // First paint: pull the server-side value. If it differs, override
  // local state. We never overwrite the server with a stale localStorage
  // value at startup — the server always wins on initial sync.
  useEffect(() => {
    let cancelled = false
    fetch("/api/v1/me/preferences", { credentials: "include" })
      .then((r) => (r.ok ? r.json() : null))
      .then((data: Record<string, unknown> | null) => {
        if (cancelled || !data) return
        if (key in data) {
          const remote = data[key] as T
          // Compare by JSON to dodge object identity; cheap for our
          // use case (small primitives + small objects).
          const same = JSON.stringify(remote) === JSON.stringify(valueRef.current)
          if (!same) {
            setLocal(remote)
            try {
              window.localStorage.setItem(lsKey, JSON.stringify(remote))
            } catch {
              /* quota / private browsing */
            }
          }
        }
        setReady(true)
      })
      .catch(() => {
        if (!cancelled) setReady(true)
      })
    return () => {
      cancelled = true
    }
  }, [key, lsKey])

  const set = useCallback(
    (next: T) => {
      setLocal(next)
      try {
        window.localStorage.setItem(lsKey, JSON.stringify(next))
      } catch {
        /* ignore */
      }
      if (debounceRef.current) clearTimeout(debounceRef.current)
      pendingValueRef.current = next
      hasPendingRef.current = true
      debounceRef.current = setTimeout(() => {
        hasPendingRef.current = false
        pendingValueRef.current = null
        fetch(`/api/v1/me/preferences/${encodeURIComponent(key)}`, {
          method: "PUT",
          credentials: "include",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify(next),
        }).catch(() => {
          /* localStorage already saved — server sync best-effort */
        })
      }, 400)
    },
    [key, lsKey],
  )

  // Flush pending write on unmount so a fast unmount-after-drag
  // doesn't lose the latest value. `keepalive: true` lets the request
  // survive a page navigation; same-tab unmounts run normally.
  useEffect(() => {
    return () => {
      if (debounceRef.current) clearTimeout(debounceRef.current)
      if (hasPendingRef.current && pendingValueRef.current !== null) {
        fetch(`/api/v1/me/preferences/${encodeURIComponent(key)}`, {
          method: "PUT",
          credentials: "include",
          keepalive: true,
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify(pendingValueRef.current),
        }).catch(() => {})
      }
    }
  }, [key])

  return [value, set, { ready }]
}
