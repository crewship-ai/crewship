"use client"

import { useEffect } from "react"

/**
 * A place for a surface to say "not yet" to a navigation it does not own.
 *
 * The Pages editor guards its own address — every section change, Page change
 * and Back goes through one function that asks before it moves. The workspace
 * switcher is not on that path: it is client state in `use-workspace`, with no
 * navigation and no reload, so switching workspaces re-keyed the editor and
 * took unsaved edits with it silently. The design asks for the same protection
 * on a workspace switch as on a Page switch, and this is the smallest thing
 * that provides it without the switcher knowing what a Page editor is.
 *
 * A guard returns `false` to refuse, and is handed a `retry` closure that
 * performs the navigation it just refused. Refusing is a plain boolean rather
 * than a promise because the caller must abandon the navigation
 * synchronously, before any state moves; `retry` is how the answer to the
 * question the guard asks gets applied afterwards.
 */
export type NavigationGuard = (retry: () => void) => boolean

const guards = new Set<NavigationGuard>()

/** Returns an unregister function. */
export function registerNavigationGuard(guard: NavigationGuard): () => void {
  guards.add(guard)
  return () => {
    guards.delete(guard)
  }
}

/**
 * True when every registered guard allows the navigation. A guard that throws
 * is treated as allowing it: a broken guard must not be able to wedge the
 * whole application into a state where nothing can be navigated away from.
 */
export function navigationAllowed(retry: () => void): boolean {
  for (const guard of guards) {
    try {
      if (!guard(retry)) return false
    } catch {
      // Deliberately ignored; see above.
    }
  }
  return true
}

/** Register for the lifetime of a component. */
export function useNavigationGuard(guard: NavigationGuard, enabled: boolean): void {
  useEffect(() => {
    if (!enabled) return
    return registerNavigationGuard(guard)
  }, [guard, enabled])
}
