"use client"

import { useCallback, useRef } from "react"
import { useRealtimeEvent, type RealtimeEvent } from "@/hooks/use-realtime"
import { apiFetch } from "@/lib/api-fetch"
import { detectSeqGap } from "@/lib/issue-events-resync"

/**
 * useIssueEventGapResync — F43's client half (PRD-ISSUES-AND-ROUTINES-
 * 2026.md §2.6/§14.2/§17, work package B11, #2368): a dropped WebSocket
 * frame in the B1 event log's per-mission `seq` stream is detected and
 * resynced via `GET .../issues/{identifier}/events?after_seq=`, so a
 * surface watching an issue is never stuck stale until a manual reload —
 * only registering the type (F32) is not the same as never missing one.
 *
 * `issue.delivery.acked` (work package B2, #2337) is the one realtime
 * type today whose payload carries `seq` — `{mission_id, identifier,
 * agent_id, delivery_id, event_id, seq}`. This hook is the ONE place that
 * needs to know which types carry a seq worth tracking; a future type that
 * adds one only needs a line added to the subscription below.
 *
 * Per-mission tracking, not global: a busy workspace has many issues in
 * flight at once, and a burst on ENG-9 must never look like a gap on the
 * ENG-1 tab that has this hook mounted.
 */
export function useIssueEventGapResync(params: {
  /** The crew the resync GET is scoped under — null skips fetching (no
   *  crew resolved yet) but still tracks the seq so a later gap on the
   *  SAME jump isn't re-detected once a crew id does arrive. */
  crewId: string | null
  /** Called with the missionId, its identifier (when the triggering
   *  payload carried one), and the ordered page of events GET .../events
   *  returned. Callers decide what "apply the missed events" means for
   *  their surface — a full refetch, or splicing rows into a feed. */
  onResync: (missionId: string, identifier: string | undefined, events: unknown[]) => void
}): void {
  const { crewId, onResync } = params
  const lastSeqRef = useRef<Map<string, number>>(new Map())

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
      // Track the observed seq unconditionally — including the "can't
      // resync, no crewId yet" branch below — so a gap this hook could not
      // act on this time is not re-flagged forever once a crew id shows up
      // and every SUBSEQUENT frame keeps arriving normally.
      lastSeqRef.current.set(missionId, seq)
      if (!hasGap || !crewId) return

      const identifier = typeof payload.identifier === "string" ? payload.identifier : undefined
      const ident = identifier ?? missionId
      void apiFetch(
        `/api/v1/crews/${encodeURIComponent(crewId)}/issues/${encodeURIComponent(ident)}/events?after_seq=${afterSeq}`,
      )
        .then((r) => (r.ok ? r.json() : null))
        .then((body: { events?: unknown[]; latest_seq?: number } | null) => {
          if (!body) return
          // Trust the server's high-water mark over the frame that
          // triggered this resync — another frame may have landed while
          // the fetch was in flight.
          if (typeof body.latest_seq === "number") {
            lastSeqRef.current.set(missionId, body.latest_seq)
          }
          onResync(missionId, identifier, Array.isArray(body.events) ? body.events : [])
        })
        .catch(() => {
          // Best-effort: a failed resync leaves lastSeqRef at the newly
          // observed seq (already set above), so the NEXT consecutive
          // frame is judged against that, not against a stale cursor that
          // would re-flag the same gap forever.
        })
    },
    [crewId, onResync],
  )

  useRealtimeEvent("issue.delivery.acked", handleSeqEvent)
}
