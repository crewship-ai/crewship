"use client"

import * as React from "react"

import { useNavigationGuard, type NavigationGuard } from "@/hooks/use-navigation-guard"

import {
  DEFAULT_EDITOR_SECTION,
  editorRouteHref,
  readEditorRoute,
  type EditorMode,
  type EditorPane,
  type EditorRoute,
  type EditorSection,
} from "@/lib/pages/editor-contract"

/**
 * The editor's address, and the only place that writes it.
 *
 * `/pages` already routes by hand rather than through App Router: a click sets
 * state and rewrites the address with `history.pushState`, because routing to
 * `/pages/<slug>` unmounts the whole subtree and the rail loses its scroll and
 * its filters. The editor inherits that decision and extends the address with
 * `?mode=edit&section=…&pane=preview`.
 *
 * Query parameters rather than `/pages/<slug>/edit/access` for one reason that
 * outranks taste: the production build is a static export, `/pages/[slug]` is
 * exported once as a placeholder that the Go binary serves for every slug, and
 * each further path segment is another exported route to arrange for. The
 * shape is not the product goal — surviving reload, Back, Forward and a pasted
 * link is, and a query string does that today.
 *
 * `useSearchParams()` is deliberately not used: under `output: "export"` there
 * is no per-request render, and the same reason `useUrlSegment` exists for the
 * slug applies to every other part of the address.
 */
export interface EditorNavigation extends EditorRoute {
  /** Open a page in view mode (or the overview with `null`). */
  openPage: (slug: string | null) => void
  /** Enter or leave the editor on the current page. */
  setMode: (mode: EditorMode) => void
  /**
   * Enter the editor directly on a section. One call, because two — a
   * `setMode` followed by a `setSection` — both read the route as it was at
   * the start of the event and the second silently overwrites the first.
   */
  openEditor: (section?: EditorSection) => void
  setSection: (section: EditorSection) => void
  setPane: (pane: EditorPane) => void
  /**
   * Raised by a section holding writes that have not landed. The shell asks
   * before it lets the address move, and puts the address back when the person
   * says no.
   */
  setDirty: (dirty: boolean) => void
  dirty: boolean
  /**
   * A navigation waiting on the unsaved-work question. Null when nothing is
   * pending. `discard` performs it; `keep` abandons it and restores the
   * address, which matters for Back — by the time `popstate` fires the browser
   * has already moved, so refusing means pushing the old entry back on.
   */
  pending: { route: EditorRoute; discard: () => void; keep: () => void } | null
}

function sameRoute(a: EditorRoute, b: EditorRoute): boolean {
  return a.slug === b.slug && a.mode === b.mode && a.section === b.section && a.pane === b.pane
}

