/**
 * Appearance — the per-browser display choices beside the accent: theme,
 * density and reduced motion, plus whether they follow the user across
 * devices.
 *
 * Where things live:
 *   - lib/theme/accents.ts      the accent registry and its own boot script.
 *   - this file                 density, motion and the sync switch: storage
 *                               keys, the <html> attributes, a tiny external
 *                               store every picker reads through, and the
 *                               pre-paint boot script.
 *   - hooks/use-appearance.ts   the React face of this store (plus the theme,
 *                               which next-themes owns).
 *   - hooks/use-appearance-sync.ts  the server round-trip, mounted once in
 *                               the authenticated shell.
 *   - app/globals.css           what `data-density` and `data-motion` do.
 *
 * Why an external store rather than component state: the same choice is shown
 * by the top-bar menu and by Settings › Appearance at the same time, and the
 * sync component has to see a change made in either. Per-component useState
 * (what useAccent used to keep) only heard about another TAB's change, through
 * the storage event — never about the picker one menu over.
 *
 * Values are re-read from localStorage on every snapshot instead of being
 * cached in module scope: it costs a few string reads per render, and it means
 * a test (or a second tab) that changes storage underneath is never shadowed
 * by a stale copy.
 */

import { ACCENT_STORAGE_KEY, DEFAULT_ACCENT, applyAccent, isAccentChoice, type AccentChoice } from "@/lib/theme/accents"
import { STYLE_BOOT_SNIPPET, STYLE_STORAGE_KEY } from "@/lib/theme/appearance-style"


export const THEME_CHOICES = ["dark", "light", "system"] as const
export type ThemeChoice = (typeof THEME_CHOICES)[number]

export const DENSITIES = ["comfortable", "compact"] as const
export type Density = (typeof DENSITIES)[number]

/** "system" follows the OS setting; "on" always reduces; "off" never does. */
export const MOTION_CHOICES = ["system", "on", "off"] as const
export type MotionChoice = (typeof MOTION_CHOICES)[number]

/** Interface text size. Every size in the app is rem-based, so this scales
 *  text, spacing and controls together — a zoom, not a font swap.
 *
 *  Each stored value keeps an absolute meaning across releases, so a choice
 *  saved (or synced) by an older build renders the same: "small" is Compact
 *  (13 px body text, the Harbor 2 size), "default" is Standard (14 px),
 *  "large" is 112.5 %. Only a brand-new device starts at Compact; see
 *  migrateDeviceAppearance. */
export const TEXT_SIZES = ["small", "default", "large"] as const
export type TextSize = (typeof TEXT_SIZES)[number]

/** How the left rail behaves; the same three modes components/ui/sidebar.tsx
 *  stores under its own key (the sidebar keeps owning that state — this is
 *  the copy that syncs). */
export const SIDEBAR_CHOICES = ["hover", "collapsed", "pinned"] as const
export type SidebarChoice = (typeof SIDEBAR_CHOICES)[number]

export const DEFAULT_DENSITY: Density = "comfortable"
/** What a device with no stored size shows: Compact (Harbor 2). */
export const DEFAULT_TEXT_SIZE: TextSize = "small"
/** What a device that ran an earlier build keeps when it never stored one. */
export const LEGACY_TEXT_SIZE: TextSize = "default"
/** next-themes' storage key and default. A new device follows the system
 *  (Harbor 2); one that ran an earlier build — whose default was Dark —
 *  keeps Dark unless it stored something else. */
export const THEME_STORAGE_KEY = "theme"
export const DEFAULT_THEME: ThemeChoice = "system"
export const LEGACY_THEME: ThemeChoice = "dark"
// Pinned open: a new person sees the names of every place (owner decision
// 2026-10-18, v67). A tablet still shows the rail (components/ui/sidebar.tsx).
export const DEFAULT_SIDEBAR: SidebarChoice = "pinned"
export const DEFAULT_MOTION: MotionChoice = "system"

