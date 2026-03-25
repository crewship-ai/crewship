"use client"

/**
 * Subtle animated line shown during background data refresh.
 * Place at the top of a card/section to indicate silent update in progress.
 */
export function RefreshIndicator({ active }: { active: boolean }) {
  if (!active) return null
  return <div className="h-0.5 bg-primary/50 animate-pulse rounded-full" aria-hidden="true" />
}
