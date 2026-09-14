/** Must run in the document head before the router registers popstate handlers. */
export const HISTORY_NAVIGATION_GUARD_SCRIPT = `(() => {
  if (window.__crewshipHistoryGuards) return;
  const guards = window.__crewshipHistoryGuards = new Set();
  const history = window.history;
  const key = "__crewshipHistoryIndex";
  let current = Number.isInteger(history.state?.[key]) ? history.state[key] : 0;
  let restoring = false;
  const push = history.pushState.bind(history);
  const replace = history.replaceState.bind(history);
  replace({ ...history.state, [key]: current }, "");
  history.pushState = (state, title, url) => {
    push({ ...state, [key]: current + 1 }, title, url);
    current += 1;
  };
  history.replaceState = (state, title, url) => {
    replace({ ...state, [key]: current }, title, url);
  };
  window.addEventListener("popstate", event => {
    if (restoring) {
      restoring = false;
      event.stopImmediatePropagation();
      return;
    }
    const target = event.state?.[key];
    for (const guard of Array.from(guards).reverse()) {
      if (guard(event) === false) {
        event.stopImmediatePropagation();
        if (Number.isInteger(target) && target !== current) {
          restoring = true;
          history.go(current - target);
        }
        return;
      }
    }
    if (Number.isInteger(target)) current = target;
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
