"use client"

import { Skeleton } from "@/components/ui/skeleton"
import { useWorkspace } from "@/hooks/use-workspace"
import { PagesLayout } from "@/components/features/pages/pages-layout"

// /pages — the Pages surface (docs/prd/pages.md §9b).
//
// Three zones: the app's icon rail, a 280px filter rail on the shared
// sidebar-kit, and the main pane — the overview here, one page's panel
// grid at /pages/<slug>. Both routes render the same shell, so the rail
// keeps its search and facets when a page is opened.
//
// Everything below the workspace gate lives in components/features/pages.
export default function PagesPage() {
  const { workspaceId, loading: wsLoading } = useWorkspace()

  if (wsLoading || !workspaceId) {
    return (
      <div className="flex h-[calc(100vh-48px)] flex-col gap-3 p-4">
        <Skeleton className="h-9 w-full" />
        <div className="flex flex-1 gap-3">
          <Skeleton className="h-full w-60" />
          <Skeleton className="h-full flex-1" />
        </div>
      </div>
    )
  }

  return <PagesLayout workspaceId={workspaceId} />
}