/** localStorage keys; per browser, like the theme and the accent. */
export const DENSITY_STORAGE_KEY = "crewship-density"
export const MOTION_STORAGE_KEY = "crewship-motion"
export const TEXT_SIZE_STORAGE_KEY = "crewship-text-size"
/** components/ui/sidebar.tsx's own key; read here, written through it. */
export const SIDEBAR_STORAGE_KEY = "crewship_sidebar_mode"
/** The sync switch is deliberately per DEVICE: "this laptop keeps its own
 *  look" is exactly the choice it exists for, so it can never itself sync. */
export const APPEARANCE_SYNC_STORAGE_KEY = "crewship-appearance-sync"
/** Marks a device whose stored look has been brought to the current
 *  defaults (migrateDeviceAppearance); bumped when a default changes. */
export const APPEARANCE_VERSION_STORAGE_KEY = "crewship-appearance-version"
export const APPEARANCE_VERSION = "2"

/** The one key in the per-user preference store (GET /api/v1/me/preferences). */
export const APPEARANCE_PREFERENCE_KEY = "appearance"

/** What syncs across devices. */
export interface Appearance {
  theme: ThemeChoice
  accent: AccentChoice
  density: Density
  motion: MotionChoice
  /** Optional on the wire: absent in values saved before it existed. */
  textSize: TextSize
  sidebar: SidebarChoice
}

export function isThemeChoice(v: unknown): v is ThemeChoice {
  return typeof v === "string" && (THEME_CHOICES as readonly string[]).includes(v)
}
export function isDensity(v: unknown): v is Density {
  return typeof v === "string" && (DENSITIES as readonly string[]).includes(v)
}
export function isMotionChoice(v: unknown): v is MotionChoice {
  return typeof v === "string" && (MOTION_CHOICES as readonly string[]).includes(v)
}
export function isTextSize(v: unknown): v is TextSize {
  return typeof v === "string" && (TEXT_SIZES as readonly string[]).includes(v)
}
export function isSidebarChoice(v: unknown): v is SidebarChoice {
  return typeof v === "string" && (SIDEBAR_CHOICES as readonly string[]).includes(v)
}

/**
 * The valid fields of a stored appearance. The server value was written by
 * some other build of this UI, maybe an older or newer one, so each field is
 * judged on its own: an unknown accent from a newer release drops that one
 * field, not the user's theme along with it.
 */
export function parseAppearance(value: unknown): Partial<Appearance> {
  if (!value || typeof value !== "object" || Array.isArray(value)) return {}
  const v = value as Record<string, unknown>
  const out: Partial<Appearance> = {}
  if (isThemeChoice(v.theme)) out.theme = v.theme
  if (isAccentChoice(v.accent)) out.accent = v.accent
  if (isDensity(v.density)) out.density = v.density
  if (isMotionChoice(v.motion)) out.motion = v.motion
  if (isTextSize(v.textSize)) out.textSize = v.textSize
  if (isSidebarChoice(v.sidebar)) out.sidebar = v.sidebar
  return out
}

// ── <html> attributes ───────────────────────────────────────────────

/** Comfortable is the absence of the attribute, so CSS only ever has to
 *  describe the compact case. */
export function applyDensityAttr(d: Density): void {
  if (d === "compact") document.documentElement.dataset.density = "compact"
  else delete document.documentElement.dataset.density
}

/** "reduce" switches motion off; "full" tells the stylesheet to ignore the
 *  OS reduce-motion query; System is the absence of the attribute. */
export function applyMotionAttr(m: MotionChoice): void {
  if (m === "on") document.documentElement.dataset.motion = "reduce"
  else if (m === "off") document.documentElement.dataset.motion = "full"
  else delete document.documentElement.dataset.motion
}

/** Standard (14 px) is the absence of the attribute. */
export function applyTextSizeAttr(t: TextSize): void {
  if (t === "default") delete document.documentElement.dataset.textSize
  else document.documentElement.dataset.textSize = t
}

/** The motion/react MotionConfig setting for a choice. */
export function motionConfigValue(m: MotionChoice): "user" | "always" | "never" {
  return m === "on" ? "always" : m === "off" ? "never" : "user"
}

// ── Device migration ────────────────────────────────────────────────

