"use client"

import { useCallback } from "react"
import { useTheme } from "next-themes"

import { toast } from "@/lib/toast"
import { useAppearance } from "@/hooks/use-appearance"
import {
  emitAppearanceChange,
  readAccent,
  readAvatarAnimation,
  readDensity,
  readMotion,
  readSidebar,
  readTextSize,
  setAccentChoice,
  setAvatarAnimation,
  setDensity,
  setMotion,
  setSidebarChoice,
  setTextSize,
  type ThemeChoice,
} from "@/lib/theme/appearance"
import { readExplicit, readInherited, restoreMarks } from "@/lib/theme/appearance-explicit"
import { clearStyleState, readStyleState, writeStyleState } from "@/lib/theme/appearance-style"

const UNDO_TOAST_ID = "appearance-undo"
const UNDO_TOAST_MS = 8000

/**
 * Personal appearance applies and saves as it is changed, so the last change
 * gets an immediate Undo: a toast with an Undo button (one at a time; the next
 * change replaces it). Undo puts back every stored value the change touched
 * and which values count as the user's own, through the raw setters, so the
 * sync hook sees an ordinary change and saves the old look again.
 *
 * Nothing here talks to the server, and nothing here touches the workspace
 * default: that is shared and has its own button.
 */
export function useUndoableAppearance() {
  const { theme } = useAppearance()
  const { setTheme: setNextTheme } = useTheme()

  const restore = useCallback(
    (snap: Snapshot) => {
      if (snap.theme) setNextTheme(snap.theme)
      setAccentChoice(snap.accent)
      if (snap.style) writeStyleState(snap.style)
      else clearStyleState()
      setDensity(snap.density)
      setMotion(snap.motion)
      setTextSize(snap.textSize)
      setSidebarChoice(snap.sidebar)
      setAvatarAnimation(snap.avatarAnimation)
      restoreMarks(snap.explicit, snap.inherited)
      emitAppearanceChange()
      toast.dismiss(UNDO_TOAST_ID)
    },
    [setNextTheme],
  )

  /** Wraps a personal change: takes the look as it is, runs the change, offers Undo. */
  return useCallback(
    <A extends unknown[]>(message: string, change: (...args: A) => void) =>
      (...args: A) => {
        const snap: Snapshot = {
          theme,
          accent: readAccent(),
          style: readStyleState(),
          density: readDensity(),
          motion: readMotion(),
          textSize: readTextSize(),
          sidebar: readSidebar(),
          avatarAnimation: readAvatarAnimation(),
          explicit: readExplicit(),
          inherited: readInherited(),
        }
        change(...args)
        toast(message, { id: UNDO_TOAST_ID, duration: UNDO_TOAST_MS, action: { label: "Undo", onClick: () => restore(snap) } })
      },
    [theme, restore],
  )
}

type Snapshot = {
  theme: ThemeChoice | undefined
  accent: ReturnType<typeof readAccent>
  style: ReturnType<typeof readStyleState>
  density: ReturnType<typeof readDensity>
  motion: ReturnType<typeof readMotion>
  textSize: ReturnType<typeof readTextSize>
  sidebar: ReturnType<typeof readSidebar>
  avatarAnimation: ReturnType<typeof readAvatarAnimation>
  explicit: ReturnType<typeof readExplicit>
  inherited: ReturnType<typeof readInherited>
}
