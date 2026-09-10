"use client"

import * as React from "react"

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

  // The route prop wins when the route genuinely changes under us — arriving
  // from another surface entirely, or a full reload.
  React.useEffect(() => {
    setRouteState((current) => (current.slug === initialSlug ? current : { ...current, slug: initialSlug, mode: "view", pane: "section" }))
  }, [initialSlug])

  const routeRef = React.useRef(route)
  routeRef.current = route
  const dirtyRef = React.useRef(dirty)
  dirtyRef.current = dirty

  const apply = React.useCallback((next: EditorRoute, replace: boolean) => {
    setRouteState(next)
    setDirty(false)
    const href = editorRouteHref(next)
    if (replace) window.history.replaceState(null, "", href)
    else window.history.pushState(null, "", href)
  }, [])

  /**
   * Every address change goes through here, so the unsaved-work question is
   * asked in exactly one place rather than at each caller.
   */
  const navigate = React.useCallback(
    (next: EditorRoute, options?: { replace?: boolean; alreadyMoved?: boolean }) => {
      const current = routeRef.current
      if (sameRoute(current, next)) return
      if (!dirtyRef.current) {
        if (options?.alreadyMoved) {
          setRouteState(next)
          setDirty(false)
        } else {
          apply(next, options?.replace === true)
        }
        return
      }
      setPending({
        route: next,
        discard: () => {
          setPending(null)
          if (options?.alreadyMoved) {
            setRouteState(next)
            setDirty(false)
          } else {
            apply(next, options?.replace === true)
          }
        },
        keep: () => {
          setPending(null)
          // Back already moved the browser. Put the entry we were on back at
          // the top so the address and the screen agree again.
          if (options?.alreadyMoved) window.history.pushState(null, "", editorRouteHref(current))
        },
      })
    },
    [apply],
  )

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

  const openPage = React.useCallback(
    (slug: string | null) => navigate({ slug, mode: "view", section: DEFAULT_EDITOR_SECTION, pane: "section" }),
    [navigate],
  )
  const setMode = React.useCallback(
    (mode: EditorMode) => navigate({ ...routeRef.current, mode, pane: "section" }),
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

  return { ...route, dirty, pending, openPage, setMode, setSection, setPane, setDirty }
}
