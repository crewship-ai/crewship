"use client"

import { useCallback, useEffect, useRef } from "react"
import { useRealtimeEvent, type RealtimeEvent } from "@/hooks/use-realtime"
import { shouldRefetchForIssueEvent } from "@/components/features/orchestration/issue-realtime"

/**
 * Wires the issues board to the workspace's issue.* realtime events.
 *
 * #2257 (PR #2310) registered the subscriptions for the first time — before
 * it, the board only ever reflected a live change on a manual reload — but
 * its client half was incomplete: the debounced refetch called `onRefresh`,
 * which is `OrchestrationPageShell.fetchData` (missions/crews/agents/
 * connections). The issues board itself renders a SEPARATE `issues` state
 * (`useIssuesList`, #2285/#2286), which `onRefresh` never touches. So a live
 * `issue.created`/`issue.status_changed`/etc. moved Graph/Timeline data but
 * never actually repainted the board — #2257 fixed the subscription, not
 * the board.
 *
 * Extracted out of OrchestrationLayout, in the spirit of
 * issue-realtime.ts's pure decision logic and use-issues-list.ts's fetch
 * logic, so the wiring itself is unit-testable without mounting the
 * ~1200-line component.
 *
 * `fetchIssues` should be the pagination-preserving refetch — useIssuesList's
 * `refetch`, which restores pages already loaded via loadMore instead of
 * resetting to page 1, so a live event during a "loaded more" session
 * doesn't silently shrink the board back to one page. `onRefresh` still
 * covers the mission-scoped views (Graph/Timeline/Activity) that read
 * `missions`, not `issues` — dropping it would regress THAT half. Both fire
 * together, debounced 200ms, so a lead splitting a brief into five
 * sub-issues in a few seconds triggers one pair of requests, not five.
 * `shouldRefetchForIssueEvent` additionally skips a refetch the active crew
 * filter can already prove is off-screen.
 *
 * `realtime.reconnected` also refreshes `issues` — mirroring
 * OrchestrationPageShell's own `realtime.reconnected` -> `fetchData`, which
 * likewise never covered the board's separate state, so a dropped socket
 * could leave the board wrong indefinitely. Not debounced: a reconnect is a
 * single event, not a burst — nothing to coalesce.
 */
export function useIssueBoardRealtime({
  filterCrewId,
  fetchIssues,
  onRefresh,
}: {
  /** The active crew filter, or null for "All crews" — passed straight to
   *  shouldRefetchForIssueEvent so it can skip a refetch the current view
   *  can already prove is off-screen. */
  filterCrewId: string | null
  /** The issues board's own pagination-preserving refetch (useIssuesList's
   *  `refetch`). */
  fetchIssues: () => void
  /** The mission-scoped refetch (missions/crews/agents/connections) that
   *  Graph/Timeline/Activity read from. */
  onRefresh: () => void
}): void {
  const timerRef = useRef<ReturnType<typeof setTimeout> | null>(null)

  const debouncedRefetch = useCallback(() => {
    if (timerRef.current !== null) clearTimeout(timerRef.current)
    timerRef.current = setTimeout(() => {
      timerRef.current = null
      fetchIssues()
      onRefresh()
    }, 200)
  }, [fetchIssues, onRefresh])

  useEffect(() => () => {
    if (timerRef.current !== null) clearTimeout(timerRef.current)
  }, [])

  const handleIssueBoardEvent = useCallback(
    (event: RealtimeEvent) => {
      if (shouldRefetchForIssueEvent(event.type, event.payload, filterCrewId)) {
        debouncedRefetch()
      }
    },
    [filterCrewId, debouncedRefetch],
  )

  useRealtimeEvent("issue.created", handleIssueBoardEvent)
  useRealtimeEvent("issue.updated", handleIssueBoardEvent)
  useRealtimeEvent("issue.status_changed", handleIssueBoardEvent)
  useRealtimeEvent("issue.started", handleIssueBoardEvent)
  useRealtimeEvent("issue.deleted", handleIssueBoardEvent)
  useRealtimeEvent("issues.bulk_updated", handleIssueBoardEvent)
  useRealtimeEvent("realtime.reconnected", useCallback(() => fetchIssues(), [fetchIssues]))
}
