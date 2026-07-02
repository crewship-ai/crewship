/**
 * Ordering helpers for the chat Sessions sidebar.
 *
 * The server orders `/agents/{id}/chats` by last activity already, but the
 * client re-sorts because it also splices optimistic rows (freshly created
 * sessions) into the list and must keep them in the right place.
 */

export interface ActivitySortable {
  started_at: string
  /** Bumped server-side on every message append (migration v130). Legacy
   *  rows and optimistic client inserts may lack it — fall back to
   *  started_at. */
  last_activity_at?: string | null
}

/**
 * Parse a chats-table timestamp into epoch millis. Handles both formats
 * the backend emits: ISO-8601 with zone ("2026-07-01T10:00:00.000Z") and
 * legacy SQLite `datetime('now')` ("2026-07-01 10:00:00", implicitly UTC —
 * naive `Date.parse` would read it in the local zone and skew ordering by
 * the user's UTC offset). Unparseable/missing input returns 0 so sorting
 * stays total and garbage sinks to the bottom.
 */
export function parseSessionTimestamp(ts: string | null | undefined): number {
  if (!ts) return 0
  const normalized = ts.includes("T") ? ts : `${ts.replace(" ", "T")}Z`
  const ms = Date.parse(normalized)
  return Number.isNaN(ms) ? 0 : ms
}

/** Newest-activity-first copy of the sessions list (input not mutated). */
export function sortSessionsByActivity<T extends ActivitySortable>(sessions: T[]): T[] {
  return [...sessions].sort(
    (a, b) =>
      parseSessionTimestamp(b.last_activity_at ?? b.started_at) -
      parseSessionTimestamp(a.last_activity_at ?? a.started_at),
  )
}

/**
 * Force unread_count=0 on the currently active session in a fetched list.
 *
 * The session being viewed is read by definition, whatever the server row
 * says — the list GET can race the mark-read PUT on mount (GET served
 * first → stale non-zero count lands in state), and the "+ New session"
 * refetch returns counts as of before the PUT committed. Input is not
 * mutated; a null/absent active id is a no-op.
 */
export function withActiveSessionRead<T extends { id: string; unread_count?: number }>(
  sessions: T[],
  activeSessionId: string | null,
): T[] {
  if (!activeSessionId) return sessions
  return sessions.map((s) =>
    s.id === activeSessionId && (s.unread_count ?? 0) > 0 ? { ...s, unread_count: 0 } : s,
  )
}
