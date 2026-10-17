"use client"

import { useEffect, useRef, useState, useSyncExternalStore } from "react"
import { useTheme } from "next-themes"

import { apiFetch } from "@/lib/api-fetch"
import { useAppearance } from "@/hooks/use-appearance"
import { APPEARANCE_PREFERENCE_KEY, emitAppearanceChange, parseAppearance, subscribeAppearance, type Appearance } from "@/lib/theme/appearance"
import {
  STYLE_PREFERENCE_KEY,
  parseStyleState,
  readStyleState,
  renderedStyle,
  serializeStyleState,
  writeStyleState,
} from "@/lib/theme/appearance-style"
import { createAppearanceOutbox, type AppearanceOutbox, type OutboxStatus } from "@/lib/theme/appearance-outbox"

/** Same tail as useUserPreference: a run of clicks through the accents is one write. */
export const APPEARANCE_SYNC_DEBOUNCE_MS = 400

function prefUrl(key: string): string {
  return `/api/v1/me/preferences/${encodeURIComponent(key)}`
}

// ── Sync status ("Not synced · Retry" in Settings › Appearance) ─────────
let syncStatus: OutboxStatus = "synced"
const statusListeners = new Set<() => void>()
let activeOutbox: AppearanceOutbox | null = null
function setSyncStatus(s: OutboxStatus): void {
  syncStatus = s
  for (const l of statusListeners) l()
}
function subscribeStatus(l: () => void): () => void {
  statusListeners.add(l)
  return () => void statusListeners.delete(l)
}
/** Whether this browser's appearance has reached the server. */
export function useAppearanceSyncStatus(): OutboxStatus {
  return useSyncExternalStore(subscribeStatus, () => syncStatus, () => "synced")
}
/** Sends writes the server refused again. */
export function retryAppearanceSync(): void {
  void activeOutbox?.retry()
}

function storageForOutbox() {
  return {
    getItem: (k: string) => localStorage.getItem(k),
    setItem: (k: string, v: string) => localStorage.setItem(k, v),
  }
}

function readStyleRaw(): string | null {
  const s = readStyleState()
  return s ? serializeStyleState(s) : null
}