export function useEditorRoute(initialSlug: string | null): EditorNavigation {
  // Seeded from the prop so a cold arrival at /pages/<slug> renders the right
  // page before any effect runs, then from the location so the query string
  // survives a reload.
  const [route, setRouteState] = React.useState<EditorRoute>(() =>
    typeof window === "undefined"
      ? { slug: initialSlug, mode: "view", section: DEFAULT_EDITOR_SECTION, pane: "section" }
      : readEditorRoute(window.location.pathname, window.location.search),
  )
  const [dirty, setDirty] = React.useState(false)
  const [pending, setPending] = React.useState<EditorNavigation["pending"]>(null)

  const routeRef = React.useRef(route)
  routeRef.current = route
  const dirtyRef = React.useRef(dirty)
  dirtyRef.current = dirty
  const pendingRef = React.useRef(pending)
  pendingRef.current = pending

  const apply = React.useCallback((next: EditorRoute, replace: boolean) => {
    setRouteState(next)
    setDirty(false)
    // Read the live search each time: the tab bar rewrites it behind us.
    const href = editorRouteHref(next, window.location.search)
    if (replace) window.history.replaceState(null, "", href)
    else window.history.pushState(null, "", href)
  }, [])

  /**
   * Every address change goes through here, so the unsaved-work question is
   * asked in exactly one place rather than at each caller.
   */
  const navigate = React.useCallback(
    (next: EditorRoute, options?: { replace?: boolean; alreadyMoved?: boolean; onDiscard?: () => void }) => {
      const current = routeRef.current
      if (sameRoute(current, next) && options?.onDiscard === undefined) return
      const perform = () => {
        if (options?.alreadyMoved) {
          setRouteState(next)
          setDirty(false)
        } else {
          apply(next, options?.replace === true)
        }
        options?.onDiscard?.()
      }
      if (!dirtyRef.current) {
        perform()
        return
      }
      // A second Back while the question is open would otherwise drop the
      // first navigation on the floor and later push an address two entries
      // stale. The person answers the question they were asked.
      if (pendingRef.current !== null) return
      setPending({
        route: next,
        discard: () => {
          setPending(null)
          perform()
        },
        keep: () => {
          setPending(null)
          // Back already moved the browser. Put the entry we were on back at
          // the top so the address and the screen agree again.
          if (options?.alreadyMoved) window.history.pushState(null, "", editorRouteHref(current, window.location.search))
        },
      })
    },
    [apply],
  )

  // The route prop wins when the route genuinely changes under us — arriving
  // from another surface entirely, or a full reload.
  //
  // It goes through `navigate` rather than straight into state, because Back
  // across two Pages arrives here as well as at `popstate`: App Router treats
  // it as a navigation, `useUrlSegment` re-reads the location and the prop
  // changes. Writing state directly here skipped the unsaved-work question,
  // and the screen jumped to the other Page with the dialog still open over
  // it, pointing at the one that had been left.
  React.useEffect(() => {
    const current = routeRef.current
    if (current.slug === initialSlug) return
    navigate({ ...current, slug: initialSlug, mode: "view", pane: "section" }, { alreadyMoved: true })
  }, [initialSlug, navigate])

  // Back and Forward. The location is the only authority: reading state we
  // pushed ourselves drifts the moment somebody navigates with the keyboard.
  React.useEffect(() => {
    const onPop = () => {
      navigate(readEditorRoute(window.location.pathname, window.location.search), { alreadyMoved: true })
    }
    window.addEventListener("popstate", onPop)
    return () => window.removeEventListener("popstate", onPop)
  }, [navigate])

  // Reload and tab close. `beforeunload` buys a browser-drawn confirmation and
  // nothing else — it cannot save the work and must not be described as if it
  // could.
  React.useEffect(() => {
    if (!dirty) return
    const warn = (event: BeforeUnloadEvent) => event.preventDefault()
    window.addEventListener("beforeunload", warn)
    return () => window.removeEventListener("beforeunload", warn)
  }, [dirty])

  // Switching workspace is client state, not a navigation: nothing routes and
  // nothing reloads, so the editor was simply re-keyed and unsaved edits went
  // with it. The guard refuses the switch, asks here, and performs it on the
  // way out of the dialog.
  const guard = React.useCallback<NavigationGuard>((retry) => {
    if (!dirtyRef.current) return true
    if (pendingRef.current !== null) return false
    setPending({
      route: routeRef.current,
      discard: () => {
        setPending(null)
        setDirty(false)
        dirtyRef.current = false
        // The switch the guard just refused, performed now that the person
        // has answered. It passes the guard on the way through because the
        // flag above is already clear.
        retry()
      },
      keep: () => setPending(null),
    })
    return false
  }, [])
  useNavigationGuard(guard, dirty)

  const openPage = React.useCallback(
    (slug: string | null) => navigate({ slug, mode: "view", section: DEFAULT_EDITOR_SECTION, pane: "section" }),
    [navigate],
  )
  const setMode = React.useCallback(
    (mode: EditorMode) => navigate({ ...routeRef.current, mode, pane: "section" }),
    [navigate],
  )
  const openEditor = React.useCallback(
    (section?: EditorSection) =>
      navigate({ ...routeRef.current, mode: "edit", section: section ?? routeRef.current.section, pane: "section" }),
    [navigate],
  )
  const setSection = React.useCallback(
    (section: EditorSection) => navigate({ ...routeRef.current, section, pane: "section" }),
    [navigate],
  )
  // Entering and leaving the candidate's preview replaces rather than pushes:
  // it is a change of workspace inside one section, and stacking it makes Back
  // walk through a preview the person already dismissed.
  const setPane = React.useCallback(
    (pane: EditorPane) => navigate({ ...routeRef.current, pane }, { replace: true }),
    [navigate],
  )

  return { ...route, dirty, pending, openPage, setMode, openEditor, setSection, setPane, setDirty }
}