/**
 * Brings this browser's stored look to the current defaults ONCE, without
 * changing what anyone sees. A device that ran an earlier build (it holds
 * any Crewship key) but never stored a text size or a theme keeps that
 * build's defaults, Standard and Dark; a brand-new device keeps nothing and
 * so gets Small text and the system theme. A stored
 * choice is never touched. The boot script below does the same before first
 * paint (the only place it runs in the app); this is its testable twin.
 * Storage that throws or cannot be listed leaves everything as it is.
 */
export function migrateDeviceAppearance(): void {
  try {
    const s = localStorage
    if (s.getItem(APPEARANCE_VERSION_STORAGE_KEY) === APPEARANCE_VERSION) return
    let used = false
    const n = typeof s.length === "number" ? s.length : 0
    for (let i = 0; i < n && typeof s.key === "function"; i++) {
      const k = s.key(i)
      if (k && k !== APPEARANCE_VERSION_STORAGE_KEY && (k === "theme" || k.indexOf("crewship") === 0)) {
        used = true
        break
      }
    }
    if (used && s.getItem(TEXT_SIZE_STORAGE_KEY) === null) s.setItem(TEXT_SIZE_STORAGE_KEY, LEGACY_TEXT_SIZE)
    if (used && s.getItem(THEME_STORAGE_KEY) === null) s.setItem(THEME_STORAGE_KEY, LEGACY_THEME)
    s.setItem(APPEARANCE_VERSION_STORAGE_KEY, APPEARANCE_VERSION)
  } catch {
    // Private mode or blocked storage: nothing stored, nothing to keep.
  }
}

// ── Store ───────────────────────────────────────────────────────────

const listeners = new Set<() => void>()
// Last value written, for a browser whose storage throws (private mode,
// blocked site data): the choice still holds for this page, as applyAccent's
// does. Consulted ONLY when storage throws, never when it is merely empty.
const memory = new Map<string, string>()

function read<T extends string>(key: string, valid: (v: unknown) => v is T, fallback: T): T {
  try {
    const v = localStorage.getItem(key)
    return valid(v) ? v : fallback
  } catch {
    const m = memory.get(key)
    return valid(m) ? m : fallback
  }
}

function write(key: string, value: string): void {
  memory.set(key, value)
  try {
    localStorage.setItem(key, value)
  } catch {
    // Private mode or blocked storage: the choice lasts for this page only.
  }
}

function emit(): void {
  for (const l of listeners) l()
}

/** Tells every appearance reader that something changed in this tab — for
 *  stores that live beside this one (lib/theme/appearance-style). */
export function emitAppearanceChange(): void {
  emit()
}

function onStorage(e: StorageEvent): void {
  // Another tab changed a choice. Paint it here too, then let readers re-read.
  if (e.key === ACCENT_STORAGE_KEY && isAccentChoice(e.newValue)) document.documentElement.dataset.accent = e.newValue
  else if (e.key === TEXT_SIZE_STORAGE_KEY) applyTextSizeAttr(readTextSize())
  else if (e.key === SIDEBAR_STORAGE_KEY) {
    // Nothing to paint: the sidebar bridge reads the new mode from the store.
  }
  else if (e.key === DENSITY_STORAGE_KEY) applyDensityAttr(readDensity())
  else if (e.key === MOTION_STORAGE_KEY) applyMotionAttr(readMotion())
  else if (e.key === STYLE_STORAGE_KEY) {
    // The style attribute is painted by AppearanceStyleSync from the new value.
  }
  else if (e.key !== APPEARANCE_SYNC_STORAGE_KEY) return
  emit()
}

/** useSyncExternalStore subscribe: one storage listener however many readers. */
export function subscribeAppearance(listener: () => void): () => void {
  if (listeners.size === 0 && typeof window !== "undefined") window.addEventListener("storage", onStorage)
  listeners.add(listener)
  return () => {
    listeners.delete(listener)
    if (listeners.size === 0 && typeof window !== "undefined") window.removeEventListener("storage", onStorage)
  }
}

