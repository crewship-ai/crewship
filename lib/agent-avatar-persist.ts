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
// session, is capped per page load, and gives up for the session after a run
// of refusals.

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

let consecutiveRefusals = 0
let forbidden = false

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
 */
export async function queueAvatarBackfill(
  agentId: string,
  seed: string,
  style: string | null | undefined,
): Promise<void> {
  if (!agentId || forbidden) return
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

    const res = await apiFetch(`/api/v1/agents/${encodeURIComponent(agentId)}/avatar`, {
      method: "PUT",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ svg }),
    })
    if (res.status === 403) {
      consecutiveRefusals++
      if (consecutiveRefusals >= MAX_CONSECUTIVE_REFUSALS) forbidden = true
      // Refused writes cost nothing to store, so don't let them eat the
      // budget that successful ones need.
      spent--
    } else {
      // Any non-403 answer proves the caller can write *somewhere*, so the
      // run of refusals is over.
      consecutiveRefusals = 0
      if (!res.ok) spent--
    }
    // 409 means someone else stored one first. That is the race working as
    // designed and says nothing about the next agent, so it is deliberately
    // not treated like a 403.
  } catch {
    // Offline, aborted navigation, server restart. The avatar still renders
    // from its seed; a later session retries. The agent stays in `attempted`
    // so a render loop can't hammer a failing endpoint, but the budget is
    // refunded since nothing was stored.
    if (spent > 0) spent--
  }
}

/** Test-only: clear the session guards between cases. */
export function _resetAvatarBackfillForTest(): void {
  attempted.clear()
  spent = 0
  forbidden = false
  consecutiveRefusals = 0
}
