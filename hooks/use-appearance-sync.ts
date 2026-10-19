"use client"

import { useEffect, useMemo, useRef, useState, useSyncExternalStore } from "react"
import { useTheme } from "next-themes"

import { apiFetch } from "@/lib/api-fetch"
import { useAppearance } from "@/hooks/use-appearance"
import { DEFAULT_ACCENT } from "@/lib/theme/accents"
import {
  APPEARANCE_PREFERENCE_KEY,
  DEFAULT_THEME,
  emitAppearanceChange,
  parseAppearance,
  setAccentChoice,
  setDensity,
  setMotion,
  setSidebarChoice,
  setTextSize,
  subscribeAppearance,
  type Appearance,
} from "@/lib/theme/appearance"
import { readExplicit, readInherited, readInheritedRaw, markExplicit, setInherited, type ExplicitField } from "@/lib/theme/appearance-explicit"
import { inheritedLook, parseWorkspaceAppearanceDefault, workspaceDefaultKey } from "@/lib/theme/appearance-inherit"
import {
  STYLE_PREFERENCE_KEY,
  parseStyleState,
  readStyleState,
  applyStyleAttributes,
  clearStyleState,
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

/** Nothing chosen: what both preference keys hold for a user with no explicit value. */
const EMPTY = "{}"

const APPEARANCE_FIELDS = ["theme", "accent", "density", "motion", "textSize", "sidebar"] as const satisfies readonly (keyof Appearance & ExplicitField)[]

/** The fields of the look the user chose; the rest is inherited and not sent. */
function explicitOnly(a: Appearance, explicit: ReadonlySet<ExplicitField>): Partial<Appearance> {
  const out: Record<string, string> = {}
  for (const f of APPEARANCE_FIELDS) if (explicit.has(f)) out[f] = a[f]
  return out as Partial<Appearance>
}

/** Fixed field order, so two equal looks are equal strings. */
function canonicalAppearance(a: Partial<Appearance>): string {
  const out: Record<string, string> = {}
  for (const f of APPEARANCE_FIELDS) if (a[f] !== undefined) out[f] = a[f] as string
  return JSON.stringify(out)
}

function readStyleRaw(): string | null {
  const s = readStyleState()
  return s ? serializeStyleState(s) : null
}

/**
 * Keeps this browser's appearance (theme, accent, density, motion,
 * text size, sidebar mode) and its visual style in step with the signed-in
 * user's `appearance` and `appearance.style` preferences on the server, and
 * applies the workspace's default look to whatever the user has not chosen.
 *
 * Explicit vs inherited (WSD-02): a value is explicit only when the user
 * changed it (lib/theme/appearance-explicit). Sync writes explicit values
 * only, so an untouched device never fills the server with its defaults, and
 * a workspace default keeps applying to everyone who did not choose.
 *
 * - Writes go through a persisted outbox (lib/theme/appearance-outbox) in a
 *   fixed order, `appearance` before `appearance.style`, with retries; a
 *   write the server refuses for good shows "Not synced · Retry" and never
 *   rolls the local look back.
 * - The style key is separate so a client that predates it cannot drop it.
 *   A style change still pending in the outbox beats the server's value.
 *   Both keys hold explicit values only; "nothing chosen" is `{}`.
 *
 * - On activation the server value wins: each field it holds is applied and
 *   becomes explicit on this device (the user chose it elsewhere).
 * - Then, for every theme/accent/style the user has not chosen, the
 *   workspace default is applied (WSD-03), recomputed when the default
 *   changes or the user switches workspace; with no default the value falls
 *   back to the built-in one, but only if it had come from a default (a
 *   device's own old look is left alone). `workspaceDefault` is the raw
 *   `appearance_default` of the current workspace: undefined while unknown
 *   (nothing is applied), null/{} when it has none.
 * - The sync switch is per device (lib/theme/appearance.ts). Off means no
 *   request at all, in either direction; local explicit values still win
 *   and the rest is inherited (WSD-04).
 * - After that, any local change — from the top-bar menu, Settings ›
 *   Appearance, or another tab — is written back after a short debounce. The
 *   pickers never call the server themselves; this hook watches the values
 *   they change, so there is exactly one writer.
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
export function useAppearanceSync(enabled: boolean, workspaceDefault?: unknown): void {
  const { theme, accent, density, motion, textSize, sidebar, syncEnabled, explicit } = useAppearance()
  const { resolvedTheme, setTheme: setNextTheme } = useTheme()
  const active = enabled && syncEnabled

  // The explicit part of the look as it would be stored; null until
  // next-themes knows the theme.
  const full: Appearance | null = theme ? { theme, accent, density, motion, textSize, sidebar } : null
  const current = full ? explicitOnly(full, explicit) : null
  const serialized = current ? canonicalAppearance(current) : null
  const fullRef = useRef(full)
  fullRef.current = full
  const serializedRef = useRef(serialized)
  serializedRef.current = serialized
  const styleRaw = useSyncExternalStore(subscribeAppearance, readStyleRaw, () => null)
  // What goes to appearance.style: the style the user picked, else nothing.
  const style = explicit.has("style") && styleRaw ? styleRaw : EMPTY
  const styleOutRef = useRef(style)
  styleOutRef.current = style
  const inheritedRaw = useSyncExternalStore(subscribeAppearance, readInheritedRaw, () => "[]")

  const wsDefault = useMemo(() => parseWorkspaceAppearanceDefault(workspaceDefault), [workspaceDefault])
  const wsKey = workspaceDefault === undefined ? null : workspaceDefaultKey(wsDefault)

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
    applyStyleAttributes(document.documentElement, styleRaw ? parseStyleState(JSON.parse(styleRaw)) : null, scheme)
  }, [styleRaw, resolvedTheme])

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
        const local = fullRef.current
        if (outbox().pending(APPEARANCE_PREFERENCE_KEY)) {
          // A local change the server has not received yet (sync was off, or
          // the write is still retrying) wins over the server's older value,
          // as it does for the style below; the outbox sends it next.
          lastSentRef.current = null
        } else {
          // Whatever the server holds is something the user chose on some
          // device: apply it and keep it as this device's explicit value.
          // Raw setters: applying must not look like a local change.
          if (remote.theme) { markExplicit("theme"); if (remote.theme !== local?.theme) setNextTheme(remote.theme) }
          if (remote.accent) { markExplicit("accent"); if (remote.accent !== local?.accent) setAccentChoice(remote.accent) }
          if (remote.density) { markExplicit("density"); if (remote.density !== local?.density) setDensity(remote.density) }
          if (remote.motion) { markExplicit("motion"); if (remote.motion !== local?.motion) setMotion(remote.motion) }
          if (remote.textSize) { markExplicit("textSize"); if (remote.textSize !== local?.textSize) setTextSize(remote.textSize) }
          if (remote.sidebar) { markExplicit("sidebar"); if (remote.sidebar !== local?.sidebar) setSidebarChoice(remote.sidebar) }
          // The server's value is what the server holds, so it is not sent
          // back; a local explicit field it lacks is, by the push effect.
          lastSentRef.current = canonicalAppearance(remote)
        }
        // The style: a local change still waiting in the outbox wins; else the
        // server's value is adopted; an empty server stays empty — only a
        // style the user picked is ever written.
        const remoteStyle = parseStyleState(map[STYLE_PREFERENCE_KEY])
        if (outbox().pending(STYLE_PREFERENCE_KEY)) {
          lastStyleSentRef.current = null
        } else if (remoteStyle) {
          const r = serializeStyleState(remoteStyle)
          markExplicit("style")
          if (r !== readStyleRaw()) {
            writeStyleState(remoteStyle)
            emitAppearanceChange()
          }
          lastStyleSentRef.current = r
        } else {
          lastStyleSentRef.current = EMPTY
        }
        setReady(true)
        void outbox().kick()
      })
      .catch(() => {
        if (cancelled) return
        // Offline, 5xx, or a response that is not JSON. Keep the local look and
        // treat it as already sent, so only a real change is written later.
        lastSentRef.current = serializedRef.current
        lastStyleSentRef.current = styleOutRef.current
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

  // The workspace default (WSD-03/04): applied to every theme/accent/style the
  // user has not chosen. Runs with sync on or off; with sync on it waits for
  // the pull, so a value the user chose on another device is not painted over
  // first.
  const inheritReady = !active || ready
  useEffect(() => {
    if (!enabled || wsKey === null || !inheritReady || !theme) return
    const ex = readExplicit()
    const inh = readInherited()
    const look = inheritedLook(wsDefault)
    if (!ex.has("theme")) {
      if (wsDefault.theme) {
        setInherited("theme", true)
        if (theme !== wsDefault.theme) setNextTheme(wsDefault.theme)
      } else if (inh.has("theme")) {
        setInherited("theme", false)
        if (theme !== DEFAULT_THEME) setNextTheme(DEFAULT_THEME)
      }
    }
    if (!ex.has("accent")) {
      if (wsDefault.accent) {
        setInherited("accent", true)
        if (accent !== wsDefault.accent) setAccentChoice(wsDefault.accent)
      } else if (inh.has("accent")) {
        setInherited("accent", false)
        if (accent !== DEFAULT_ACCENT) setAccentChoice(DEFAULT_ACCENT)
      }
    }
    if (!ex.has("style")) {
      if (look.style) {
        setInherited("style", true)
        if (serializeStyleState(look.style) !== readStyleRaw()) {
          writeStyleState(look.style)
          emitAppearanceChange()
        }
      } else if (inh.has("style")) {
        setInherited("style", false)
        if (readStyleRaw() !== null) {
          clearStyleState()
          emitAppearanceChange()
        }
      }
    }
    // theme and accent are read, not reacted to: a change of either is the
    // user's (explicit) or ours, and the effect must not chase it.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [enabled, wsKey, inheritReady, explicit, inheritedRaw, theme === undefined])

  // Push local changes, debounced, both keys in one tick so the outbox can
  // keep its order. Each new value clears the previous timer, so a value that
  // was only on screen for one render is never sent.
  useEffect(() => {
    const appearanceOwed = Boolean(serialized) && serialized !== lastSentRef.current
    const styleOwed = style !== lastStyleSentRef.current
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
      if (styleOwed) {
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

