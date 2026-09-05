"use client"

import { useCallback, useRef } from "react"
import { useRealtimeEvent, type RealtimeEvent } from "@/hooks/use-realtime"
import { apiFetch } from "@/lib/api-fetch"
import { detectSeqGap } from "@/lib/issue-events-resync"

/** Bounds the number of `after_seq` pages one resync will follow. Each
 *  page is capped server-side at 500 rows (issue_events_list.go's
 *  maxIssueEventsPage) — 20 pages is 10,000 events of headroom, and the
 *  cap exists only so a pathological/never-converging `latest_seq` can't
 *  spin this loop forever. */
const MAX_RESYNC_PAGES = 20

/**
 * useIssueEventGapResync — F43's client half (PRD-ISSUES-AND-ROUTINES-
 * 2026.md §2.6/§14.2/§17, work package B11, #2368): a jump in the B1
 * event log's per-mission `seq` — carried on `issue.delivery.acked`
 * frames today — is resynced via `GET .../issues/{identifier}/events?
 * after_seq=`, so a surface watching an issue is never stuck stale until
 * a manual reload — only registering the type (F32) is not the same as
 * never missing one.
 *
 * What "gap" means here, precisely (code review on #2377 caught the
 * original doc overclaiming this): `seq` is `mission_activity`'s ONE
 * counter for every kind of event on the mission — comments, status
 * changes, checkpoints, session-state changes, deliveries, all of it. It
 * is NOT a private counter for delivery acks. So two consecutive
 * `issue.delivery.acked` frames are almost never exactly `seq` and
 * `seq+1` in a mission with any other activity — the other activity
 * between them legitimately consumed the seq values in between. This
 * hook does not try to tell "a delivery-ack frame was dropped" apart
 * from "other mission activity happened between two acks that this
 * client has no other broadcast for" — it can't, with only one
 * seq-carrying type to compare against. Both cases mean the same thing
 * for a caller: there is confirmed activity on this mission the client
 * has not resolved, so resync. A resync is a safe, idempotent GET; firing
 * on ordinary interleaved activity costs an extra round trip, not
 * incorrect state — the in-flight guard below keeps that cost to at most
 * one outstanding fetch per mission at a time.
 *
 * `issue.delivery.acked` (work package B2, #2337) is the one realtime
 * type today whose payload carries `seq` — `{mission_id, identifier,
 * agent_id, delivery_id, event_id, seq}`. This hook is the ONE place that
 * needs to know which types carry a seq worth tracking; a future type
 * that adds one only needs a line added to the subscription below.
 *
 * Per-mission tracking, not global: a busy workspace has many issues in
 * flight at once, and a burst on ENG-9 must never look like a gap on the
 * ENG-1 tab that has this hook mounted.
 */
