"use client"

import { useEffect, useRef } from "react"

import { apiFetch } from "@/lib/api-fetch"
import {
  DEFAULT_SOUND_PREFERENCES,
  SOUND_PREFERENCES_EVENT,
  readSoundPreferences,
  saveSoundPreferences,
  type SoundPreferences,
} from "@/lib/notification-sounds"

export const SOUND_SYNC_DEBOUNCE_MS = 400

/** The per-user preference key for one workspace's sounds. Workspace ids are
 *  cuid-style, inside the server's [A-Za-z0-9._-]{1,64} key alphabet. */
export function soundPreferenceKey(workspaceId: string): string {
  return `notification-sounds.${workspaceId}`
}

/**
 * Keeps Settings › Notification sounds (and the user-menu switch) with the
 * signed-in user on the server, per workspace — the same scope the browser
 * store already uses (lib/notification-sounds.ts, keyed by user + workspace).
 *
 * They used to live in this browser only, so a second laptop started silent
 * and a cleared profile lost them. Now:
 *   - on load the server value wins and is written into the local store,
 *     which every reader (the sound player, the settings card, the menu)
 *     already follows through SOUND_PREFERENCES_EVENT;
 *   - with nothing on the server, a local choice that differs from the
 *     defaults seeds it;
 *   - every local change is PUT back after a short debounce.
 * The local store stays the source of truth for playback, so an unreachable
 * server costs nothing but the sync.
 *
 * Browser audio permission is NOT synced and cannot be: a new device still
 * needs one click before it may play.
 *
 * `enabled` is the caller's session gate — the preference routes are not on
 * the restricted allowlist.
 */
export function useSoundPreferencesSync(enabled: boolean, userId: string | undefined, workspaceId: string | null): void {
  const scope = userId && workspaceId ? JSON.stringify([userId, workspaceId]) : null
  const lastSentRef = useRef<string | null>(null)
  const applyingRef = useRef(false)

  useEffect(() => {
    if (!enabled || !scope || !workspaceId) return
    const key = soundPreferenceKey(workspaceId)
    const url = `/api/v1/me/preferences/${encodeURIComponent(key)}`
    lastSentRef.current = null
    let cancelled = false
    let ready = false
    let timer: ReturnType<typeof setTimeout> | null = null

    const put = (value: SoundPreferences) => {
      const body = JSON.stringify(value)
      if (body === lastSentRef.current) return
      lastSentRef.current = body
      apiFetch(url, { method: "PUT", headers: { "Content-Type": "application/json" }, body }).catch(() => {
        /* best-effort: the choice is already applied locally */
      })
    }

    const onChange = (e: Event) => {
      const detail = (e as CustomEvent<{ scope?: string }>).detail
      if (!ready || applyingRef.current || detail?.scope !== scope) return
      if (timer) clearTimeout(timer)
      timer = setTimeout(() => put(readSoundPreferences(scope)), SOUND_SYNC_DEBOUNCE_MS)
    }
    window.addEventListener(SOUND_PREFERENCES_EVENT, onChange)

    apiFetch("/api/v1/me/preferences")
      .then((r) => (r.ok ? r.json() : Promise.reject(new Error(`HTTP ${r.status}`))))
      .then((data: unknown) => {
        if (cancelled) return
        const remote = data && typeof data === "object" ? (data as Record<string, unknown>)[key] : undefined
        const local = readSoundPreferences(scope)
        if (remote && typeof remote === "object") {
          // saveSoundPreferences sanitises and announces the change; the
          // flag keeps that announcement from being echoed back as a PUT.
          applyingRef.current = true
          try {
            const applied = saveSoundPreferences(scope, remote as SoundPreferences)
            lastSentRef.current = JSON.stringify(applied)
          } finally {
            applyingRef.current = false
          }
        } else if (JSON.stringify(local) !== JSON.stringify(DEFAULT_SOUND_PREFERENCES)) {
          ready = true
          put(local)
        }
        ready = true
      })
      .catch(() => {
        if (cancelled) return
        // Offline or refused: keep the local value, sync only real changes.
        lastSentRef.current = JSON.stringify(readSoundPreferences(scope))
        ready = true
      })

    return () => {
      cancelled = true
      if (timer) clearTimeout(timer)
      window.removeEventListener(SOUND_PREFERENCES_EVENT, onChange)
    }
  }, [enabled, scope, workspaceId])
}
