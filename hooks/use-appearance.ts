"use client"

import { useCallback, useMemo, useSyncExternalStore } from "react"
import { useTheme } from "next-themes"

import { DEFAULT_ACCENT, type AccentChoice } from "@/lib/theme/accents"
import {
  applyStyleAttributes,
  parseStyleState,
  planStyleSelection,
  readStyleState,
  resetStyleState,
  serializeStyleState,
  setKnobValue,
  writeStyleState,
  type StyleState,
} from "@/lib/theme/appearance-style"
import {
  DEFAULT_DENSITY,
  DEFAULT_MOTION,
  DEFAULT_SIDEBAR,
  DEFAULT_TEXT_SIZE,
  isThemeChoice,
  readAccent,
  readDensity,
  readMotion,
  readSidebar,
  readTextSize,
  readSyncEnabled,
  emitAppearanceChange,
  setAccentChoice,
  setDensity,
  setMotion,
  setSidebarChoice,
  setTextSize,
  setSyncEnabled,
  subscribeAppearance,
  type Density,
  type MotionChoice,
  type SidebarChoice,
  type TextSize,
  type ThemeChoice,
} from "@/lib/theme/appearance"

// The server snapshot is the default for every field. Hydration renders the
// defaults, then React re-renders with the stored values — the same "render
// unpressed until mounted" the pickers did by hand, without each one keeping
// its own copy. The <html> attributes are already right before that: the boot
// scripts in app/layout.tsx painted them from storage.
const serverAccent = () => DEFAULT_ACCENT
const serverDensity = () => DEFAULT_DENSITY
const serverMotion = () => DEFAULT_MOTION
const serverTextSize = () => DEFAULT_TEXT_SIZE
const serverSidebar = () => DEFAULT_SIDEBAR
const serverSync = () => true

export function useAccentChoice(): AccentChoice {
  return useSyncExternalStore(subscribeAppearance, readAccent, serverAccent)
}
export function useDensity(): Density {
  return useSyncExternalStore(subscribeAppearance, readDensity, serverDensity)
}
export function useMotionChoice(): MotionChoice {
  return useSyncExternalStore(subscribeAppearance, readMotion, serverMotion)
}
export function useTextSize(): TextSize {
  return useSyncExternalStore(subscribeAppearance, readTextSize, serverTextSize)
}
export function useSidebarChoice(): SidebarChoice {
  return useSyncExternalStore(subscribeAppearance, readSidebar, serverSidebar)
}
export function useAppearanceSyncEnabled(): boolean {
  return useSyncExternalStore(subscribeAppearance, readSyncEnabled, serverSync)
}

const serverStyle = () => null
function readStyleRaw(): string | null {
  const s = readStyleState()
  return s ? serializeStyleState(s) : null
}
/** The stored style state (null until one is picked), as the sync hook keeps it. */
export function useStyleState(): StyleState | null {
  const raw = useSyncExternalStore(subscribeAppearance, readStyleRaw, serverStyle)
  return useMemo(() => (raw ? parseStyleState(JSON.parse(raw)) : null), [raw])
}

export interface AppearanceState {
  /** The stored theme choice (System stays "system"); undefined until
   *  next-themes knows it. */
  theme: ThemeChoice | undefined
  accent: AccentChoice
  density: Density
  motion: MotionChoice
  textSize: TextSize
  sidebar: SidebarChoice
  syncEnabled: boolean
  /** The stored style and knobs; null while Harbor is simply the default. */
  style: StyleState | null
  /** One-shot: the style, its suggested accent and, for a single-scheme style, its scheme. */
  pickStyle: (id: string) => void
  /** Back to Harbor with default knobs; theme and accent are left alone. */
  resetStyle: () => void
  setKnob: (name: string, value: string) => void
  setTheme: (t: ThemeChoice) => void
  setAccent: (id: AccentChoice) => void
  setDensity: (d: Density) => void
  setMotion: (m: MotionChoice) => void
  setTextSize: (t: TextSize) => void
  setSidebar: (m: SidebarChoice) => void
  setSyncEnabled: (on: boolean) => void
}

/**
 * Every appearance choice and its setter, for any picker — the top-bar menu,
 * Settings › Appearance, anything later.
 *
 * Changing a choice here only changes it locally. Nothing in a picker talks
 * to the server: the one AppearanceSync in the shell (hooks/use-appearance-sync)
 * watches these same values and writes them back, so a picker cannot forget
 * to sync and a restricted session cannot be made to call a route it is
 * refused.
 */
export function useAppearance(): AppearanceState {
  const { theme: rawTheme, resolvedTheme, setTheme: setNextTheme } = useTheme()
  const theme = isThemeChoice(rawTheme) ? rawTheme : undefined
  const setTheme = useCallback((t: ThemeChoice) => setNextTheme(t), [setNextTheme])
  const style = useStyleState()
  const accent = useAccentChoice()
  const paint = (next: StyleState, scheme: "light" | "dark") => {
    writeStyleState(next)
    applyStyleAttributes(document.documentElement, next, scheme)
  }
  const currentScheme = resolvedTheme === "light" ? "light" : "dark"
  const pickStyle = (id: string) => {
    const plan = planStyleSelection(id, style, theme)
    paint(plan.style, plan.theme === "light" || plan.theme === "dark" ? plan.theme : currentScheme)
    setAccentChoice(plan.accent)
    if (plan.theme) setTheme(plan.theme)
    emitAppearanceChange()
  }
  const resetStyle = () => {
    paint(resetStyleState(), currentScheme)
    emitAppearanceChange()
  }
  const setKnob = (name: string, value: string) => {
    paint(setKnobValue(style, name, value), currentScheme)
    emitAppearanceChange()
  }
  return {
    theme,
    accent,
    style,
    pickStyle,
    resetStyle,
    setKnob,
    density: useDensity(),
    motion: useMotionChoice(),
    textSize: useTextSize(),
    sidebar: useSidebarChoice(),
    syncEnabled: useAppearanceSyncEnabled(),
    setTheme,
    setAccent: setAccentChoice,
    setDensity,
    setMotion,
    setTextSize,
    setSidebar: setSidebarChoice,
    setSyncEnabled,
  }
}
