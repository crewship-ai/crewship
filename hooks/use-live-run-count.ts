"use client"

import { useActiveRoutineRuns } from "@/hooks/use-active-routine-runs"
import { useActiveRuns } from "@/hooks/use-active-runs"

/**
 * How many runs are live right now: routine runs plus agent runs. The same
 * sum the Activity bell prints on its badge, for the surfaces that have no
 * bell (the phone More sheet).
 */
export function useLiveRunCount(workspaceId: string | null | undefined): number {
  const { runs } = useActiveRuns(workspaceId)
  const { activeCount } = useActiveRoutineRuns()
  return activeCount + runs.filter((r) => r.kind === "agent").length
}
