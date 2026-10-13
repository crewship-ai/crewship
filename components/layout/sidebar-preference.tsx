"use client"

import { useEffect, useRef } from "react"

import { useSidebar } from "@/components/ui/sidebar"
import { useSidebarChoice } from "@/hooks/use-appearance"
import { setSidebarChoice } from "@/lib/theme/appearance"

/**
 * Keeps the left rail's mode and the synced appearance preference in step.
 *
 * The sidebar owns its mode (components/ui/sidebar.tsx: React state plus its
 * own localStorage key) and is switched from the rail itself and ⌘B. The
 * appearance store reads the same key so the mode can sync with the account
 * and be chosen in Settings › Appearance. Two directions:
 *   - the rail changed → tell the store (so AppearanceSync writes it back);
 *   - the store changed (Settings, or the server on sign-in) → switch the rail.
 * Equal values stop the loop. Must render inside SidebarProvider.
 */
export function SidebarPreference() {
  const { sidebarMode, setSidebarMode, isMobile } = useSidebar()
  const choice = useSidebarChoice()

  useEffect(() => {
    setSidebarChoice(sidebarMode)
  }, [sidebarMode])

  // React to the preference CHANGING, never to its first value: on mount the
  // rail already holds its stored mode, and the store's first read can only be
  // that same key or a default — adopting it would undo the rail's own state.
  const seen = useRef(choice)
  useEffect(() => {
    if (seen.current === choice) return
    seen.current = choice
    // The phone has no rail; its sheet ignores the mode.
    if (!isMobile && choice !== sidebarMode) setSidebarMode(choice)
    // sidebarMode is deliberately not a dependency: reacting to it here would
    // fight the effect above for the rail's own changes.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [choice, isMobile, setSidebarMode])

  return null
}
