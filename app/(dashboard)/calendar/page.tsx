"use client"

import { Skeleton } from "@/components/ui/skeleton"
import { PageNoAccess } from "@/components/layout/page-state"
import { useWorkspace } from "@/hooks/use-workspace"
import { useAccessMode } from "@/hooks/use-access-mode"
import { CalendarPage } from "@/components/features/calendar/calendar-page"

// /calendar — the workspace's dates in one place (Harbor 2, F-01). A view,
// not a store: each date lives on its routine (and, once the backend serves
// them, on its issue, milestone or decision).
export default function CalendarRoute() {
  const { workspaceId, loading } = useWorkspace()
  // Session-level, like Routines: a restricted account reads none of the
  // calendar's sources, so it gets no calendar (the layout's allowlist
  // normally stops it before this page renders).
  const accessMode = useAccessMode()

  if (loading || !workspaceId || accessMode === "loading") {
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
  if (accessMode === "restricted") return <PageNoAccess area="Calendar" />
  return <CalendarPage key={workspaceId} workspaceId={workspaceId} />
}