export function useIssueEventGapResync(params: {
  /** The crew the resync GET is scoped under — null skips fetching (no
   *  crew resolved yet). A gap detected before crewId is known is NOT
   *  advanced past (see the handler body) so it is still detected — and
   *  retried — once a crew id does arrive, instead of being silently
   *  forgotten (a real bug caught in review on #2377). */
  crewId: string | null
  /** Called with the missionId, its identifier (when the triggering
   *  payload carried one), and the ordered, FULLY-paged events GET
   *  .../events returned across every page this resync fetched. Callers
   *  decide what "apply the missed events" means for their surface — a
   *  full refetch, or splicing rows into a feed. */
  onResync: (missionId: string, identifier: string | undefined, events: unknown[]) => void
}): void {
  const { crewId, onResync } = params
  const lastSeqRef = useRef<Map<string, number>>(new Map())
  // Per-mission in-flight guard: a burst of acks while a resync for that
  // SAME mission is already fetching must not fan out into N concurrent,
  // redundant GETs — the fetch already in flight will catch up to
  // whatever the newest observed seq implies once it (and any page after
  // it) lands, because it always pages up to the server's OWN latest_seq,
  // not just the seq that triggered it.
  const inFlightRef = useRef<Set<string>>(new Set())

  const handleSeqEvent = useCallback(
    (event: RealtimeEvent) => {
      const payload = event.payload as {
        mission_id?: unknown
        identifier?: unknown
        seq?: unknown
      }
      const missionId = typeof payload.mission_id === "string" ? payload.mission_id : undefined
      const seq = typeof payload.seq === "number" ? payload.seq : undefined
      if (!missionId || seq === undefined) return

      const lastSeq = lastSeqRef.current.get(missionId) ?? null
      const { hasGap, afterSeq } = detectSeqGap(lastSeq, seq)

      if (!hasGap) {
        // No gap. `afterSeq` here is detectSeqGap's OWN correct next
        // cursor — `seq` for the first-ever observation or a genuinely
        // consecutive frame, but `lastSeq` (unchanged) for a stale or
        // out-of-order-OLDER frame. Setting the cursor to the raw `seq`
        // instead would regress it backwards on that last case — a real
        // bug caught in review on #2377 — so this always takes
        // detectSeqGap's answer, never the frame's own seq directly.
        lastSeqRef.current.set(missionId, afterSeq)
        return
      }

      // A gap. Deliberately do NOT advance lastSeqRef past `afterSeq`
      // (the OLD lastSeq) until a resync actually confirms it caught up —
      // three cases fall through to here and all three need the SAME
      // seq still treated as missing next time:
      //   1. No crewId yet — nothing this hook can fetch with. Advancing
      //      the cursor here would mean the gap is silently forgotten the
      //      moment a crewId later shows up, because the next frame would
      //      read as "consecutive" against a cursor that was never
      //      actually resynced (caught live in review on #2377).
      //   2. A resync for this mission is already in flight — that fetch
      //      already pages up to the server's CURRENT latest_seq, so a
      //      second, concurrent GET for the same mission would be pure
      //      waste; its own completion (below) is what advances the
      //      cursor.
      //   3. The resync itself fails (network error) — the `.catch`
      //      below intentionally does nothing, leaving the cursor at
      //      `afterSeq` so the NEXT frame retries from the same point
      //      rather than silently giving up on the missed range.
      if (!crewId || inFlightRef.current.has(missionId)) return

      const identifier = typeof payload.identifier === "string" ? payload.identifier : undefined
      const ident = identifier ?? missionId
      inFlightRef.current.add(missionId)
      void resyncMission(crewId, ident, afterSeq)
        .then((result) => {
          if (!result) return
          // Trust the paged, ACTUALLY-FETCHED high-water mark over the
          // frame that triggered this resync — another frame may have
          // landed while the fetch was in flight, and this is what makes
          // a SUBSEQUENT consecutive ack (seq = result.latestSeq + 1)
          // read as consecutive rather than as yet another gap. `Math.max`
          // against whatever is stored now is a defensive floor, not the
          // expected path — a NEWER consecutive frame could in principle
          // have already advanced the cursor past this resync's own
          // watermark while the fetch was in flight.
          lastSeqRef.current.set(missionId, Math.max(lastSeqRef.current.get(missionId) ?? 0, result.latestSeq))
          if (result.events.length > 0) onResync(missionId, identifier, result.events)
        })
        .catch(() => {
          // Best-effort: lastSeqRef was never advanced past `afterSeq`
          // for this gap, so the NEXT delivery.acked frame re-detects it
          // and retries from the same point — a failed resync does not
          // silently give up on the missed range.
        })
        .finally(() => {
          inFlightRef.current.delete(missionId)
        })
    },
    [crewId, onResync],
  )

  useRealtimeEvent("issue.delivery.acked", handleSeqEvent)
}

/**
 * resyncMission pages `GET .../events?after_seq=` starting at `afterSeq`
 * until the returned `latest_seq` is caught up to (or the page cap is
 * hit), accumulating every event across pages.
 *
 * Fixes a real bug code review caught on #2377: the original one-shot
 * version trusted `latest_seq` from the FIRST page even when the server
 * capped that page at 500 rows short of `latest_seq` — for a gap spanning
 * more than one page, the client would silently mark itself "caught up"
 * at the true high-water mark while never having fetched the middle
 * chunk. Paging here means the cursor this hook advances to is always the
 * seq of the actual last event it has in hand, not a number the server
 * merely reported.
 */
async function resyncMission(
  crewId: string,
  identifier: string,
  afterSeq: number,
): Promise<{ events: unknown[]; latestSeq: number } | null> {
  let cursor = afterSeq
  let latestSeq = afterSeq
  const events: unknown[] = []

  for (let page = 0; page < MAX_RESYNC_PAGES; page++) {
    const res = await apiFetch(
      `/api/v1/crews/${encodeURIComponent(crewId)}/issues/${encodeURIComponent(identifier)}/events?after_seq=${cursor}`,
    )
    if (!res.ok) return page === 0 ? null : { events, latestSeq: cursor }
    const body: { events?: Array<{ seq?: unknown }>; latest_seq?: unknown } = await res.json()
    const pageEvents = Array.isArray(body.events) ? body.events : []
    latestSeq = typeof body.latest_seq === "number" ? body.latest_seq : cursor

    if (pageEvents.length === 0) break
    events.push(...pageEvents)

    // Advance the cursor to the highest seq actually seen on this page —
    // NOT to `latest_seq` — so the next page request (if any) starts
    // exactly where this one left off.
    for (const e of pageEvents) {
      if (typeof e.seq === "number" && e.seq > cursor) cursor = e.seq
    }

    if (cursor >= latestSeq) break
  }

  // cursor is the true watermark this client has now confirmed events
  // for — it can be behind the server's latest_seq only if MAX_RESYNC_PAGES
  // was exhausted, which is the deliberate, documented degradation for a
  // pathological case rather than an unbounded fetch loop.
  return { events, latestSeq: cursor }
}
