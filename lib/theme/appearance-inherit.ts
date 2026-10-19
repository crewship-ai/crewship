/**
 * The workspace default look and what a user inherits from it (WSD-01/03/04).
 *
 * `workspaces.appearance_default` holds `{style, knobs, accent, theme}`, set by
 * an OWNER or ADMIN. A user inherits each of those values they have not chosen
 * themselves (lib/theme/appearance-explicit). Precedence per value:
 * the user's explicit value, then the workspace default, then the built-in
 * default (Harbor, the Blue accent, follow the system theme).
 */

import { DEFAULT_ACCENT, isAccentChoice, type AccentChoice } from "@/lib/theme/accents"
import { DEFAULT_THEME, isThemeChoice, type ThemeChoice } from "@/lib/theme/appearance"
import { HARBOR, parseStyleState, type StyleState } from "@/lib/theme/appearance-style"

export interface WorkspaceAppearanceDefault {
  style?: string
  knobs?: Record<string, string>
  accent?: AccentChoice
  theme?: ThemeChoice
}

/** Each key judged on its own, like parseAppearance: a value this build does
 *  not understand drops that key, not the whole default. */
export function parseWorkspaceAppearanceDefault(value: unknown): WorkspaceAppearanceDefault {
  if (!value || typeof value !== "object" || Array.isArray(value)) return {}
  const v = value as Record<string, unknown>
  const out: WorkspaceAppearanceDefault = {}
  const style = parseStyleState({ style: v.style, knobs: v.knobs })
  if (style) out.style = style.style
  // Knobs without a style ride on Harbor; a knobs value that is not an object is dropped.
  const knobsOnly = style ?? parseStyleState({ style: HARBOR.id, knobs: v.knobs })
  if (knobsOnly && v.knobs !== undefined && Object.keys(knobsOnly.knobs).length > 0) out.knobs = knobsOnly.knobs
  if (isAccentChoice(v.accent)) out.accent = v.accent
  if (isThemeChoice(v.theme)) out.theme = v.theme
  return out
}

/** Stable text for comparing two defaults and for effect dependencies. */
export function workspaceDefaultKey(d: WorkspaceAppearanceDefault): string {
  return JSON.stringify({ s: d.style ?? null, k: d.knobs ? Object.entries(d.knobs).sort() : null, a: d.accent ?? null, t: d.theme ?? null })
}

export interface InheritedLook {
  theme: ThemeChoice
  accent: AccentChoice
  /** null = Harbor with default knobs (nothing stored). */
  style: StyleState | null
}

/** What a user who chose nothing sees in this workspace. */
export function inheritedLook(d: WorkspaceAppearanceDefault): InheritedLook {
  const hasStyle = d.style !== undefined || d.knobs !== undefined
  return {
    theme: d.theme ?? DEFAULT_THEME,
    accent: d.accent ?? DEFAULT_ACCENT,
    style: hasStyle ? { v: 1, style: d.style ?? HARBOR.id, knobs: { ...(d.knobs ?? {}) } } : null,
  }
}

/** The body of the PATCH that makes the current look the workspace default. */
export function toWorkspaceDefaultBody(look: { theme: ThemeChoice; accent: AccentChoice; style: StyleState | null }): WorkspaceAppearanceDefault {
  return {
    style: look.style?.style ?? HARBOR.id,
    knobs: { ...(look.style?.knobs ?? {}) },
    accent: look.accent,
    theme: look.theme,
  }
}
