"use client"

import { useCallback, useEffect, useRef, useState } from "react"
import { apiFetch } from "@/lib/api-fetch"
import * as auth from "@/hooks/use-auth"

/**
 * Whose device copy this is. A user id once the session is known; null while
 * an AuthProvider is still settling or nobody is signed in (no device copy is
 * read or written then); "" with no AuthProvider at all (a surface or test
 * outside the signed-in app), which keeps the unscoped legacy key.
 *
 * Read through the namespace with a fallback because many component tests
 * mock "@/hooks/use-auth" with only the exports they use, and a missing mock
 * export throws on access. The hooks called here are plain useContext reads,
 * so a render that falls back still calls hooks in a stable order.
 */
function usePreferenceOwner(): string | null {
  let id: string | null = null
  try {
    id = auth.useSessionSafe().data?.user?.id ?? null
  } catch {
    /* mocked without useSessionSafe */
  }
  if (id) return id
  try {
    return auth.useAuthAvailable() ? null : ""
  } catch {
    return ""
  }
}

function deviceKey(owner: string | null, key: string): string | null {
  if (owner == null) return null
  if (owner === "") return `crewship.pref.${key}`
  return `crewship.pref.u.${encodeURIComponent(owner)}.${key}`
}

function readDevice<T>(lsKey: string | null, fallback: T): T {
  if (lsKey == null || typeof window === "undefined") return fallback
  try {
    const raw = window.localStorage.getItem(lsKey)
    if (raw == null) return fallback
    return JSON.parse(raw) as T
  } catch {
    return fallback
  }
}

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
 * The device copy belongs to one signed-in user
 * (`crewship.pref.u.<user id>.<key>`). Two accounts in one browser used to
 * share `crewship.pref.<key>`: the next account's first paint showed the
 * previous account's value, the server had nothing to override it with, and
 * its next write uploaded the previous account's private values (recently
 * opened artifacts) into its own server preference. Nothing is read from or
 * written to the device while the session is unknown, the old unscoped copy is
 * dropped once it is, and a write made for one user is never sent after the
 * session changed to another. Outside an AuthProvider the unscoped key stays.
 *
 * The hook is generic; key namespace is the caller's responsibility.
 * Use dotted keys like "crews.bottomPanel.height" to keep things tidy.
 */
export function useUserPreference<T>(
  key: string,
  defaultValue: T,
): [T, (next: T) => void, { ready: boolean }] {
  const owner = usePreferenceOwner()
  const lsKey = deviceKey(owner, key)
  const unsyncedKey = lsKey == null ? null : `${lsKey}.unsynced`
  // The current device copy; a write is sent only while the copy it was made
  // for is still this one. Kept in render so that an effect cleanup running on
  // the session change (the unmount flush below) already sees the new owner.
  const lsKeyRef = useRef(lsKey)
  lsKeyRef.current = lsKey

  const [value, setLocal] = useState<T>(() => readDevice(lsKey, defaultValue))
  const [loadedKey, setLoadedKey] = useState(lsKey)
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

  // The session changed (signed in, out, or to another account): drop the
  // previous account's value and its pending write before anything is sent.
  // That write stays marked unconfirmed in its own account's copy, so it is
  // sent again the next time that account signs in here.
  if (loadedKey !== lsKey) {
    setLoadedKey(lsKey)
    setLocal(readDevice(lsKey, defaultValue))
    setReady(false)
    if (debounceRef.current) clearTimeout(debounceRef.current)
    hasPendingRef.current = false
    pendingValueRef.current = undefined
  }

  // The unscoped copy predates per-user keys and may be another account's.
  useEffect(() => {
    if (!owner) return
    try {
      window.localStorage.removeItem(`crewship.pref.${key}`)
      window.localStorage.removeItem(`crewship.pref.${key}.unsynced`)
    } catch {
      /* ignore */
    }
  }, [owner, key])

  const isUnsynced = useCallback(() => {
    if (unsyncedKey == null) return false
    try {
      return window.localStorage.getItem(unsyncedKey) != null
    } catch {
      return false
    }
  }, [unsyncedKey])

  const put = useCallback(
    (next: T | undefined, keepalive = false) => {
      // Written for another session's device copy: not this user's to send.
      if (lsKeyRef.current !== lsKey) return
      const seq = ++writeSeq.current
      apiFetch(`/api/v1/me/preferences/${encodeURIComponent(key)}`, {
        method: "PUT",
        ...(keepalive ? { keepalive: true } : {}),
        // Never refresh-and-retry: the refresh can outlast a sign-out, and the
        // retry would carry the next person's cookies. A 401 leaves the
        // unsynced mark, so this owner's next load sends the value again.
        skipRefresh: true,
        headers: { "Content-Type": "application/json" },
        // next may legitimately be null — JSON.stringify serialises that as
        // the string "null", which is exactly what the server expects.
        body: JSON.stringify(next),
      })
        .then((res) => {
          if (res?.ok && unsyncedKey != null && seq === writeSeq.current && !hasPendingRef.current) {
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
    [key, lsKey, unsyncedKey],
  )

  // First paint: pull the server-side value. If it differs, override
  // local state. We never overwrite the server with a stale localStorage
  // value at startup — the server always wins on initial sync, except over
  // a local change it never confirmed, which is sent again.
  useEffect(() => {
    let cancelled = false
    // While the session is unknown the server's answer is still applied (it
    // is the cookie's user), only never stored on the device; it runs again
    // once the owner is known.
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
              if (lsKey != null) window.localStorage.setItem(lsKey, JSON.stringify(remote))
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
        if (lsKey != null && unsyncedKey != null) {
          window.localStorage.setItem(lsKey, JSON.stringify(next))
          window.localStorage.setItem(unsyncedKey, "1")
        }
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
