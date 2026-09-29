"use client"

import { useCallback, useEffect, useState } from "react"

import { ACCENT_STORAGE_KEY, DEFAULT_ACCENT, applyAccent, isAccentId, readStoredAccent, type AccentId } from "@/lib/theme/accents"

/**
 * The accent theme for this browser. The boot script in app/layout.tsx has
 * already painted the stored accent before hydration; this hook only reads it
 * back for the picker, changes it, and follows changes made in another tab.
 */
export function useAccent(): [AccentId, (id: AccentId) => void] {
  const [accent, setAccentState] = useState<AccentId>(DEFAULT_ACCENT)

  useEffect(() => {
    setAccentState(readStoredAccent())
    const onStorage = (e: StorageEvent) => {
      if (e.key !== ACCENT_STORAGE_KEY || !isAccentId(e.newValue)) return
      document.documentElement.dataset.accent = e.newValue
      setAccentState(e.newValue)
    }
    window.addEventListener("storage", onStorage)
    return () => window.removeEventListener("storage", onStorage)
  }, [])

  const setAccent = useCallback((id: AccentId) => {
    applyAccent(id)
    setAccentState(id)
  }, [])

  return [accent, setAccent]
}
