// issue-realtime.ts — #2257: the issues board never moved on its own.
//
// The list this drives (OrchestrationLayout, via OrchestrationPageShell) is
// fetched workspace-wide and filtered to one crew CLIENT-SIDE
// (hooks/use-filtered-issues.ts) — there is no per-crew fetch to
// selectively refresh. So "reconcile rather than trust the event" means:
// refetch when the change could affect what's currently on screen, skip
// when it provably can't. This is the pure decision extracted out of
// OrchestrationLayout so it's unit-testable without mounting the
// component or a RealtimeProvider.

/**
 * Every issue.* event type that means "the board might need to redraw".
 *
 * `issue.session.state` and `run.outcome` (work package B11, #2368) join
 * the set here: §17's B11 accept line names "session state" and "outcome"
 * alongside create/status-change/comment as things the board must move on
 * without a refresh. Neither payload carries `crew_id` (see the Go
 * emitters, internal/api/issue_session_realtime.go) — shouldRefetchForIssueEvent's
 * existing "crew_id missing → can't rule it out → refetch" fallback below
 * already covers that; no new branch needed.
 */
const ISSUE_BOARD_EVENT_TYPES: ReadonlySet<string> = new Set([
  "issue.created",
  "issue.updated",
  "issue.status_changed",
  "issue.started",
  "issue.deleted",
  "issues.bulk_updated",
  "issue.session.state",
  "run.outcome",
])

export function isIssueBoardEvent(type: string): boolean {
  return ISSUE_BOARD_EVENT_TYPES.has(type)
}

/**
 * Decide whether an issue.* realtime event should trigger a refetch of the
 * currently-filtered issue board.
 *
 * - `issue.deleted`'s only reliable field is `identifier` (the handler that
 *   emits it, issue_handler_update.go's Delete, never has the mission id in
 *   scope at the broadcast call site) and `issues.bulk_updated` can span
 *   crews (`{count}`, no per-row identity) — neither can be ruled out
 *   cheaply, so both always refetch.
 * - Every other issue.* event carries `crew_id` once the row exists
 *   (#2257 enriched `issue.created`/`issue.status_changed`; `issue.updated`
 *   and `issue.started` carry it on some but not all emitters). If it's
 *   present and does not match the active crew filter, the change is
 *   provably off-screen and refetching would just waste a round trip.
 * - No filter active (`filterCrewId` null — the "All crews" view) or
 *   `crew_id` missing from this particular payload: can't rule it out, so
 *   refetch.
 */
export function shouldRefetchForIssueEvent(
  type: string,
  payload: Record<string, unknown>,
  filterCrewId: string | null,
): boolean {
  if (!isIssueBoardEvent(type)) return false
  if (type === "issue.deleted" || type === "issues.bulk_updated") return true
  if (filterCrewId == null) return true

  const crewId = payload.crew_id
  if (typeof crewId !== "string") return true
  return crewId === filterCrewId
}
