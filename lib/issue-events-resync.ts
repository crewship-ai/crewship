// lib/issue-events-resync.ts — the client half of F43's gap-detection and
// resync rule (PRD-ISSUES-AND-ROUTINES-2026.md §2.6/§14.2/§17, work package
// B11, #2368).
//
// F32 ("one allowlist decides what realtime exists") only guards
// REGISTRATION — whether a client accepts a frame of a given type at all.
// F43 is the separate, harder problem: `ws.Hub.dispatch` (internal/ws/hub.go)
// sends non-blocking, and a full client buffer drops the frame silently —
// logged server-side at a sampled rate, invisible to the client. A board
// that missed a frame under load has no way to notice, let alone recover,
// without a cursor to compare against. The B1 event log's per-mission `seq`
// (internal/missionactivity) IS that cursor: every event on an issue's
// realtime payload that carries one is comparable against the last seq this
// client has already seen for that mission, and a jump bigger than +1 is
// exactly a dropped frame.
//
// This file is pure decision logic — no fetch, no React — so the actual
// "gap → call GET .../events?after_seq=" wiring (hooks/use-issue-event-
// gap-resync.ts) is testable against a fake clock/fetch and this function
// is testable in isolation.

export interface SeqGapResult {
  /** True when newSeq skipped over at least one seq this client never saw. */
  hasGap: boolean
  /**
   * The `after_seq` value a resync request should use: the last seq this
   * client can prove it already has. On a gap this is the OLD lastSeq (so
   * the resync fetches everything missed, including newSeq itself); on no
   * gap it is simply newSeq (nothing to fetch, nothing lost).
   */
  afterSeq: number
}

/**
 * Decide whether observing `newSeq` for a mission represents a gap against
 * `lastSeq`, the highest seq this client has already confirmed for that
 * same mission.
 *
 * `lastSeq === null` — the first event this client session has ever seen
 * for this mission — is NEVER a gap. There is nothing to compare against:
 * the initial page load (a plain GET, not the realtime channel) is what's
 * responsible for backfilling everything before the first live frame, and
 * treating "no prior observation" as a gap would fire a redundant resync
 * on every issue a tab opens, every time.
 *
 * `newSeq <= lastSeq` — a duplicate or a frame that arrived out of order
 * (the hub makes no ordering guarantee across frames, only within one
 * dispatch) — is also not a gap; the client already has this seq or newer.
 * `afterSeq` in that case stays at `lastSeq` (not `newSeq`) so an
 * out-of-order OLD frame never moves the cursor backwards.
 */
export function detectSeqGap(lastSeq: number | null, newSeq: number): SeqGapResult {
  if (lastSeq === null) return { hasGap: false, afterSeq: newSeq }
  if (newSeq <= lastSeq) return { hasGap: false, afterSeq: lastSeq }
  if (newSeq === lastSeq + 1) return { hasGap: false, afterSeq: newSeq }
  return { hasGap: true, afterSeq: lastSeq }
}
