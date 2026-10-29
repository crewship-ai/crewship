import * as React from "react"

/**
 * The app's one set of layout breakpoints, on Tailwind's `md`, `xl` and `2xl`
 * edges: phone < 768, tablet 768–1279, desktop ≥ 1280, wide ≥ 1536.
 *
 * Page layout decides through these — `useLayoutBreakpoint()` in code, the
 * `md:` / `xl:` / `2xl:` variants (and their `max-*` forms) in markup and CSS.
 * Component-internal `sm:` / `lg:` tweaks are not page layout and stay as they
 * are. `useIsMobile()` is the phone edge of the same scale.
 */
export const LAYOUT_BREAKPOINTS = { tablet: 768, desktop: 1280, wide: 1536 } as const

export type LayoutBreakpoint = "phone" | "tablet" | "desktop" | "wide"

/** Server render and first client render, before the viewport is known. */
const SSR_DEFAULT: LayoutBreakpoint = "desktop"

export function layoutBreakpointFor(width: number): LayoutBreakpoint {
  if (width >= LAYOUT_BREAKPOINTS.wide) return "wide"
  if (width >= LAYOUT_BREAKPOINTS.desktop) return "desktop"
  if (width >= LAYOUT_BREAKPOINTS.tablet) return "tablet"
  return "phone"
}

/** Which layout band the viewport is in; re-renders only when it crosses an edge. */
export function useLayoutBreakpoint(): LayoutBreakpoint {
  const [breakpoint, setBreakpoint] = React.useState<LayoutBreakpoint>(SSR_DEFAULT)

  React.useEffect(() => {
    const mqls = Object.values(LAYOUT_BREAKPOINTS).map((edge) =>
      window.matchMedia(`(min-width: ${edge}px)`),
    )
    const onChange = () => {
      setBreakpoint(layoutBreakpointFor(window.innerWidth))
    }
    for (const mql of mqls) mql.addEventListener("change", onChange)
    onChange()
    return () => {
      for (const mql of mqls) mql.removeEventListener("change", onChange)
    }
  }, [])

  return breakpoint
}

/** Media queries for the bands code asks about directly, built from LAYOUT_BREAKPOINTS. */
export const LAYOUT_BAND_QUERIES = {
  tablet: `(min-width: ${LAYOUT_BREAKPOINTS.tablet}px) and (max-width: ${LAYOUT_BREAKPOINTS.desktop - 1}px)`,
  wide: `(min-width: ${LAYOUT_BREAKPOINTS.wide}px)`,
} as const

export type LayoutBand = keyof typeof LAYOUT_BAND_QUERIES

/** Whether the viewport is in `band` now; false on the server or without matchMedia. */
export function matchesLayoutBand(band: LayoutBand): boolean {
  return typeof window !== "undefined" && typeof window.matchMedia === "function" && window.matchMedia(LAYOUT_BAND_QUERIES[band]).matches
}

/**
 * Whether the viewport is in `band`, following it across edges. The first
 * render uses `initial` (false by default, the server's answer); pass
 * `() => matchesLayoutBand(band)` to read the viewport synchronously instead.
 */
export function useLayoutBand(band: LayoutBand, initial: boolean | (() => boolean) = false): boolean {
  const [on, setOn] = React.useState(initial)
  React.useEffect(() => {
    if (typeof window === "undefined" || typeof window.matchMedia !== "function") return
    const mql = window.matchMedia(LAYOUT_BAND_QUERIES[band])
    const update = () => setOn(mql.matches)
    update()
    mql.addEventListener?.("change", update)
    return () => mql.removeEventListener?.("change", update)
  }, [band])
  return on
}
