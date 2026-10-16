/**
 * Appearance style — the Harbor 2 visual style and its knobs (#3334).
 *
 * Why a second preference key: `appearance` (lib/theme/appearance.ts) is
 * written whole by every client, including ones built before styles existed.
 * Keeping the style in `appearance.style` means such a client cannot drop it
 * when it saves an accent or a theme. In the other direction the theme and the
 * accent live ONLY in `appearance`, so they always win when rendering: a style
 * merely suggests an accent (and, for a single-scheme style, a theme) at the
 * moment it is picked.
 *
 * Rendering order: Harbor tokens → the style's block (`data-style` on <html>)
 * → knobs → accent and theme. Only Harbor ships today; the gallery that adds
 * more styles comes later and only has to add rows to STYLES and CSS blocks.
 *
 * A value this build does not understand — a style or knob from a newer
 * release — is not rendered, but it is kept, and written back unchanged.
 */

import type { AccentChoice } from "@/lib/theme/accents"
import type { ThemeChoice } from "@/lib/theme/appearance"

/** The per-user preference key (GET/PUT /api/v1/me/preferences/{key}). */
export const STYLE_PREFERENCE_KEY = "appearance.style"
/** This browser's copy, read by the boot script before first paint. */
export const STYLE_STORAGE_KEY = "crewship-appearance-style"

export type Scheme = "light" | "dark"

export interface StyleDef {
  id: string
  /** The schemes the style has a palette for. */
  schemes: Scheme[]
  /** The accent suggested when the style is picked. */
  accent: AccentChoice
}

export interface StyleState {
  v: 1
  style: string
  knobs: Record<string, string>
}

export const HARBOR: StyleDef = { id: "harbor", schemes: ["light", "dark"], accent: "blue" }
/** Every style this build can render. */
export const STYLES: StyleDef[] = [HARBOR]

const ID = /^[a-z0-9-]{1,32}$/
const KNOB_VALUE_MAX = 64

/** A stored value, judged on its own. Unknown-but-well-formed is kept. */
export function parseStyleState(value: unknown): StyleState | null {
  if (!value || typeof value !== "object" || Array.isArray(value)) return null
  const v = value as Record<string, unknown>
  if (typeof v.style !== "string" || !ID.test(v.style)) return null
  if (v.knobs !== undefined && (typeof v.knobs !== "object" || v.knobs === null || Array.isArray(v.knobs))) return null
  const knobs: Record<string, string> = {}
  for (const [k, val] of Object.entries((v.knobs as Record<string, unknown>) ?? {})) {
    if (ID.test(k) && typeof val === "string" && val.length <= KNOB_VALUE_MAX) knobs[k] = val
  }
  return { v: 1, style: v.style, knobs }
}

export function serializeStyleState(s: StyleState): string {
  return JSON.stringify({ v: 1, style: s.style, knobs: s.knobs })
}

export function readStyleState(): StyleState | null {
  try {
    const raw = localStorage.getItem(STYLE_STORAGE_KEY)
    return raw ? parseStyleState(JSON.parse(raw)) : null
  } catch {
    return null
  }
}

export function writeStyleState(s: StyleState): void {
  try {
    localStorage.setItem(STYLE_STORAGE_KEY, serializeStyleState(s))
  } catch {
    // Blocked storage: the choice holds for this page only.
  }
}

/** Reset style: back to Harbor with default knobs. */
export function clearStyleState(): void {
  try {
    localStorage.removeItem(STYLE_STORAGE_KEY)
  } catch {
    // nothing stored
  }
}

function find(id: string, registry: StyleDef[]): StyleDef | undefined {
  return registry.find((s) => s.id === id)
}

/**
 * The style actually painted. Harbor stands in for a style this build does not
 * know, and for a style shown in a scheme it has no palette for — the pair a
 * half-finished write on another device can leave behind. Nothing is written:
 * the stored value stays for the device that understands it, or until the
 * missing half of the write arrives.
 */
export function renderedStyle(state: StyleState | null, scheme: Scheme, registry: StyleDef[] = STYLES): string {
  const def = state ? find(state.style, registry) : undefined
  return def && def.schemes.includes(scheme) ? def.id : HARBOR.id
}

/** The theme choices a style can be shown in; System only when it has both. */
export function allowedSchemes(id: string, registry: StyleDef[] = STYLES): ThemeChoice[] {
  const def = find(id, registry)
  if (!def || def.schemes.length > 1) return ["dark", "light", "system"]
  return [...def.schemes]
}

export interface StyleSelection {
  style: StyleState
  accent: AccentChoice
  /** Set only when the current theme is one the style cannot show. */
  theme?: ThemeChoice
}

/**
 * What picking a style writes: the style (keeping the user's knobs), its
 * suggested accent, and — for a single-scheme style when the current theme is
 * another one or System — that scheme as the theme.
 */
export function planStyleSelection(
  id: string,
  current: StyleState | null,
  theme: ThemeChoice | undefined,
  registry: StyleDef[] = STYLES,
): StyleSelection {
  const def = find(id, registry)
  if (!def) throw new Error(`unknown style: ${id}`)
  const plan: StyleSelection = { style: { v: 1, style: def.id, knobs: { ...(current?.knobs ?? {}) } }, accent: def.accent }
  if (def.schemes.length === 1 && theme !== def.schemes[0]) plan.theme = def.schemes[0]
  return plan
}

/** Random changes the style only — never scheme, text size, motion or density. */
export function randomStyle(registry: StyleDef[], rng: () => number, knobs: Record<string, string> = {}): StyleState {
  const def = registry[Math.min(registry.length - 1, Math.floor(rng() * registry.length))]
  return { v: 1, style: def.id, knobs: { ...knobs } }
}

/**
 * Pre-paint part of APPEARANCE_BOOT_SCRIPT (it defines `d` and `s`). Writes
 * only a known style id; anything else paints Harbor.
 */
export const STYLE_BOOT_SNIPPET = `try{var sv=${JSON.stringify(HARBOR.id)},sr=s.getItem(${JSON.stringify(
  STYLE_STORAGE_KEY,
)});if(sr){var so=JSON.parse(sr);if(so&&${JSON.stringify(STYLES.map((x) => x.id))}.indexOf(so.style)>-1)sv=so.style}d.setAttribute("data-style",sv)}catch(e){d.setAttribute("data-style",${JSON.stringify(
  HARBOR.id,
)})}`
