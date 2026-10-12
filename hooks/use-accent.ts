"use client"

import type { AccentId } from "@/lib/theme/accents"
import { setAccentChoice } from "@/lib/theme/appearance"
import { useAccentChoice } from "@/hooks/use-appearance"

/**
 * The accent theme for this browser. The boot script in app/layout.tsx has
 * already painted the stored accent before hydration; this hook only reads it
 * back for the picker and changes it.
 *
 * It reads through the shared appearance store (lib/theme/appearance.ts), so
 * two pickers on one page — the top-bar menu and Settings › Appearance — show
 * the same swatch pressed, and another tab's change still arrives through the
 * store's storage listener. Kept as its own hook for existing callers.
 */
export function useAccent(): [AccentId, (id: AccentId) => void] {
  return [useAccentChoice(), setAccentChoice]
}
