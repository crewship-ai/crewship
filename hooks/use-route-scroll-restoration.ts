"use client"

import { useEffect, useRef, type RefObject } from "react"

/**
 * Scroll restoration for content that scrolls inside an element, not the
 * document.
 *
 * The dashboard's pages scroll in a container, so the browser has nothing to
 * manage on a route change: pushing forward arrived at the new screen already
 * scrolled halfway down the last one, and going back arrived at the top of a
 * list the person had been two screens into. This does what the browser would
 * have done for a document — top on a new entry, the remembered offset when a
 * history entry is revisited.
 *
 * "Revisited" is decided by the history entry, not by timing. Each entry this
 * hook has seen carries a key in `history.state`; a `pathname` change whose
 * entry already has one is a pop (or forward) and restores, one without is a
 * push and starts at the top. A flag set on `popstate` would have been simpler
 * and wrong: an overlay's own history entry pops without changing the route,
 * and the flag would still be set for the next real navigation.
 */

const KEY = "__scrollKey"

type Keyed = { [KEY]?: string } | null

const currentKey = () => (window.history.state as Keyed)?.[KEY]

let seq = 0

export function useRouteScrollRestoration(
  scrollRef: RefObject<HTMLElement | null>,
  pathname: string,
) {
  const offsets = useRef(new Map<string, number>())

  useEffect(() => {
    const el = scrollRef.current
    if (!el || typeof window === "undefined") return

    const key = currentKey()
    const remembered = key ? offsets.current.get(key) : undefined

    if (key === undefined) {
      // Spread the router's own state so Next's internal keys survive; a bare
      // object here makes the App Router lose its place.
      window.history.replaceState({ ...window.history.state, [KEY]: `s${++seq}` }, "")
    }
    el.scrollTo({ top: remembered ?? 0 })

    // Recorded as it happens rather than on the way out: by the time the
    // route effect runs the new screen is already in the container, and a
    // shorter one has clamped the offset we meant to remember.
    const onScroll = () => {
      const k = currentKey()
      if (k) offsets.current.set(k, el.scrollTop)
    }
    el.addEventListener("scroll", onScroll, { passive: true })
    return () => el.removeEventListener("scroll", onScroll)
  }, [scrollRef, pathname])
}
