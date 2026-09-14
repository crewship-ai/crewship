/** Must run in the document head before the router registers popstate handlers. */
export const HISTORY_NAVIGATION_GUARD_SCRIPT = `(() => {
  if (window.__crewshipHistoryGuards) return;
  const guards = window.__crewshipHistoryGuards = new Set();
  window.addEventListener("popstate", event => {
    for (const guard of Array.from(guards).reverse()) {
      if (guard(event) === false) {
        event.stopImmediatePropagation();
        return;
      }
    }
  }, true);
})();`

type HistoryGuard = (event: PopStateEvent) => boolean

declare global {
  interface Window {
    __crewshipHistoryGuards?: Set<HistoryGuard>
  }
}

export function registerHistoryNavigationGuard(guard: HistoryGuard): () => void {
  const guards = window.__crewshipHistoryGuards
  if (guards) {
    guards.add(guard)
    return () => { guards.delete(guard) }
  }
  // Standalone component roots do not render the application's head. The
  // application itself installs the dispatcher before hydration: a listener
  // added by an editor after the router mounted can be removed by that
  // router's unmount before it ever gets to handle the same popstate.
  const onPop = (event: PopStateEvent) => {
    if (!guard(event)) event.stopImmediatePropagation()
  }
  window.addEventListener("popstate", onPop, true)
  return () => window.removeEventListener("popstate", onPop, true)
}
