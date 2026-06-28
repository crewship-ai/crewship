"use client"

import { useEffect, useState } from "react"
import { usePathname } from "next/navigation"

/**
 * Read a dynamic route segment from the live URL after client hydration.
 *
 * Why this exists: in production we ship a Next.js **static export**
 * (`output: "export"`). Dynamic routes are prerendered with a single
 * placeholder param (`generateStaticParams` returns `[{ x: "_" }]`), and
 * `useParams()` then returns `"_"` *persistently* for that prerendered
 * file — even after the user navigates to the real URL. Any page that
 * fetches by that param ends up requesting `/api/.../_`, which 404s for a
 * resource that actually exists (this is the bug behind the inbox
 * "Open OPS-4" link landing on "Issue not found", and the same class of
 * bug on the skill / mission / chat detail routes).
 *
 * Reading `window.location.pathname` after mount sees the *actual* URL and
 * sidesteps the placeholder entirely.
 *
 * Pass a regex whose **first capture group** is the segment you want
 * (e.g. `/^\/issues\/([^/]+)\/?$/`). Returns `null` until the component
 * has mounted and when the path doesn't match — callers should treat
 * `null` as "not ready, don't fetch yet".
 *
 * `pattern` is an effect dependency, so define it at module scope (a
 * stable reference) rather than inline per render.
 *
 * Reactivity: keyed on usePathname() so it re-reads on client-side
 * navigation between sibling dynamic routes that reuse the same component
 * instance (e.g. /issues/OPS-1 → /issues/OPS-2 — no remount). usePathname
 * is the change signal; window.location.pathname stays the source of
 * truth (it never returns the "_" placeholder).
 */
export function useUrlSegment(pattern: RegExp): string | null {
  const pathname = usePathname()
  const [value, setValue] = useState<string | null>(null)
  useEffect(() => {
    if (typeof window === "undefined") return
    const m = window.location.pathname.match(pattern)
    if (!m) {
      setValue(null)
      return
    }
    // Malformed percent-encoding (e.g. "/issues/50%off") makes
    // decodeURIComponent throw — fall back to the raw segment rather than
    // crashing the page into its error boundary.
    try {
      setValue(decodeURIComponent(m[1]))
    } catch {
      setValue(m[1])
    }
  }, [pattern, pathname])
  return value
}
