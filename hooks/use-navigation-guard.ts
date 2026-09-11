"use client"

import { useEffect } from "react"
import { useRouter } from "next/navigation"

/**
 * A place for a surface to say "not yet" to a navigation it does not own.
 *
 * The Pages editor guards its own address — every section change, Page change
 * and Back goes through one function that asks before it moves. Two ways out
 * of it are not on that path, and both lose typed work silently:
 *
 *   - the workspace switcher, which is client state in `use-workspace` with no
 *     navigation and no reload, so the editor was simply re-keyed;
 *   - **every other link in the application**. The global sidebar, the toolbar
 *     and the mobile menu are plain `next/link`, so clicking Routines is a
 *     client-side navigation that is neither a reload nor a popstate: the
 *     editor unmounts and the half-typed form goes with it. Guarding the
 *     switcher alone left that wide open (counter-review R3).
 *
 * So the interception lives here rather than in each navigation component.
 * One capture-phase listener, installed only while a guard is actually
 * registered, covers links this module has never heard of — which is the point:
 * a surface that grows a new way out should not be able to forget about it.
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

/**
 * Which clicks are this module's business.
 *
 * Deliberately narrow: anything a person could reasonably expect to escape the
 * page entirely — a new tab, a download, another origin — is left alone. A
 * guard exists to protect unsaved work inside this app, not to trap a cursor.
 */
export function interceptableHref(event: MouseEvent): string | null {
  if (event.defaultPrevented) return null
  // Anything but an unmodified primary click is "open this somewhere else".
  if (event.button !== 0 || event.metaKey || event.ctrlKey || event.shiftKey || event.altKey) return null
  const target = event.target
  if (!(target instanceof Element)) return null
  const anchor = target.closest("a")
  if (!(anchor instanceof HTMLAnchorElement)) return null
  if (anchor.hasAttribute("download")) return null
  const explicitTarget = anchor.getAttribute("target")
  if (explicitTarget !== null && explicitTarget !== "_self") return null
  const href = anchor.getAttribute("href")
  if (href === null || href === "" || href.startsWith("#")) return null
  let url: URL
  try {
    url = new URL(anchor.href, window.location.href)
  } catch {
    return null
  }
  if (url.origin !== window.location.origin) return null
  // Going where you already are is not leaving.
  if (url.pathname + url.search === window.location.pathname + window.location.search) return null
  return url.pathname + url.search + url.hash
}

/**
 * Register for the lifetime of a component, and while registered, put the same
 * question in front of every in-app link.
 */
export function useNavigationGuard(guard: NavigationGuard, enabled: boolean): void {
  const router = useRouter()
  useEffect(() => {
    if (!enabled) return
    const unregister = registerNavigationGuard(guard)
    const onClick = (event: MouseEvent) => {
      const href = interceptableHref(event)
      if (href === null) return
      // The listener is on capture so the decision happens before Next's own
      // handler starts the navigation; `stopPropagation` is what keeps it
      // from starting at all when a guard refuses.
      if (navigationAllowed(() => router.push(href))) return
      event.preventDefault()
      event.stopPropagation()
    }
    document.addEventListener("click", onClick, true)
    return () => {
      document.removeEventListener("click", onClick, true)
      unregister()
    }
  }, [guard, enabled, router])
}
