"use client"

// Client half of persisted agent avatars (#1297).
//
// An agent's face is generated from (avatar_seed, avatar_style) by DiceBear
// on every render, which makes it a function of the installed library
// version: a dependency bump repaints the whole roster. The server can store
// a render and serve it back verbatim, but it cannot *produce* one — the
// generator is JavaScript-only. So the browser fills that gap, here.
//
// Two jobs:
//   - resolveStoredAvatarSrc: decide whether an <img> can actually load the
//     stored render, or whether the caller should generate from the seed.
//   - queueAvatarBackfill: hand the server a render for an agent that has
//     none yet.
//
// The backfill runs off ordinary page views, so everything below exists to
// keep it from becoming a nuisance: it fires at most once per agent per
// session, is capped per page load, is skipped outright when it has no
// workspace to scope the write to, and gives up for the session after a run
// of refusals or a run of failures.

import { getAgentAvatarSVG } from "@/lib/agent-avatar"
import { apiFetch } from "@/lib/api-fetch"
import { getAuthMode, withServerBase } from "@/lib/server-base"

/**
 * Per-page-load ceiling on backfill uploads.
 *
 * A large workspace can paint hundreds of avatars in one roster render.
 * Without a cap, the first visit after this ships would fire one write per
 * agent — a self-inflicted thundering herd for what is only an optimisation.
 * With it, coverage converges over a handful of visits instead, which is
 * fine: an agent that is never viewed is also never repainted in front of
 * anyone.
 */
const BACKFILL_BUDGET_PER_LOAD = 25

/** Agents already attempted this session (regardless of outcome). */
const attempted = new Set<string>()

let spent = 0

/**
 * How many refusals in a row before we stop asking for the rest of the
 * session.
 *
 * Not a latch on the first 403, which is what this used to be. Edit rights
 * are decided per agent, not per workspace (canEditAgent in rbac.go): a
 * MANAGER may edit agents they created or agents in crews they lead, and is
 * refused on everyone else's. On a multi-crew roster that means interleaved
 * 403s and successes, and a single-403 latch would disable backfill for the
 * agents that user *can* persist — the role most affected being the one the
 * feature most needs. A VIEWER, who can edit nothing, still stops after a
 * handful of attempts, which is all the latch was ever for.
 */
const MAX_CONSECUTIVE_REFUSALS = 5

/**
 * How many writes may fail in a row — for a reason that is *not* a permission
 * refusal — before we stop asking for the rest of the session.
 *
 * A refusal is a fact about one agent and is cheap to keep asking about, so it
 * is refunded out of the budget. Anything else — a 400 the next agent's write
 * will earn just as surely, a 5xx, a transport error — is a property of the
 * endpoint rather than of the agent, and refunding those meant a permanently
 * broken write path consumed no budget at all: the page re-attempted the full
 * per-load allowance on every load and never converged on giving up. #2196 is
 * what that looked like in production — eight failed writes per view of
 * `/crews`, for the life of the feature. So a run of failures latches the
 * session off the way a run of refusals does, and a failure spends its budget.
 *
 * The threshold is shared by both failure classes below (#2203) — only what
 * happens once it is reached differs.
 */
const MAX_CONSECUTIVE_FAILURES = 5

/**
 * How long a run of TRANSIENT failures (5xx, thrown/network) closes the rail
 * for, instead of the permanent stop a run of 4xx failures earns (#2203).
 *
 * `apiFetch` synthesizes a 503 whenever the original request 401s and
 * `/api/auth/token/refresh` is itself transiently unavailable — a 5xx, a
 * network throw, or its own 10s abort (lib/api-fetch.ts). That is a routine
 * event during an API restart or a deploy, which is to say: exactly the
 * window in which several tabs are open and re-rendering. A permanent latch
 * on that is #2196's bug wearing a different status code — the fix there was
 * "a broken endpoint should stop asking", not "any bad minute should stop
 * asking forever". A run of 400s is still a property of the endpoint and
 * keeps the permanent `stopped` latch below.
 */
const TRANSIENT_STOP_MS = 60_000

