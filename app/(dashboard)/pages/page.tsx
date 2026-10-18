"use client"

import { useEffect, useState } from "react"
import { Skeleton } from "@/components/ui/skeleton"
import { useWorkspace } from "@/hooks/use-workspace"
import { useAccessMode } from "@/hooks/use-access-mode"
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
  const [search, setSearch] = useState<string>()
  useEffect(() => { setSearch(window.location.search) }, [])
  const { workspaceId, loading: wsLoading } = useWorkspace()
  const access = useAccessContext()
  // Session-level, not the selected workspace's row: one restricted
  // membership restricts every request the account makes (#2861).
  const accessMode = useAccessMode()

  if (wsLoading || !workspaceId || accessMode === "loading" || search === undefined) {
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
    return <PageReferenceEntry workspaceId={workspaceId} search={search} />
  }

  return accessMode === "restricted" ? <RestrictedPages key={workspaceId} workspaceId={workspaceId} access={access} /> : <PagesLayout workspaceId={workspaceId} />
}
