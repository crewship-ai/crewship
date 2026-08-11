"use client"

import { Suspense } from "react"

import { Skeleton } from "@/components/ui/skeleton"
import { useWorkspace } from "@/hooks/use-workspace"
import { ActivityStreamView } from "@/components/features/activity-stream/activity-stream-view"

// /activity — the activity stream.
//
// This route used to be the single-canvas run TRACE: pick a run from a rail,
// render its execution chain on a ReactFlow canvas. That view answered "how did
// this one run execute" and nothing else, so the question people actually
// arrived with — "what is happening, and where do I look" — had no page. It was
// built alongside on a temporary route, compared against this one, and has
// replaced it; the trace canvas and its rail are deleted rather than left as a second
// Activity nobody could tell apart from the first.
//
// The run's own execution is not lost. It moved INTO the stream: open a run
// from a workflow or a routine and the drill-down renders its steps, cost and
// journal (drill-downs.tsx → RunDrillDown), which is what the canvas was read
// for. The chain GRAPH is on the workflow page (topology-card.tsx).
//
// Suspense: the view reads useSearchParams() for the legacy deep links this
// route has always accepted (?run, ?pipeline, ?mission, ?status — see
// lib/activity-deeplink). Without a boundary the static-export build fails on
// this route, which is the same reason the trace page carried one.
export default function ActivityPage() {
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

  return (
    <Suspense fallback={null}>
      <ActivityStreamView workspaceId={workspaceId} />
    </Suspense>
  )
}
