"use client"

import { Suspense, useEffect } from "react"
import { useRouter, useSearchParams } from "next/navigation"
import { Skeleton } from "@/components/ui/skeleton"
import { usePageWorkspace } from "@/hooks/use-page-workspace"
import { useAccessMode } from "@/hooks/use-access-mode"
import { artifactsLibrary } from "@/lib/feature-flags"
import { legacyPagesTarget } from "@/components/features/artifacts-library/legacy-pages-url"
import { LegacyPagesRedirect } from "@/components/features/artifacts-library/legacy-pages-redirect"
import { PagesLayout } from "@/components/features/pages/pages-layout"
import { useAccessContext } from "@/components/features/restricted/access-notice"
import { RestrictedPages } from "@/components/features/pages/restricted-pages"
import { PageReferenceEntry } from "@/components/features/pages/page-reference-entry"

// /pages — the Pages surface (docs/specs/pages.md §9b).
//
// Three zones: the app's icon rail, a 280px filter rail on the shared
// sidebar-kit, and the main pane — the overview here, one page's panel
// grid at /pages/<slug>. Both routes render the same shell, so the rail
// keeps its search and facets when a page is opened.
//
// Everything below the workspace gate lives in components/features/pages.
export default function PagesPage() {
  return <Suspense fallback={<Skeleton className="m-6 h-12" />}><PagesEntry /></Suspense>
}

function PagesEntry() {
  const params = useSearchParams()
  const search = params?.toString() ?? ""
  const { workspaceId, loading: wsLoading, linkUnavailable } = usePageWorkspace()
  const access = useAccessContext()
  // Session-level, not the selected workspace's row: one restricted
  // membership restricts every request the account makes (#2861).
  const accessMode = useAccessMode()
  const router = useRouter()
  // With the library on the overview lives in the Artifacts library, and a
  // reference link opens its artifact at /artifacts/<id> (#5129). A restricted
  // member's catalog stays here: the library reads are not on their allowlist.
  const target = workspaceId && accessMode !== "loading"
    ? legacyPagesTarget("/pages", search, { workspaceId, library: artifactsLibrary(), trusted: accessMode === "trusted" })
    : null
  const toLibrary = target?.to === "library"
  useEffect(() => {
    // The static export has no server redirect; replace, so Back does not return here.
    if (target?.to === "library") router.replace(target.href)
  }, [toLibrary]) // eslint-disable-line react-hooks/exhaustive-deps

  if (linkUnavailable) return <p className="p-6" role="alert">Document unavailable.</p>

  if (wsLoading || !workspaceId || accessMode === "loading" || toLibrary) {
    return (
      <div className="flex h-[calc(100dvh-var(--app-header-h)-var(--mobile-tab-bar-h))] flex-col gap-3 p-4">
        <Skeleton className="h-9 w-full" />
        <div className="flex flex-1 gap-3">
          <Skeleton className="h-full w-60" />
          <Skeleton className="h-full flex-1" />
        </div>
      </div>
    )
  }

  if (new URLSearchParams(search).has("ref")) {
    const entry = <PageReferenceEntry workspaceId={workspaceId} search={search} />
    return target?.to === "resolve" ? <LegacyPagesRedirect target={target}>{entry}</LegacyPagesRedirect> : entry
  }

  return accessMode === "restricted" ? <RestrictedPages key={workspaceId} workspaceId={workspaceId} access={access} /> : <PagesLayout workspaceId={workspaceId} />
}
