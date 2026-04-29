"use client"

import { startTransition } from "react"

/**
 * Run a state update inside the View Transitions API when the browser
 * supports it; falls back to React's startTransition otherwise.
 *
 * Use for crossfading session swap, large layout reflows, etc.
 */
export function withViewTransition(update: () => void) {
  if (typeof document === "undefined") {
    update()
    return
  }
  if (typeof document.startViewTransition === "function") {
    document.startViewTransition(() => {
      startTransition(update)
    })
    return
  }
  startTransition(update)
}
