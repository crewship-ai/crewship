"use client"

import { useEffect, useReducer } from "react"
import { Search } from "lucide-react"

import { BareState } from "@/components/layout/app-frame"
import { PageNotFound } from "@/components/layout/page-state"
import { SubBar } from "@/components/layout/sub-bar"
import { isKnownAppPath } from "@/lib/app-routes"
import { useAppStore } from "@/lib/store"

const TITLE = "This page doesn’t exist"
const DESCRIPTION = "The link may be old, or the page was moved."

/**
 * The missing path as words to search: "/routnes/weekly-scan" becomes
 * "routnes weekly scan", so the palette finds the routine that was meant.
 */
export function paletteQueryForPath(pathname: string): string {
  let path = pathname
  try {
    path = decodeURIComponent(pathname)
  } catch {
    // A malformed escape: search the raw text.
  }
  return path.split(/[/\-_\s]+/).filter(Boolean).join(" ")
}

/** The 404 inside the app: the shell stays, the content says what happened. */
export function NotFoundInShell() {
  const openCommandPalette = useAppStore((s) => s.openCommandPalette)
  // Search opens the palette with the missing path typed in (CHR-03, G05).
  const openPalette = () =>
    openCommandPalette(typeof window === "undefined" ? "" : paletteQueryForPath(window.location.pathname))
  return (
    <>
      <SubBar icon={Search} title="Not found" ariaLabel="Not found" />
      <PageNotFound
        title={TITLE}
        description={DESCRIPTION}
        primary={{ label: "Go to Dashboard", href: "/" }}
        secondary={{ label: "Search", icon: Search, onClick: openPalette }}
      />
    </>
  )
}

/** The 404 without a session: the ground and the lockup, no app frame. */
export function BareNotFound() {
  return (
    <BareState>
      <PageNotFound title={TITLE} description={DESCRIPTION} primary={{ label: "Go to Dashboard", href: "/" }} />
    </BareState>
  )
}

/**
 * Whether the address bar holds a path the app has no page for.
 *
 * On the static-export deployment the Go backend answers such a path with
 * the root index.html (SPA fallback), so it is the DASHBOARD page that
 * hydrates and Next's not-found.tsx never runs; the router may even report
 * "/" as the pathname. The real URL is therefore read from window.location.
 *
 * During a client navigation the router renders the new page before it
 * writes the URL, so the check re-runs after every pathname change, once
 * the address bar is current. In the static build there is no window and it
 * answers false; callers read it only once the session is known, which is
 * never the first (hydrating) render, so the markup still matches.
 */
export function useUnknownAppPath(pathname: string | null): boolean {
  const [, recheck] = useReducer((n: number) => n + 1, 0)
  useEffect(() => { recheck() }, [pathname])
  if (pathname && pathname !== "/" && !isKnownAppPath(pathname)) return true
  if (typeof window === "undefined") return false
  return !isKnownAppPath(window.location.pathname)
}