let consecutiveRefusals = 0
/** Consecutive 4xx (other than 403/409) — a property of the endpoint. */
let consecutiveEndpointFailures = 0
/** Consecutive 5xx or thrown/network errors — a property of the minute. */
let consecutiveTransientFailures = 0
let stopped = false
/** Set while a run of transient failures is serving out its cool-down. */
let stoppedUntil = 0

/** The per-load upload ceiling. Exported so tests don't hard-code it. */
export function avatarBackfillBudget(): number {
  return BACKFILL_BUDGET_PER_LOAD
}

/**
 * Resolve the <img> src for an agent's stored avatar, or null when the
 * caller should fall back to generating from the seed.
 *
 * Returns null in bearer mode (the desktop shell): an <img> request carries
 * no Authorization header and cookies are omitted there, so the stored URL
 * would 401 and render as a broken image. Generating from the seed is the
 * pre-persistence behaviour — never worse than today, just not better.
 */
export function resolveStoredAvatarSrc(avatarUrl: string | null | undefined): string | null {
  if (!avatarUrl) return null
  if (getAuthMode() === "bearer") return null
  // Remote-server mode points the dashboard at a different origin than the
  // page it was served from; a bare relative path would resolve against the
  // wrong host.
  return withServerBase(avatarUrl)
}

/**
 * Store a render for an agent that has none, so its face survives the next
 * generator upgrade.
 *
 * Safe to call from a render effect for every visible agent: it self-limits
 * (see the module comment) and never rejects — a failed backfill just means
 * the agent keeps generating from its seed, exactly as it does today.
 *
 * `workspaceId` is required rather than optional on purpose: the write cannot
 * succeed without it (see below), and a required parameter is what makes a
 * call site that has not got one a compile error instead of a 400 nobody
 * reads.
 */