export function readDensity(): Density {
  return read(DENSITY_STORAGE_KEY, isDensity, DEFAULT_DENSITY)
}
export function readMotion(): MotionChoice {
  return read(MOTION_STORAGE_KEY, isMotionChoice, DEFAULT_MOTION)
}
/** Default ON: a user who never opens Settings still gets the same look on
 *  every device, which is what "my preferences" means to most people. */
export function readSyncEnabled(): boolean {
  return read(APPEARANCE_SYNC_STORAGE_KEY, (v): v is "0" | "1" => v === "0" || v === "1", "1") === "1"
}
/** readStoredAccent, plus this page's memory when storage throws. */
export function readAccent(): AccentChoice {
  return read(ACCENT_STORAGE_KEY, isAccentChoice, DEFAULT_ACCENT)
}
export function readTextSize(): TextSize {
  return read(TEXT_SIZE_STORAGE_KEY, isTextSize, DEFAULT_TEXT_SIZE)
}
export function setTextSize(t: TextSize): void {
  applyTextSizeAttr(t)
  write(TEXT_SIZE_STORAGE_KEY, t)
  emit()
}
export function readSidebar(): SidebarChoice {
  return read(SIDEBAR_STORAGE_KEY, isSidebarChoice, DEFAULT_SIDEBAR)
}
/** Records the rail mode in the store. The sidebar itself is switched by the
 *  bridge inside SidebarProvider (components/layout/sidebar-preference.tsx). */
export function setSidebarChoice(m: SidebarChoice): void {
  write(SIDEBAR_STORAGE_KEY, m)
  emit()
}

export function setDensity(d: Density): void {
  applyDensityAttr(d)
  write(DENSITY_STORAGE_KEY, d)
  emit()
}
export function setMotion(m: MotionChoice): void {
  applyMotionAttr(m)
  write(MOTION_STORAGE_KEY, m)
  emit()
}
export function setSyncEnabled(on: boolean): void {
  write(APPEARANCE_SYNC_STORAGE_KEY, on ? "1" : "0")
  emit()
}
/** applyAccent plus a same-tab notification, so every picker agrees. */
export function setAccentChoice(id: AccentChoice): void {
  applyAccent(id)
  memory.set(ACCENT_STORAGE_KEY, id)
  emit()
}

/**
 * Inline in <head> (app/layout.tsx), beside the accent script, so a compact,
 * reduced-motion or large-text user does not see one frame of the default
 * look on every load. It first runs the one-time device migration (same
 * rules as migrateDeviceAppearance). Only known attribute values are ever
 * written.
 */
export const APPEARANCE_BOOT_SCRIPT = `(function(){try{var d=document.documentElement,s=localStorage;var vk=${JSON.stringify(
  APPEARANCE_VERSION_STORAGE_KEY,
)},tk=${JSON.stringify(TEXT_SIZE_STORAGE_KEY)};if(s.getItem(vk)!==${JSON.stringify(
  APPEARANCE_VERSION,
)}){var u=false,n=typeof s.length==="number"?s.length:0;for(var i=0;i<n&&typeof s.key==="function";i++){var k=s.key(i);if(k&&k!==vk&&(k==="theme"||k.indexOf("crewship")===0)){u=true;break}}if(u&&s.getItem(tk)===null)s.setItem(tk,${JSON.stringify(
  LEGACY_TEXT_SIZE,
)});if(u&&s.getItem(${JSON.stringify(THEME_STORAGE_KEY)})===null)s.setItem(${JSON.stringify(
  THEME_STORAGE_KEY,
)},${JSON.stringify(LEGACY_THEME)});s.setItem(vk,${JSON.stringify(APPEARANCE_VERSION)})}if(s.getItem(${JSON.stringify(
  DENSITY_STORAGE_KEY,
)})==="compact")d.setAttribute("data-density","compact");var m=s.getItem(${JSON.stringify(
  MOTION_STORAGE_KEY,
)});if(m==="on")d.setAttribute("data-motion","reduce");else if(m==="off")d.setAttribute("data-motion","full");var t=s.getItem(tk);if(t===null)t=${JSON.stringify(
  DEFAULT_TEXT_SIZE,
)};if(t==="small"||t==="large")d.setAttribute("data-text-size",t);${STYLE_BOOT_SNIPPET}}catch(e){}})()`