/**
 * Keeps this browser's appearance (theme, accent, density, motion,
 * text size, sidebar mode) and its visual style in step with the signed-in
 * user's `appearance` and `appearance.style` preferences on the server.
 *
 * - Writes go through a persisted outbox (lib/theme/appearance-outbox) in a
 *   fixed order, `appearance` before `appearance.style`, with retries; a
 *   write the server refuses for good shows "Not synced · Retry" and never
 *   rolls the local look back.
 * - The style key is separate so a client that predates it cannot drop it.
 *   A style change still pending in the outbox beats the server's value; an
 *   empty server style is never seeded from a default — only a style the
 *   user picked is written.
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
  const { theme, accent, density, motion, textSize, sidebar, syncEnabled, setTheme, setAccent, setDensity, setMotion, setTextSize, setSidebar } = useAppearance()
  const { resolvedTheme } = useTheme()
  const active = enabled && syncEnabled

  // The look as it would be stored; null until next-themes knows the theme.
  const current: Appearance | null = theme ? { theme, accent, density, motion, textSize, sidebar } : null
  const serialized = current ? JSON.stringify(current) : null
  const currentRef = useRef(current)
  currentRef.current = current
  const style = useSyncExternalStore(subscribeAppearance, readStyleRaw, () => null)

  // What the server is known to hold. A local value equal to it is not sent:
  // that is how applying the server's own value does not echo it back.
  const lastSentRef = useRef<string | null>(null)
  const lastStyleSentRef = useRef<string | null>(null)
  const pendingRef = useRef<{ appearance: string | null; style: string | null }>({ appearance: null, style: null })
  const [ready, setReady] = useState(false)

  const outboxRef = useRef<AppearanceOutbox | null>(null)
  function outbox(): AppearanceOutbox {
    if (!outboxRef.current) {
      outboxRef.current = createAppearanceOutbox({
        storage: storageForOutbox(),
        send: (key, body, keepalive) =>
          apiFetch(prefUrl(key), { method: "PUT", keepalive, headers: { "Content-Type": "application/json" }, body }).then((r) => ({
            ok: Boolean(r?.ok),
            status: typeof r?.status === "number" ? r.status : r?.ok ? 200 : 0,
          })),
        schedule: (fn, ms) => {
          const t = setTimeout(() => void fn(), ms)
          return () => clearTimeout(t)
        },
        onStatus: setSyncStatus,
      })
    }
    return outboxRef.current
  }

  // Paint the style that can actually be shown (Harbor for an unknown style
  // or one without a palette for the current scheme), sync on or off.
  useEffect(() => {
    const scheme = resolvedTheme === "light" ? "light" : "dark"
    document.documentElement.dataset.style = renderedStyle(style ? parseStyleState(JSON.parse(style)) : null, scheme)
  }, [style, resolvedTheme])

  // Pull once per activation (mount, sign-in, or the switch turned on).
  useEffect(() => {
    if (!active) {
      setReady(false)
      // APR-05: sync off stops every request, a scheduled retry included.
      outboxRef.current?.pause()
      return
    }
    activeOutbox = outbox()
    outbox().resume()
    let cancelled = false
    apiFetch("/api/v1/me/preferences")
      .then((r) => (r.ok ? r.json() : Promise.reject(new Error(`HTTP ${r.status}`))))
      .then((data: unknown) => {
        if (cancelled) return
        const map = data && typeof data === "object" ? (data as Record<string, unknown>) : {}
        const remote = parseAppearance(map[APPEARANCE_PREFERENCE_KEY])
        const local = currentRef.current
        if (outbox().pending(APPEARANCE_PREFERENCE_KEY)) {
          // A local change the server has not received yet (sync was off, or
          // the write is still retrying) wins over the server's older value,
          // as it does for the style below; the outbox sends it next.
          lastSentRef.current = null
        } else if (Object.keys(remote).length > 0) {
          if (remote.theme && remote.theme !== local?.theme) setTheme(remote.theme)
          if (remote.accent && remote.accent !== local?.accent) setAccent(remote.accent)
          if (remote.density && remote.density !== local?.density) setDensity(remote.density)
          if (remote.motion && remote.motion !== local?.motion) setMotion(remote.motion)
          if (remote.textSize && remote.textSize !== local?.textSize) setTextSize(remote.textSize)
          if (remote.sidebar && remote.sidebar !== local?.sidebar) setSidebar(remote.sidebar)
          // A complete server value is what the server holds, so it is not
          // sent back. A partial one (an older build, or a field this build
          // rejected) is left unmarked: the push effect then writes the
          // completed value — server fields plus local ones — back up.
          const { theme: t, accent: a, density: d, motion: m, textSize: ts, sidebar: sb } = remote
          lastSentRef.current = t && a && d && m && ts && sb
            ? JSON.stringify({ theme: t, accent: a, density: d, motion: m, textSize: ts, sidebar: sb })
            : null
        } else {
          // Nothing on the server yet: leaving lastSent empty makes the push
          // effect seed it with this device's look.
          lastSentRef.current = null
        }
        // The style: a local change still waiting in the outbox wins; else the
        // server's value is adopted; an empty server is left empty unless the
        // user picked a style on this device (the push effect sends that).
        const remoteStyle = parseStyleState(map[STYLE_PREFERENCE_KEY])
        if (outbox().pending(STYLE_PREFERENCE_KEY)) {
          lastStyleSentRef.current = null
        } else if (remoteStyle) {
          const r = serializeStyleState(remoteStyle)
          if (r !== readStyleRaw()) {
            writeStyleState(remoteStyle)
            emitAppearanceChange()
          }
          lastStyleSentRef.current = r
        } else {
          lastStyleSentRef.current = null
        }
        setReady(true)
        void outbox().kick()
      })
      .catch(() => {
        if (cancelled) return
        // Offline, 5xx, or a response that is not JSON. Keep the local look and
        // treat it as already sent, so only a real change is written later.
        lastSentRef.current = currentRef.current ? JSON.stringify(currentRef.current) : null
        lastStyleSentRef.current = readStyleRaw()
        setReady(true)
      })
    const kick = () => void outbox().kick()
    window.addEventListener("online", kick)
    window.addEventListener("focus", kick)
    return () => {
      cancelled = true
      window.removeEventListener("online", kick)
      window.removeEventListener("focus", kick)
    }
    // The setters are stable (module functions, next-themes' memoised setter).
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [active])

  // Push local changes, debounced, both keys in one tick so the outbox can
  // keep its order. Each new value clears the previous timer, so a value that
  // was only on screen for one render is never sent.
  useEffect(() => {
    const appearanceOwed = Boolean(serialized) && serialized !== lastSentRef.current
    const styleOwed = Boolean(style) && style !== lastStyleSentRef.current
    if (!active || !ready || (!appearanceOwed && !styleOwed)) {
      // Nothing owed: a value that was replaced before its timer fired, or a
      // switch turned off, must not be flushed on unmount either.
      pendingRef.current = { appearance: null, style: null }
      return
    }
    pendingRef.current = { appearance: appearanceOwed ? serialized : null, style: styleOwed ? style : null }
    const t = setTimeout(() => {
      const ob = outbox()
      if (appearanceOwed && serialized) {
        lastSentRef.current = serialized
        ob.enqueue(APPEARANCE_PREFERENCE_KEY, serialized)
      }
      if (styleOwed && style) {
        lastStyleSentRef.current = style
        ob.enqueue(STYLE_PREFERENCE_KEY, style)
      }
      pendingRef.current = { appearance: null, style: null }
      void ob.flush()
    }, APPEARANCE_SYNC_DEBOUNCE_MS)
    return () => clearTimeout(t)
  }, [active, ready, serialized, style])

  // A change made just before the shell unmounts (sign-out, a hard
  // navigation) still reaches the server: keepalive lets the request outlive
  // the page; anything unsent stays in the outbox for the next load.
  useEffect(() => {
    return () => {
      const p = pendingRef.current
      if (p.appearance || p.style) {
        const ob = outbox()
        if (p.appearance) ob.enqueue(APPEARANCE_PREFERENCE_KEY, p.appearance)
        if (p.style) ob.enqueue(STYLE_PREFERENCE_KEY, p.style)
        ob.flushKeepalive()
      }
      pendingRef.current = { appearance: null, style: null }
      if (activeOutbox === outboxRef.current) activeOutbox = null
    }
  }, [])
}

