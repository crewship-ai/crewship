"use client"

import { Skeleton } from "@/components/ui/skeleton"
import { useWorkspace } from "@/hooks/use-workspace"
import { RoutinesLayout } from "@/components/features/routines/routines-layout"

// /routines — full asset-management surface for workspace routines.
// Layout mirrors /orchestration: top tab toolbar, left filter panel,
// main area, right detail panel. Tabs: Routines / Graph / Timeline /
// Activity. The orchestration page has its own Routines tab (compact
// list) for in-context invocation; this page is the management home.
//
// See PIPELINES.md §17.3 for the full architecture.
export default function RoutinesPage() {
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

  return <RoutinesLayout workspaceId={workspaceId} />
}
