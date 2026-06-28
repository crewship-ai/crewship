"use client"

import { useEffect, useState } from "react"

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
 */
export function useUrlSegment(pattern: RegExp): string | null {
  const [value, setValue] = useState<string | null>(null)
  useEffect(() => {
    if (typeof window === "undefined") return
    const m = window.location.pathname.match(pattern)
    setValue(m ? decodeURIComponent(m[1]) : null)
  }, [pattern])
  return value
}
