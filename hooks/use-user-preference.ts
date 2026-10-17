"use client"

import { useCallback, useEffect, useRef, useState } from "react"
import { apiFetch } from "@/lib/api-fetch"

/**
 * Per-user UI preference with cross-device sync via the server +
 * localStorage cache for instant first-paint reads.
 *
 * Flow on mount:
 *   1. Read localStorage synchronously — UI gets the user's last value
 *      immediately, no flash of default.
 *   2. Fetch /api/v1/me/preferences in the background; if the server
 *      has a different value, override and update localStorage — unless
 *      the local value is a change the server never confirmed (see below),
 *      which is sent again instead.
 *
 * Flow on set:
 *   1. Update React state + localStorage synchronously (snappy UI), and mark
 *      the key unsynced (`crewship.pref.<key>.unsynced`).
 *   2. Debounced PUT /api/v1/me/preferences/{key} — 400ms tail so a
 *      drag gesture doesn't hammer the server with intermediate values.
 *      A confirmed PUT clears the mark.
 *
 * A reload does not unmount React, so a change made inside the debounce
 * window used to be dropped, and the next load took the server's older
 * value over it. The pending write is now flushed with `keepalive` when the
 * page is hidden (`pagehide`, `visibilitychange`), and a write that still
 * did not land keeps its mark, so the next load re-sends it.
 *
 * The hook is generic; key namespace is the caller's responsibility.
 * Use dotted keys like "crews.bottomPanel.height" to keep things tidy.
 */
export function useUserPreference<T>(
  key: string,
  defaultValue: T,
): [T, (next: T) => void, { ready: boolean }] {
  const lsKey = `crewship.pref.${key}`
  const unsyncedKey = `${lsKey}.unsynced`

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
  // hasPendingRef is the source of truth — pendingValueRef can be a
  // legitimate `null` preference (or `undefined` for never-set), so we
  // must not gate the unmount flush on its value.
  const pendingValueRef = useRef<T | undefined>(undefined)
  const hasPendingRef = useRef(false)
  const valueRef = useRef(value)
  valueRef.current = value
  // Each write gets a number; only the newest confirmed write clears the mark.
  const writeSeq = useRef(0)

  const isUnsynced = useCallback(() => {
    try {
      return window.localStorage.getItem(unsyncedKey) != null
    } catch {
      return false
    }
  }, [unsyncedKey])

  const put = useCallback(
    (next: T | undefined, keepalive = false) => {
      const seq = ++writeSeq.current
      apiFetch(`/api/v1/me/preferences/${encodeURIComponent(key)}`, {
        method: "PUT",
        ...(keepalive ? { keepalive: true } : {}),
        headers: { "Content-Type": "application/json" },
        // next may legitimately be null — JSON.stringify serialises that as
        // the string "null", which is exactly what the server expects.
        body: JSON.stringify(next),
      })
        .then((res) => {
          if (res?.ok && seq === writeSeq.current && !hasPendingRef.current) {
            try {
              window.localStorage.removeItem(unsyncedKey)
            } catch {
              /* ignore */
            }
          }
        })
        .catch(() => {
          /* localStorage already saved — the mark makes the next load retry */
        })
    },
    [key, unsyncedKey],
  )

  // First paint: pull the server-side value. If it differs, override
  // local state. We never overwrite the server with a stale localStorage
  // value at startup — the server always wins on initial sync, except over
  // a local change it never confirmed, which is sent again.
  useEffect(() => {
    let cancelled = false
    apiFetch("/api/v1/me/preferences")
      .then((r) => (r.ok ? r.json() : null))
      .then((data: Record<string, unknown> | null) => {
        if (cancelled) return
        if (isUnsynced()) {
          if (!hasPendingRef.current) put(valueRef.current)
        } else if (data && key in data) {
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
  }, [key, lsKey, isUnsynced, put])

  const set = useCallback(
    (next: T) => {
      setLocal(next)
      try {
        window.localStorage.setItem(lsKey, JSON.stringify(next))
        window.localStorage.setItem(unsyncedKey, "1")
      } catch {
        /* ignore */
      }
      if (debounceRef.current) clearTimeout(debounceRef.current)
      pendingValueRef.current = next
      hasPendingRef.current = true
      debounceRef.current = setTimeout(() => {
        hasPendingRef.current = false
        pendingValueRef.current = undefined
        put(next)
      }, 400)
    },
    [lsKey, unsyncedKey, put],
  )

  // Send what is still waiting for the debounce now, with keepalive so the
  // request survives the page going away.
  const flush = useCallback(() => {
    if (debounceRef.current) clearTimeout(debounceRef.current)
    if (!hasPendingRef.current) return
    const pending = pendingValueRef.current
    hasPendingRef.current = false
    pendingValueRef.current = undefined
    put(pending, true)
  }, [put])

  // A reload or a closed tab: React does not unmount, so the unmount flush
  // below never runs. The page hiding is the last moment to send it.
  useEffect(() => {
    const onHide = () => flush()
    const onVisibility = () => {
      if (document.visibilityState === "hidden") flush()
    }
    window.addEventListener("pagehide", onHide)
    document.addEventListener("visibilitychange", onVisibility)
    return () => {
      window.removeEventListener("pagehide", onHide)
      document.removeEventListener("visibilitychange", onVisibility)
    }
  }, [flush])

  // Flush pending write on unmount so a fast unmount-after-drag
  // doesn't lose the latest value. `keepalive: true` lets the request
  // survive a page navigation; same-tab unmounts run normally.
  useEffect(() => {
    return () => flush()
  }, [flush])

  return [value, set, { ready }]
}
