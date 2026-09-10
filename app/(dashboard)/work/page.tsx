"use client"

import { Skeleton } from "@/components/ui/skeleton"
import { useWorkspace } from "@/hooks/use-workspace"
import { WorkLayout } from "@/components/features/work/work-layout"

// /work — the durable work ledger
// (docs/prd/WEBHOOKS-AGENT-PARALLELISM-IMPLEMENTATION-1-0.md §9).
//
// Everything the ledger knows is workspace-scoped and nothing here has meaning
// without a workspace, so the whole screen waits for one rather than fetching
// against an empty id and rendering an empty ledger that reads as "nothing has
// happened".
export default function WorkPage() {
  const { workspaceId, loading } = useWorkspace()

  if (loading || !workspaceId) {
    return (
      <div className="flex h-[calc(100dvh-48px)] flex-col gap-3 p-4">
        <Skeleton className="h-9 w-full" />
        <Skeleton className="h-full flex-1" />
      </div>
    )
  }
  return <WorkLayout workspaceId={workspaceId} />
}
