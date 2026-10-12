"use client"

import { useEffect, useRef, useState } from "react"

import { apiFetch } from "@/lib/api-fetch"
import { useAppearance } from "@/hooks/use-appearance"
import { APPEARANCE_PREFERENCE_KEY, parseAppearance, type Appearance } from "@/lib/theme/appearance"

/** Same tail as useUserPreference: a run of clicks through the accents is one write. */
export const APPEARANCE_SYNC_DEBOUNCE_MS = 400

const PUT_URL = `/api/v1/me/preferences/${encodeURIComponent(APPEARANCE_PREFERENCE_KEY)}`

function put(body: string, keepalive = false): void {
  apiFetch(PUT_URL, {
    method: "PUT",
    keepalive,
    headers: { "Content-Type": "application/json" },
    body,
  }).catch(() => {
    /* best-effort: the choice is already applied and stored locally */
  })
}

/**
 * Keeps this browser's appearance (theme, accent, base, density, motion) in step
 * with the signed-in user's `appearance` preference on the server.
 *
 * - On activation the server value wins: it is applied field by field, so a
 *   laptop that just signed in looks like the desktop the user set up. When
 *   the server has nothing yet, this device's current look seeds it.
 * - After that, any local change — from the top-bar menu, Settings ›
 *   Appearance, or another tab — is written back after a short debounce. The
 *   pickers never call the server themselves; this hook watches the values
 *   they change, so there is exactly one writer.
 * - The sync switch is per device (lib/theme/appearance.ts). Off means no
 *   request at all, in either direction.
 * - A failure never breaks anything: an unreachable server leaves the local
 *   look alone, and nothing is pushed until the user actually changes
 *   something, so an offline start cannot overwrite the server with stale
 *   local state.
 *
 * Why not useUserPreference: that hook fetches on mount unconditionally (the
 * sync switch has to be able to stop it) and falls back to its own
 * localStorage cache when the server has no row or cannot be reached, which
 * makes "server wins" indistinguishable from "an old cached copy wins".
 * Here the source of truth for the local look stays where the pickers and the
 * boot scripts already read it.
 *
 * `enabled` is the caller's gate on the session: pass false for anything but a
 * trusted session. The preference routes are not on the restricted allowlist
 * (internal/api/restricted_surfaces.go), so a restricted session must never
 * send them.
 */
export function useAppearanceSync(enabled: boolean): void {
  const { theme, accent, base, density, motion, syncEnabled, setTheme, setAccent, setBase, setDensity, setMotion } = useAppearance()
  const active = enabled && syncEnabled

  // The look as it would be stored; null until next-themes knows the theme.
  const current: Appearance | null = theme ? { theme, accent, base, density, motion } : null
  const serialized = current ? JSON.stringify(current) : null
  const currentRef = useRef(current)
  currentRef.current = current

  // What the server is known to hold. A local value equal to it is not sent:
  // that is how applying the server's own value does not echo it back.
  const lastSentRef = useRef<string | null>(null)
  const pendingRef = useRef<string | null>(null)
  const [ready, setReady] = useState(false)

  // Pull once per activation (mount, sign-in, or the switch turned on).
  useEffect(() => {
    if (!active) {
      setReady(false)
      return
    }
    let cancelled = false
    apiFetch("/api/v1/me/preferences")
      .then((r) => (r.ok ? r.json() : Promise.reject(new Error(`HTTP ${r.status}`))))
      .then((data: unknown) => {
        if (cancelled) return
        const map = data && typeof data === "object" ? (data as Record<string, unknown>) : {}
        const remote = parseAppearance(map[APPEARANCE_PREFERENCE_KEY])
        const local = currentRef.current
        if (Object.keys(remote).length > 0) {
          if (remote.theme && remote.theme !== local?.theme) setTheme(remote.theme)
          if (remote.accent && remote.accent !== local?.accent) setAccent(remote.accent)
          if (remote.base && remote.base !== local?.base) setBase(remote.base)
          if (remote.density && remote.density !== local?.density) setDensity(remote.density)
          if (remote.motion && remote.motion !== local?.motion) setMotion(remote.motion)
          // A complete server value is what the server holds, so it is not
          // sent back. A partial one (an older build, or a field this build
          // rejected) is left unmarked: the push effect then writes the
          // completed value — server fields plus local ones — back up.
          const { theme: t, accent: a, base: b, density: d, motion: m } = remote
          lastSentRef.current = t && a && b && d && m ? JSON.stringify({ theme: t, accent: a, base: b, density: d, motion: m }) : null
        } else {
          // Nothing on the server yet: leaving lastSent empty makes the push
          // effect seed it with this device's look.
          lastSentRef.current = null
        }
        setReady(true)
      })
      .catch(() => {
        if (cancelled) return
        // Offline, 5xx, or a response that is not JSON. Keep the local look and
        // treat it as already sent, so only a real change is written later.
        lastSentRef.current = currentRef.current ? JSON.stringify(currentRef.current) : null
        setReady(true)
      })
    return () => {
      cancelled = true
    }
    // The setters are stable (module functions, next-themes' memoised setter).
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [active])

  // Push local changes, debounced. Each new value clears the previous timer,
  // so a value that was only on screen for one render is never sent.
  useEffect(() => {
    if (!active || !ready || !serialized || serialized === lastSentRef.current) {
      // Nothing owed: a value that was replaced before its timer fired, or a
      // switch turned off, must not be flushed on unmount either.
      pendingRef.current = null
      return
    }
    pendingRef.current = serialized
    const t = setTimeout(() => {
      pendingRef.current = null
      lastSentRef.current = serialized
      put(serialized)
    }, APPEARANCE_SYNC_DEBOUNCE_MS)
    return () => clearTimeout(t)
  }, [active, ready, serialized])

  // A change made just before the shell unmounts (sign-out, a hard
  // navigation) still reaches the server: keepalive lets the request outlive
  // the page.
  useEffect(() => {
    return () => {
      if (pendingRef.current) put(pendingRef.current, true)
      pendingRef.current = null
    }
  }, [])
}
