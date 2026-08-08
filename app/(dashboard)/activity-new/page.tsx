"use client"

import { Skeleton } from "@/components/ui/skeleton"
import { useWorkspace } from "@/hooks/use-workspace"
import { ActivityStreamView } from "@/components/features/activity-new/activity-stream-view"

// /activity-new — the merged activity stream.
//
// Sits beside /activity (the run-trace canvas) rather than replacing it
// while the two are compared. The canvas is still the right tool for one
// run's execution graph; this page answers the question that came before
// it — "what is happening, and where do I look" — across runs, issues,
// approvals, keeper, cost and memory at once.
//
// Chrome is the shared SubBar + sidebar-kit; motion is the dashboard's
// Appear stagger. See components/features/activity-new/.
export default function ActivityNewPage() {
  const { workspaceId, loading } = useWorkspace()

  if (loading || !workspaceId) {
    return (
      <div className="flex h-[calc(100vh-48px)] flex-col gap-3 p-4">
        <Skeleton className="h-9 w-full" />
        <div className="flex flex-1 gap-3">
          <Skeleton className="h-full w-[280px]" />
          <Skeleton className="h-full flex-1" />
        </div>
      </div>
    )
  }

  return <ActivityStreamView workspaceId={workspaceId} />
}
