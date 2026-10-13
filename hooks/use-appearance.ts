"use client"

import { useCallback, useSyncExternalStore } from "react"
import { useTheme } from "next-themes"

import { DEFAULT_ACCENT, type AccentChoice } from "@/lib/theme/accents"
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
  const { theme: rawTheme, setTheme: setNextTheme } = useTheme()
  const theme = isThemeChoice(rawTheme) ? rawTheme : undefined
  const setTheme = useCallback((t: ThemeChoice) => setNextTheme(t), [setNextTheme])
  return {
    theme,
    accent: useAccentChoice(),
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
