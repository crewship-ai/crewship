"use client"

import { useAppearanceSync } from "@/hooks/use-appearance-sync"

/**
 * Mounted once by the dashboard layout, and only for a trusted session: the
 * preference routes are not on the restricted allowlist. Renders nothing; see
 * hooks/use-appearance-sync.ts for what it does.
 */
export function AppearanceSync() {
  useAppearanceSync(true)
  return null
}