export async function queueAvatarBackfill(
  agentId: string,
  seed: string,
  style: string | null | undefined,
  workspaceId: string | null | undefined,
): Promise<void> {
  if (!agentId || stopped) return
  if (stoppedUntil) {
    // Window still open — stay shut.
    if (Date.now() < stoppedUntil) return
    // Window elapsed: this is a fresh run's first attempt, not evidence the
    // endpoint recovered on its own — reset the run so the next agent gets a
    // clean read instead of one failure away from tripping the latch again.
    stoppedUntil = 0
    consecutiveTransientFailures = 0
  }
  // No workspace, no write. The PUT is registered behind wsCtx, which resolves
  // the workspace from ?workspace_id, a {workspaceId} path segment, or the
  // X-Workspace-ID header. This route has no workspace segment and apiFetch
  // sets no header, so a request without the query param is refused by the
  // middleware before the handler runs — which is what #2196 was: 8 of 8
  // agents, 400, on every load, for the life of the feature.
  //
  // The read side already applies this rule: agentAvatarURL
  // (internal/api/agents_avatar.go) returns nil rather than hand the client a
  // URL it knows will 400. This is the same rule on the write side.
  //
  // Returning *before* `attempted` and `spent` are touched is deliberate. The
  // workspace store resolves asynchronously, so the first paint of a roster
  // can legitimately have no id yet; burning the agent's one attempt on it
  // would leave that agent unbackfilled for the rest of the session.
  if (!workspaceId) return
  if (attempted.has(agentId)) return
  if (spent >= BACKFILL_BUDGET_PER_LOAD) return

  try {
    // Null means the style's collection is still loading and
    // getAgentAvatarSVG would otherwise have handed back a placeholder disc.
    // Storing that would freeze the wrong picture — and since the server
    // stores write-once, there is no second chance. Skip without marking the
    // agent attempted so a later render, once the import lands, can still
    // fill it in.
    //
    // Inside the try: generation calls into the avatar library, and an
    // exception escaping an un-awaited call would surface as an unhandled
    // rejection (and a Sentry report) from what is meant to be an inert
    // background nicety.
    const svg = getAgentAvatarSVG(seed, style)
    if (!svg) return

    attempted.add(agentId)
    spent++

    const res = await apiFetch(
      `/api/v1/agents/${encodeURIComponent(agentId)}/avatar` +
        `?workspace_id=${encodeURIComponent(workspaceId)}`,
      {
        method: "PUT",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ svg }),
      },
    )
    if (res.status === 403) {
      consecutiveRefusals++
      if (consecutiveRefusals >= MAX_CONSECUTIVE_REFUSALS) stopped = true
      // Refused writes cost nothing to store, so don't let them eat the
      // budget that successful ones need; the run-of-refusals latch above is
      // what bounds them instead.
      spent--
    } else {
      // Any non-403 answer proves the caller can write *somewhere*, so the
      // run of refusals is over.
      consecutiveRefusals = 0
      if (res.status === 409) {
        // Someone else stored one first. That is the race working as
        // designed: the endpoint is healthy, so it clears both failure runs,
        // and nothing of ours was stored, so the budget comes back.
        consecutiveEndpointFailures = 0
        consecutiveTransientFailures = 0
        spent--
      } else if (!res.ok) {
        // Deliberately includes two statuses that are arguably per-agent
        // rather than per-endpoint, and so could have been exempted the way
        // 403 and 409 are: a 404 (no such agent in this workspace) and a
        // validation 400 (this SVG failed the allowlist or the 64 KiB cap).
        // Both are lumped in with the endpoint-wide failures on purpose.
        //
        // Exempting them would mean deciding, from a status code alone, that
        // the *next* agent will fare better — which is exactly the reasoning
        // that produced #2196, where every write 400'd and each one was
        // forgiven individually. And neither is reachable in a way that would
        // cost anything today: every call site passes a real workspace-scoped
        // agent id, and a soft-deleted agent keys off `expired_at`, not a
        // status the roster can still render. If a surface ever does render
        // agents it cannot write to, revisit this — with a test that tells
        // the two cases apart.
        //
        // A 5xx is a different animal (#2203): `apiFetch` itself synthesizes
        // a 503 whenever token refresh is transiently unavailable, so it is
        // reachable on every write in flight during an ordinary deploy —
        // never evidence this endpoint is broken the way a 400 is.
        if (res.status >= 500) {
          noteTransientFailure()
        } else {
          noteEndpointFailure()
        }
      } else {
        consecutiveEndpointFailures = 0
        consecutiveTransientFailures = 0
      }
    }
  } catch {
    // Offline, aborted navigation, server restart. The avatar still renders
    // from its seed; a later session retries. Unlike a refusal the attempt
    // keeps its budget — what the budget limits is requests fired, not bytes
    // stored — and a run of these gives up for a while (#2203: temporarily,
    // not for the rest of the session — see noteTransientFailure).
    noteTransientFailure()
  }
}

/**
 * Record a write that failed for a reason that is a property of the
 * ENDPOINT — a 4xx other than 403/409 — and give up for the rest of the
 * session once enough arrive in a row.
 *
 * Deliberately does not refund the budget — see MAX_CONSECUTIVE_FAILURES.
 */
function noteEndpointFailure(): void {
  consecutiveEndpointFailures++
  if (consecutiveEndpointFailures >= MAX_CONSECUTIVE_FAILURES) stopped = true
}

/**
 * Record a write that failed for a reason that is a property of THIS
 * MINUTE — a 5xx, or a thrown/network error — and close the rail for
 * `TRANSIENT_STOP_MS` rather than for the rest of the session (#2203).
 *
 * Also does not refund the budget: #2199's fix (a failure spends its budget)
 * still has to hold here, or a deploy-window run of these re-attempts the
 * full per-load allowance on every subsequent load.
 */
function noteTransientFailure(): void {
  consecutiveTransientFailures++
  if (consecutiveTransientFailures >= MAX_CONSECUTIVE_FAILURES) {
    stoppedUntil = Date.now() + TRANSIENT_STOP_MS
  }
}

/** Test-only: clear the session guards between cases. */
export function _resetAvatarBackfillForTest(): void {
  attempted.clear()
  spent = 0
  stopped = false
  stoppedUntil = 0
  consecutiveRefusals = 0
  consecutiveEndpointFailures = 0
  consecutiveTransientFailures = 0
}
